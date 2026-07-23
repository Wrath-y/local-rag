<div align="center">

# 🧠 Local RAG

**赋予 Claude Code 持久长期记忆 —— 从你的文档中精准检索知识**

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev/)
[![SQLite](https://img.shields.io/badge/SQLite-vec0+FTS5-003B57?style=flat-square&logo=sqlite&logoColor=white)](https://github.com/asg017/sqlite-vec)
[![Gin](https://img.shields.io/badge/Gin-HTTP-00ADD8?style=flat-square)](https://gin-gonic.com/)
[![License](https://img.shields.io/badge/License-MIT-green?style=flat-square)](LICENSE)
[![Claude Code](https://img.shields.io/badge/Claude_Code-Plugin-orange?style=flat-square)](https://claude.ai/code)

[安装](#安装) · [使用方法](#使用方法) · [检索评估](#检索评估) · [配置](#配置) · [架构](#架构) · [命令汇总](#命令汇总) · [工作原理](#工作原理) · [FAQ](#faq)

📖 [English](README.md)

🧪 [检索评估](docs/retrieval-evaluation.md)

🔧 [Agent 工具循环边界](docs/agent-tools.md)

</div>

---

## 为什么需要它？

Claude Code 无向量库，记忆系统基于文件笔记（CLAUDE.md），**无语义检索**。

| 原生限制 | 表现 | 本插件的解法 |
|---------|------|-------------|
| 关闭对话即遗忘 | 新对话 Claude 对上次内容一无所知 | 文档存本地向量库，永久保留随时可用 |
| 大文档耗大量 token | 100 页手册贴进对话，读文档就大量费用 | 只检索相关片段，其余不传输 |
| 无法跨文档语义搜索 | Claude 无法同时"记住"多份文档按语义查找 | 所有文档统一索引，按语义返回最相关内容 |

> 🔒 **所有数据本地存储，不上传任何服务器。**

---

## 安装

### 前提

| 工具 | 说明 |
|------|------|
| [Go 1.22+](https://go.dev/dl/) | 编译服务端 |
| [Python 3.8+](https://www.python.org/downloads/) | 运行 embedding 微服务（仅本地模型需要） |
| [jq](https://jqlang.github.io/jq/download/) | Hook 脚本解析 JSON，Mac 用户运行 `brew install jq` |

### 一键安装

```bash
git clone https://github.com/Wrath-y/local-rag
cd local-rag
./start.sh
```

脚本自动完成：
1. 编译 Go 二进制
2. 设置 Python 虚拟环境（如使用本地 embedding）
3. 启动服务

看到以下提示即成功：
```
RAG server started (PID: xxxxx) at http://127.0.0.1:8765
```

**重启 Claude Code 后即可使用。**

> 脚本可重复运行，不产生重复配置。移动项目目录需重新运行更新路径。

---

## 使用方法

所有操作在 Claude Code 对话框完成，输入 `/rag` 触发补全提示。

### 📥 存入文档

```bash
/rag 你的文档内容...                              # 直接粘贴文字
/rag https://xxx.feishu.cn/docx/xxx              # 飞书文档链接
/rag /path/to/file.txt                           # 本地文件路径
/rag /path/to/file.txt --source 产品手册v2        # 自定义来源标识
```

| 输入类型 | 自动推断的来源标识 |
|---------|-----------------|
| 直接文字 | `manual` |
| 飞书文档链接 | 链接 URL |
| 本地文件路径 | 文件名（如 `手册.txt`） |

### 🔍 检索知识库

```bash
/rag-retrieve Redis 缓存穿透怎么处理？
```

检索结果会包含本次请求范围内的 `[n]` 引用标记。HTTP/MCP 会额外返回结构化
`citations` 与 `evidence_token`；可将最终回答提交至
`POST /citations/validate`，校验有效、伪造或缺失的引用。完整字段、时效及不确定性
处理方式见 [引用契约](CITATIONS.md)。

### 🤖 Agent 按需检索

`POST /agent/chat` 支持让模型在回答前按需调用知识库。可直接执行只读
`rag_retrieve`；`rag_ingest`、`rag_delete_source` 和 `rag_index_rebuild` 在模型请求后
会先返回一次性授权提示。不会执行 shell、HTTP、任意文件写入或其他未注册工具。

先创建会话：

```bash
curl -X POST http://127.0.0.1:8765/agent/session
```

再携带返回的 `session_id` 发起对话：

```bash
curl -X POST http://127.0.0.1:8765/agent/chat \
  -H 'Content-Type: application/json' \
  -d '{"session_id":"<session_id>","message":"Redis 缓存穿透怎么处理？"}'
```

响应会包含 `outcome`、`citations`、`evidence_token` 和
`citation_validation`。工具调用受到轮次、调用次数、截止时间、上下文与结果大小
限制；写操作会返回 `permission_request.token`，由用户选择后调用
`POST /agent/permission/:token`（请求体包含 `session_id` 和 `approved`）执行或拒绝。
令牌会话绑定、一次性且五分钟后失效。完整契约和 provider 回退行为见
[Agent 工具循环边界](docs/agent-tools.md)。

### ⚡ 自动检索模式

开启后，每次提交 prompt 自动检索知识库并注入结果，无需手动触发：

```bash
/rag-mode on    # 开启（持久化，重启后依然有效）
/rag-mode off   # 关闭
```

### 🔄 更新文档

文档变更后重新同步，一条命令替代「删除 + 重新入库」两步：

```bash
/rag-update https://xxx.feishu.cn/docx/xxx
/rag-update /path/to/file.txt --source 产品手册v2
```

### 📊 管理知识库

```bash
/rag-status                        # 查看服务状态和 chunk 总数
/rag-sources                       # 列出所有来源及各来源 chunk 数
/rag-source-delete <来源名称>       # 删除指定来源
/rag-reset                         # 清空全部知识库
```

### 💾 导出与导入

导出会生成版本化 ZIP 包，内含一致性 SQLite 快照（`rag.db`）与 `manifest.json`。manifest 记录包/Schema 版本、创建时间、chunk 数、embedding 配置摘要、文件大小及 SHA-256 校验和。

```bash
/rag-export ~/rag-backup.zip
/rag-import ~/rag-backup.zip
```

导入必须显式确认。服务会先校验 ZIP 结构、manifest 版本、文件大小/校验和与 SQLite Schema；随后快照当前数据库、原子替换、重新加载并执行完整性检查。替换、重载或完整性校验失败时会自动回滚。

**兼容性与限制：**仅支持由当前版本导出的备份。旧版单文件 ZIP 因缺少 `manifest.json` 会被拒绝；请先使用当前服务重新导出源知识库。v1 格式只允许 `manifest.json` 和 `rag.db` 两个条目；压缩包最大 256 MiB，解压后最大 512 MiB，共两个条目。

HTTP 导入使用 multipart，且必须提供确认字段：

```bash
curl -s -X POST http://127.0.0.1:8765/import \
  -F 'confirm=true' \
  -F 'file=@~/rag-backup.zip'
```

响应包括阶段 `stage`（`validate`、`snapshot`、`replace`、`reload`、`integrity` 或 `complete`）和 `rolled_back` 状态。恢复指标：`rag_restore_total`、`rag_restore_duration_seconds`；日志不会记录文档正文。

### ♻️ 重建向量索引

在更换 embedding 模型或维度、修复历史向量时，可发起索引重建。重建为异步任务：服务会先快照 chunk ID 和文本，在影子索引中分批生成向量，并校验数量、ID、维度和一次代表性检索；全部通过后才以事务方式替换活动向量。切换成功前，检索始终使用旧索引。

```bash
curl -s -X POST http://127.0.0.1:8765/index/rebuild
curl -s http://127.0.0.1:8765/index/status
```

`POST /index/rebuild` 会返回带任务 ID 的 `202 Accepted`；重建期间重复调用会返回 `409 Conflict` 及当前任务。`GET /index/status` 返回 `normal`、`rebuilding`、`failed` 或 `read-only`，并包含时间戳、已处理/总数、进度和失败任务的安全错误分类。

重建期间，写入操作（入库、删除来源、重置和导入）会返回 `503 Service Unavailable`，请在任务结束后重试。这一临时拒绝策略可避免快照期间静默丢失写入。embedding、校验、切换或完整性检查失败时，会保留或恢复先前的活动索引；旧索引会保留在 SQLite 中以供恢复。Prometheus 指标包括 `rag_index_rebuild_total`、`rag_index_rebuild_duration_seconds`、`rag_index_rebuild_progress` 和 `rag_index_rebuild_active`，其标签不包含文档或查询文本。

### 🎯 Rerank 精排

开启后，检索结果经 cross-encoder 二次排序，提升相关性精度：

```bash
/rag-rerank on    # 开启
/rag-rerank off   # 关闭
```

> 首次开启下载 `BAAI/bge-reranker-base` 模型（约 400MB），之后进程内复用。每次检索额外约 50～200ms，不消耗 token。

---

## 检索评估

内置离线评估器使用确定性的本地 fixture corpus，并调用与 HTTP/Hook 相同的结构化检索路径。它只评估检索结果，不访问生产数据库、不生成回答，也不会调用 LLM 或 LLM 评审。

```bash
go run ./cmd/eval
```

命令会验证 `evaluation/fixtures/golden-v1`，将可复现的结果快照保存到 `artifacts/retrieval-evaluation.json`，并与已批准基线比较 Recall@K、MRR、nDCG 和来源命中率。任何指标的下降超过 `tolerances.json` 中配置的绝对容差时，命令会以非零状态退出，并输出基线、观测值、阈值、目标和每条查询的排序证据。

基线更新需要维护者显式确认套件版本，例如：

```bash
go run ./cmd/eval --output artifacts/retrieval-evaluation.json \
  --update-baseline --approve golden-v1
```

提交新的 fixture、相关性标签、容差或基线前，应一并审查它们。完整的格式、版本演进和 CI 使用说明见 [检索评估文档](docs/retrieval-evaluation.md)。

---

## 配置

编辑 `config.yaml` 调整参数：

```yaml
server:
  port: 8765

# Embedding 提供方: "local"（Python 微服务）或 "openai"（API）
embedding:
  provider: "local"
  model: "BAAI/bge-small-zh-v1.5"
  dims: 512

# Rerank: "local" / "cohere" / "jina" / "disabled"
rerank:
  provider: "local"
  model: "BAAI/bge-reranker-base"

# LLM（用于 agentic 分块和 query rewrite）
llm:
  provider: "anthropic"
  model: "claude-sonnet-4-6"
  api_key_env: "ANTHROPIC_API_KEY"

# 分块策略: fixed | structure | semantic | agentic
chunk:
  strategy: "fixed"
  min_tokens: 200
  max_tokens: 400

# 检索
retrieve:
  top_k: 3
  candidate_multiplier: 10    # 召回池 = top_k × 此值
  rerank_candidates: 9        # 送入 reranker 数量 = top_k × 3
  score_weights:
    vector: 0.7               # 向量得分权重
    bm25: 0.3                 # 关键词得分权重

# Agent 工具循环：写操作需要用户一次性授权
agent:
  max_rounds: 4
  max_tool_calls: 3
  deadline_seconds: 20
  max_context_bytes: 24000
  max_result_bytes: 12000
  max_top_k: 3

# 存储
storage:
  db_path: "data/rag.db"
```

### 使用 OpenRouter

OpenRouter 兼容 OpenAI Chat Completions API。将 provider 设为 `openrouter`
即可，服务会自动使用对应的 API 地址：

```yaml
llm:
  provider: "openrouter"
  model: "openai/gpt-4.1-mini"
  api_key_env: "OPENROUTER_API_KEY"
```

启动服务前导出 API Key：

```bash
export OPENROUTER_API_KEY="sk-or-v1-..."
```

`model` 请填写 OpenRouter 的模型 slug，例如 `anthropic/claude-sonnet-4` 或
`deepseek/deepseek-chat-v3-0324`。

### 使用第三方 Embedding API

无需 Python 环境，直接调用远端 API:

```yaml
embedding:
  provider: "openai"
  model: "text-embedding-3-small"
  dims: 1536
  api_key_env: "OPENAI_API_KEY"
  base_url: "https://api.openai.com/v1"
```

---

## 架构

```
┌─────────────────────────────────────┐
│  Go 主服务 (Gin, :8765)            │
│  ┌─────────┐  ┌──────────┐         │
│  │ Chunker │  │ Retriever│         │
│  │ (4种策略)│  │vec+FTS5  │         │
│  └────┬────┘  └────┬─────┘         │
│       │             │               │
│  ┌────▼─────────────▼────┐         │
│  │  Provider 接口层       │         │
│  │  Embed / Rerank / LLM │         │
│  └────────────┬──────────┘         │
└───────────────┼─────────────────────┘
                │ HTTP
┌───────────────▼─────────────────────┐
│  Python Sidecar (:8766)             │
│  /embed  /rerank  /health           │
│  (由 Go 管理，按需启动)              │
└─────────────────────────────────────┘
```

### 技术栈

| 层 | 方案 |
|---|---|
| 语言 | Go 1.22+ |
| HTTP 框架 | Gin |
| 向量存储 | SQLite + sqlite-vec（vec0 虚拟表） |
| 全文检索 | SQLite FTS5（BM25） |
| SQLite 驱动 | modernc.org/sqlite（纯 Go，内置 sqlite-vec） |
| Embedding/Rerank | 统一 HTTP 接口（本地微服务或第三方 API） |
| 可观测性 | Prometheus + slog |

### 混合检索流水线

```
Query → [Query Rewrite] → Embed
         │
         ├─ 向量 KNN（top_k × 10 候选）
         ├─ BM25 FTS5（top_k × 10 候选）
         │
         ├─ 分数融合: α·vec + (1-α)·bm25
         ├─ 粗筛 → top_k × 3
         ├─ [Rerank] → 最终 top_k
         │
         └─ 返回结果
```

---

## 分块策略

| 策略 | 说明 | 外部依赖 |
|------|------|---------|
| `fixed` | 按句分割，按 token 数合并 | 无 |
| `structure` | Markdown 感知：保持代码块、表格、列表完整 | 无 |
| `semantic` | 嵌入句子，在余弦相似度下降处切分 | EmbedProvider |
| `agentic` | LLM 智能判定最优分块边界 | LLMProvider |

运行时切换：
```bash
# 通过 API 切换
curl -X PUT http://127.0.0.1:8765/config/chunk-strategy \
  -H "Content-Type: application/json" \
  -d '{"strategy": "structure"}'
```

---

## 命令汇总

| 命令 | 说明 | 额外 Token |
|------|------|:----------:|
| `/rag <内容或链接> [--source <名称>]` | 存入文档 | — |
| `/rag-update <链接或路径> [--source <名称>]` | 更新来源（删旧 + 重新入库） | — |
| `/rag-retrieve <问题>` | 主动检索 | ✓ 少量 |
| `/rag-mode on/off` | 自动检索模式 | ✓ 开启时 |
| `/rag-rerank on/off` | rerank 精排 | — |
| `/rag-rewrite on/off` | 查询改写 | — |
| `/rag-verbose on/off` | 检索可观测性日志 | — |
| `/rag-status` | 服务状态 + chunk 总数 | — |
| `/rag-sources` | 所有来源及各来源 chunk 数 | — |
| `/rag-source-delete <名称>` | 按来源删除 | — |
| `/rag-reset` | 清空全部知识库 | — |
| `/rag-export` | 导出数据库备份 | — |
| `/rag-import` | 从备份导入 | — |

---

## 工作原理

RAG 通过 **Claude Code Hook 机制**拦截 prompt，发送给模型前注入检索结果：

```
用户输入 prompt
    ↓
UserPromptSubmit Hook（hook.sh）
    ├─ rag-mode off → 原样发出
    └─ rag-mode on  → POST /hook
                        ↓
                      检索相关内容
                        ↓
                      注入 additionalContext
                        ↓
                      模型看到：[system] + [RAG 结果] + [用户 prompt]
```

`additionalContext` 注入在 system prompt 层，**模型可见，用户侧不显示**，不改变对话结构。

---

## 服务管理

```bash
./start.sh    # 编译 + 启动（幂等）
./stop.sh     # 停止服务

# 健康检查
curl http://127.0.0.1:8765/health

# Prometheus 指标
curl http://127.0.0.1:8765/metrics
```

Agent 工具循环额外暴露 `rag_agent_tool_calls_total`、
`rag_agent_tool_latency_seconds` 和 `rag_agent_terminal_total`。持久化 trace 仅保留
工具名称、耗时、结果数、证据 ID 与安全错误类别，不保存提示词或检索正文。

---

## 项目结构

```
local-rag/
├── cmd/eval/main.go            # 离线检索评估命令
├── cmd/server/main.go          # 入口
├── internal/
│   ├── config/                 # YAML 配置加载
│   ├── provider/               # Embed/Rerank/LLM 接口 + 实现
│   ├── store/                  # SQLite + vec0 + FTS5
│   ├── chunk/                  # 4 种分块策略
│   ├── eval/                   # 确定性检索评估、指标与基线比较
│   ├── handler/                # 全部 HTTP 端点
│   ├── agent/                  # Agent 对话 + 工具调用
│   ├── sidecar/                # Python 进程管理
│   ├── retrieve/               # 查询改写
│   ├── retrieval/              # 生产共用的结构化检索组合
│   └── observe/                # 指标 + 日志
├── evaluation/                 # 版本化 fixture、基线、容差与 schema
├── sidecar/
│   ├── main.py                 # Python embedding/rerank 微服务
│   └── requirements.txt
├── config.yaml                 # 配置文件
├── start.sh / stop.sh          # 生命周期脚本
└── .claude/
    ├── hook.sh                 # Claude Code hook
    ├── settings.json           # Hook 注册
    └── commands/               # 斜杠命令定义
```

---

## FAQ

**Q: 使用 OpenAI embedding 还需要 Python 吗？**

不需要。设置 `embedding.provider: "openai"` 后 Python sidecar 不会启动。

**Q: 占用多少磁盘空间？**

SQLite 数据库很紧凑。10,000 个 chunk（512 维向量）约 25MB。

**Q: 可以换其他 embedding 模型吗？**

可以。修改 config.yaml 中的 `embedding.model` 和 `embedding.dims`。使用本地 provider 时，模型首次使用自动下载。

**Q: 服务崩溃会丢数据吗？**

不会。SQLite WAL 模式确保数据完整性。

**Q: 和之前的 Python 版有什么区别？**

Go 重写版优势：
- ⚡ 更快启动（~100ms vs ~3s）
- 📦 单一二进制（核心无需 pip install）
- 🔒 SQLite 取代 pickle+FAISS+手写 WAL —— 更简单、更可靠
- 🏗️ 更清晰的架构，接口明确
