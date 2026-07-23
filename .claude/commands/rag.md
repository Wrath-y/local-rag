存入向量库（支持文本、飞书文档链接、单个 HTTP(S) 网页、本地 TXT/JSON/PDF/DOCX 文件和 Git 仓库）

输入参数：$ARGUMENTS

## --source 参数

若参数中包含 `--source <名称>`，提取该值作为来源标识，并从输入中移除这部分。

示例：
- `/rag 这段文字 --source 会议纪要Q1` → source = "会议纪要Q1"
- `/rag /path/to/file.txt --source 产品手册v2` → source = "产品手册v2"
- `--source` 可出现在参数任意位置

未指定 `--source` 时，按以下规则自动推断：
- 飞书文档链接 → `source` = 链接 URL
- HTTP(S) 网页 URL → `source` = 最终安全访问的 URL
- 本地文件路径 → `source` = 文件名（basename，不含目录）
- 其他文本 → `source` = "manual"

## 内容获取

根据去掉 `--source` 后的剩余输入判断类型：

1. **飞书文档链接**（含 `feishu.cn` / `larksuite.com`）→ 用 lark-doc 技能获取文档正文
2. **HTTP/HTTPS URL**（非飞书）→ 将 URL 交给服务的安全网页加载器；它只读取该页面，不执行脚本或抓取链接
3. **本地 PDF/DOCX/JSON/TXT 路径** → 用 `path` 和显式 `kind` 交给服务加载器
4. **Git 仓库**（本地白名单路径或无凭据 HTTPS/SSH 地址）→ 用 `kind: "git"`；可附带 `ref` 和额外 `exclusions`
5. **其他本地文件路径** → 用 Read 工具读取当前支持的纯文本内容
6. **其他** → 直接作为文本输入

网页加载拒绝含凭据、内网/本机目标、过多重定向、超时或过大的响应；不支持 Office 文档、压缩包或其他云盘连接器。PDF 必须含可提取的文本（不支持扫描件 OCR）。

## 写入

普通文本或已解析的飞书内容使用 `text` 和 `source`：

```bash
curl -s -X POST http://127.0.0.1:8765/ingest \
  -H "Content-Type: application/json" \
  -d "{\"text\": \"<内容>\", \"source\": \"<source>\"}"
```

网页 URL 使用 `url`，本地 PDF 使用 `path`：

```bash
curl -s -X POST http://127.0.0.1:8765/ingest \
  -H "Content-Type: application/json" \
  -d '{"url": "https://example.com/article"}'

curl -s -X POST http://127.0.0.1:8765/ingest \
  -H "Content-Type: application/json" \
  -d '{"path": "/path/to/document.pdf"}'
```

JSON、DOCX 和 Git 必须显式声明类型；Git 的远程地址不能包含用户名、密码或 token，认证仅使用主机 credential helper 或 SSH agent：

```bash
curl -s -X POST http://127.0.0.1:8765/ingest \
  -H "Content-Type: application/json" \
  -d '{"kind":"git","path":"/approved/repository","ref":"main","exclusions":["generated/"]}'
```

告知用户写入了多少个 chunk 及来源标识。

服务未启动时提示用户运行 `./start.sh`。
