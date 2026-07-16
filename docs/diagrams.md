# Local RAG — 系统图

## 1. 系统架构总览

```mermaid
graph TB
    subgraph Agents
        CC[Claude Code]
        Cursor[Cursor / Other Agent]
    end

    subgraph "rag-server 二进制"
        subgraph "HTTP 模式 (:8765)"
            GIN[Gin Router]
            HOOK_EP[POST /hook]
            INGEST_EP[POST /ingest]
            RETRIEVE_EP[POST /retrieve]
            MANAGE_EP[管理端点]
        end

        subgraph "MCP 模式 (stdio)"
            MCP[MCP Server<br/>JSON-RPC over stdio]
        end

        subgraph "核心层"
            CHUNKER[Chunker<br/>fixed/structure/semantic/agentic]
            STORE[SQLite Store<br/>vec0 + FTS5]
            PROVIDER[Provider 层<br/>Embed / Rerank / LLM]
        end
    end

    subgraph "外部服务"
        SIDECAR[Python Sidecar :8766<br/>/embed /rerank]
        OPENAI[OpenAI API]
        ANTHROPIC[Anthropic API]
    end

    CC -->|UserPromptSubmit Hook| HOOK_EP
    CC -->|MCP stdio| MCP
    Cursor -->|MCP stdio| MCP

    GIN --> INGEST_EP & RETRIEVE_EP & MANAGE_EP
    HOOK_EP --> RETRIEVE_EP

    INGEST_EP --> CHUNKER --> PROVIDER
    RETRIEVE_EP --> PROVIDER
    MCP --> CHUNKER & STORE & PROVIDER

    PROVIDER -->|HTTP| SIDECAR
    PROVIDER -->|HTTP| OPENAI
    PROVIDER -->|HTTP| ANTHROPIC

    CHUNKER --> STORE
    RETRIEVE_EP --> STORE
```

---

## 2. 入库时序图 (Ingest)

```mermaid
sequenceDiagram
    participant User as 用户 / Agent
    participant Server as rag-server
    participant Chunker as Chunker
    participant Embedder as EmbedProvider
    participant Store as SQLite Store

    User->>Server: POST /ingest {text, source}
    Server->>Server: 验证 text 非空

    Server->>Chunker: Chunk(text, source)
    alt strategy = fixed
        Chunker->>Chunker: 按句分割 → 合并至 token 区间
    else strategy = structure
        Chunker->>Chunker: 识别 MD 原子块 → 按标题分割
    else strategy = semantic
        Chunker->>Embedder: Embed(所有句子)
        Embedder-->>Chunker: [][]float32
        Chunker->>Chunker: 计算相邻余弦 → 在断裂点切分
    else strategy = agentic
        Chunker->>Server: LLM.Complete(prompt + 文本)
        Server-->>Chunker: JSON {chunks: [{start_line, end_line}]}
        Chunker->>Chunker: 解析边界 → 切片
    end
    Chunker-->>Server: []Chunk

    Server->>Embedder: Embed(chunk texts[])
    Embedder-->>Server: [][]float32

    loop 每个 chunk
        Server->>Store: InsertChunk(text, source, md5, embedding)
        Store->>Store: 检查 md5 去重
        alt 不重复
            Store->>Store: INSERT chunks + vec_chunks + FTS5 触发器
            Store-->>Server: id > 0
        else 重复
            Store-->>Server: id = 0 (skip)
        end
    end

    Server-->>User: {status: "ok", chunks_added: N}
```

---

## 3. 检索时序图 (Retrieve)

