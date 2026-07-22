<div align="center">

# 🧠 Local RAG

**赋予 Claude Code 持久长期记忆 —— 从你的文档中精准检索知识**

[![Go](https://img.shields.io/badge/Go-1.22+-00ADD8?style=flat-square&logo=go&logoColor=white)](https://go.dev/)
[![SQLite](https://img.shields.io/badge/SQLite-vec0+FTS5-003B57?style=flat-square&logo=sqlite&logoColor=white)](https://github.com/asg017/sqlite-vec)
[![Gin](https://img.shields.io/badge/Gin-HTTP-00ADD8?style=flat-square)](https://gin-gonic.com/)
[![License](https://img.shields.io/badge/License-MIT-green?style=flat-square)](LICENSE)
[![Claude Code](https://img.shields.io/badge/Claude_Code-Plugin-orange?style=flat-square)](https://claude.ai/code)

[安装](#安装) · [使用方法](#使用方法) · [配置](#配置) · [架构](#架构) · [命令汇总](#命令汇总) · [工作原理](#工作原理) · [FAQ](#faq)

📖 [English](README.md)

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

### 🎯 Rerank 精排

开启后，检索结果经 cross-encoder 二次排序，提升相关性精度：

```bash
/rag-rerank on    # 开启
/rag-rerank off   # 关闭
```

> 首次开启下载 `BAAI/bge-reranker-base` 模型（约 400MB），之后进程内复用。每次检索额外约 50～200ms，不消耗 token。

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

# 存储
storage:
  db_path: "data/rag.db"
```

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

---

## 项目结构

```
local-rag/
├── cmd/server/main.go          # 入口
├── internal/
│   ├── config/                 # YAML 配置加载
│   ├── provider/               # Embed/Rerank/LLM 接口 + 实现
│   ├── store/                  # SQLite + vec0 + FTS5
│   ├── chunk/                  # 4 种分块策略
│   ├── handler/                # 全部 HTTP 端点
│   ├── agent/                  # Agent 对话 + 工具调用
│   ├── sidecar/                # Python 进程管理
│   ├── retrieve/               # 查询改写
│   └── observe/                # 指标 + 日志
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