```mermaid
sequenceDiagram
    participant User as 用户 / Agent
    participant Server as rag-server
    participant Embedder as EmbedProvider
    participant Store as SQLite Store
    participant Reranker as RerankProvider

    User->>Server: POST /retrieve {text, context_tokens_used}
    Server->>Server: 计算 dynamic_top_k（如启用）

    opt Query Rewrite 已启用
        Server->>Server: LLM.Complete(rewrite prompt)
        Note right of Server: expansion / hyde / multi_query
    end

    Server->>Embedder: Embed(query_prefix + text)
    Embedder-->>Server: queryVec []float32

    Server->>Store: Retrieve(queryVec, text, opts)

    par 向量路径
        Store->>Store: vec_chunks WHERE embedding MATCH ?<br/>ORDER BY distance LIMIT top_k*10
    and BM25 路径
        Store->>Store: chunks_fts MATCH ?<br/>ORDER BY rank LIMIT top_k*10
    end

    Store->>Store: 合并去重 + 加权融合<br/>final = α·vecSim + (1-α)·bm25Norm
    Store->>Store: 排序 → 取前 top_k*3
    Store->>Store: Hydrate (JOIN chunks 表)
    Store-->>Server: []RetrieveResult

    opt Rerank 已启用
        Server->>Reranker: Rerank(query, docs, top_n)
        Reranker-->>Server: []RerankResult{index, score}
        Server->>Server: 按 rerank 分数重排
    end

    Server->>Server: 格式化: "[来源: src]\ntext"
    Server-->>User: {chunks: [...]}
```

---

## 4. Hook 自动检索时序图

```mermaid
sequenceDiagram
    participant Claude as Claude Code
    participant Hook as hook.sh
    participant Server as rag-server
    participant Store as SQLite Store

    Claude->>Claude: 用户输入 prompt
    Claude->>Hook: UserPromptSubmit 事件<br/>(stdin: {prompt, cwd, transcript_path})

    Hook->>Server: POST /hook (curl --max-time 3)

    Server->>Server: 检查 <cwd>/.rag-mode 文件
    alt .rag-mode 不存在
        Server-->>Hook: {additional_context: ""}
        Hook-->>Claude: (无输出，静默)
    else .rag-mode 存在
        Server->>Server: doRetrieve(prompt)
        Note right of Server: 走完整检索流程
        Server-->>Hook: {additional_context: "[RAG 自动检索结果]\n..."}
        Hook-->>Claude: 输出 additionalContext JSON
        Claude->>Claude: 模型看到: system + RAG结果 + 用户prompt
    end

    Note over Hook: 任何错误 → exit 0（静默，不阻塞对话）
```

---

## 5. MCP 调用时序图

```mermaid
sequenceDiagram
    participant Agent as Claude Code / Cursor
    participant MCP as rag-server mcp<br/>(stdio JSON-RPC)
    participant Core as 核心层<br/>(Chunker/Store/Provider)

    Agent->>MCP: initialize {protocolVersion, capabilities}
    MCP-->>Agent: {serverInfo, tools: [...]}

    Agent->>MCP: tools/call {name: "rag_retrieve", arguments: {query: "..."}}
    MCP->>Core: Embed(query)
    Core-->>MCP: queryVec
    MCP->>Core: Store.Retrieve(queryVec, text, opts)
    Core-->>MCP: []RetrieveResult
    MCP-->>Agent: {content: [{type: "text", text: "..."}]}

    Agent->>MCP: tools/call {name: "rag_ingest", arguments: {text: "...", source: "..."}}
    MCP->>Core: Chunker.Chunk(text)
    MCP->>Core: Embed(chunks)
    MCP->>Core: Store.InsertChunk(...)
    MCP-->>Agent: {content: [{type: "text", text: "Ingested 5 chunks"}]}
```

---

## 6. 启动流程图

```mermaid
flowchart TD
    START[./start.sh] --> CHECK_GO{Go 已安装?}
    CHECK_GO -->|否| ERROR_GO[❌ 退出: 请安装 Go]
    CHECK_GO -->|是| BUILD{rag-server 需要编译?}
    BUILD -->|是| GO_BUILD[go build -o rag-server ./cmd/server/]
    BUILD -->|否| CHECK_PROVIDER

    GO_BUILD --> CHECK_PROVIDER{embedding.provider = local?}
    CHECK_PROVIDER -->|否| START_SERVER
    CHECK_PROVIDER -->|是| CHECK_PYTHON{Python3 已安装?}
    CHECK_PYTHON -->|否| ERROR_PY[❌ 退出: 请安装 Python3]
    CHECK_PYTHON -->|是| CHECK_VENV{sidecar/.venv 存在?}
    CHECK_VENV -->|否| SETUP_VENV[创建 venv + pip install]
    CHECK_VENV -->|是| START_SERVER
    SETUP_VENV --> START_SERVER

    START_SERVER[启动 rag-server 后台进程] --> WAIT_HEALTH{/health 返回 200?}
    WAIT_HEALTH -->|30s 内成功| DONE[✅ 服务已启动]
    WAIT_HEALTH -->|超时| WARN[⚠️ 已启动但未就绪]
```

---

## 7. 服务内部启动流程图

```mermaid
flowchart TD
    MAIN[main()] --> LOAD_CFG[加载 config.yaml]
    LOAD_CFG --> CHECK_MODE{os.Args[1] == "mcp"?}

    CHECK_MODE -->|是| MCP_MODE
    CHECK_MODE -->|否| HTTP_MODE

    subgraph MCP_MODE[MCP 模式]
        M1[InitLogger error/text] --> M2[启动 Sidecar]
        M2 --> M3[初始化 Provider]
        M3 --> M4[初始化 Store]
        M4 --> M5[初始化 Chunker]
        M5 --> M6[注册 MCP Tools]
        M6 --> M7[server.Run StdioTransport<br/>阻塞等待客户端]
    end

    subgraph HTTP_MODE[HTTP 模式]
        H1[InitLogger] --> H2[启动 Sidecar]
        H2 --> H3[初始化 Provider]
        H3 --> H4[初始化 Store]
        H4 --> H5[初始化 Chunker]
        H5 --> H6[构建 Handler]
        H6 --> H7[注册 28 个 Gin 路由]
        H7 --> H8[监听 SIGINT/SIGTERM]
        H8 --> H9[r.Run :8765]
    end
```

---

## 8. Sidecar 生命周期流程图

```mermaid
flowchart TD
    START[Manager.Start] --> CHECK{provider == "local"?}
    CHECK -->|否| SKIP[跳过，不启动 sidecar]
    CHECK -->|是| SPAWN[启动 python3 sidecar/main.py --port 8766]

    SPAWN --> POLL{每 500ms 探活 /health}
    POLL -->|200 OK| READY[✅ Sidecar 就绪]
    POLL -->|超时 30s| FAIL[❌ 启动失败，kill 进程]

    READY --> LOOP[后台健康检查循环<br/>每 10s 探测一次]
    LOOP --> HEALTH_OK{/health 200?}
    HEALTH_OK -->|是| RESET_FAIL[failures = 0]
    HEALTH_OK -->|否| INC_FAIL[failures++]
    RESET_FAIL --> LOOP
    INC_FAIL --> TOO_MANY{failures >= 3?}
    TOO_MANY -->|否| LOOP
    TOO_MANY -->|是| RESTART[Kill + 重新 Start]
    RESTART --> SPAWN
```

---

## 9. 降级策略流程图

```mermaid
flowchart TD
    REQ[请求到达] --> CHECK_DB{SQLite 正常?}
    CHECK_DB -->|否| ERROR_MODE["❌ error 模式<br/>所有端点返回 503"]
    CHECK_DB -->|是| CHECK_EMBED{Embedder 可用?}

    CHECK_EMBED -->|是| NORMAL["✅ 正常模式<br/>所有功能可用"]
    CHECK_EMBED -->|否| DEGRADED["⚠️ 降级模式"]

    DEGRADED --> DEG_INGEST["/ingest → 503"]
    DEGRADED --> DEG_RETRIEVE["/retrieve → 仅 BM25 关键词搜索"]
    DEGRADED --> DEG_OTHER["其他端点 → 正常"]
```

---

## 10. 混合检索评分流程图

```mermaid
flowchart LR
    QUERY[用户查询] --> EMBED[Embed 向量化]
    QUERY --> FTS[FTS5 分词]

    EMBED --> VEC_SEARCH["vec_chunks KNN<br/>top_k × 10 候选"]
    FTS --> BM25_SEARCH["chunks_fts MATCH<br/>top_k × 10 候选"]

    VEC_SEARCH --> MERGE[合并去重]
    BM25_SEARCH --> MERGE

    MERGE --> SCORE["加权融合<br/>final = 0.7·vec + 0.3·bm25"]
    SCORE --> COARSE["粗筛: top_k × 3"]
    COARSE --> RERANK{Rerank 启用?}
    RERANK -->|是| DO_RERANK["CrossEncoder 重排"]
    RERANK -->|否| FINAL
    DO_RERANK --> FINAL["返回 top_k 结果"]
```
