查看 RAG 服务状态、chunk 总数、检索命中率统计、Hook 结果与存储一致性

**第一步：获取服务状态**

```bash
curl -s http://127.0.0.1:8765/health
```

**第二步：获取检索统计**

```bash
curl -s http://127.0.0.1:8765/stats
```

**第三步：获取存储一致性**

```bash
curl -s -w "\n__HTTP_CODE__:%{http_code}" http://127.0.0.1:8765/storage/integrity-check
```

根据 HTTP 状态码展示：

- `200` + `regenerated=false`：存储一致，展示 `committed_at`（最近一次成功提交时间）、chunk/index 摘要、`wal.committed_offset` 与 `wal.committed_seq`
- `200` + `regenerated=true`：manifest 缺失已自动补齐，提示这是正常现象
- `409`：存储不一致，展示 `mismatches` 字段列表，建议用户检查是否外部文件被篡改
- `503`：chunks.pkl / index.bin 缺失或无法读取，提示可能损坏

若 `/health` 返回体的 `wal_replaying=true`，额外提示「WAL replay 进行中，稍后再查」；若 `wal_readonly_reason` 非 null，以红色/警告样式展示原因并建议检查 `storage/wal.jsonl` 末尾。

将三个接口的结果合并展示，包含：
- 服务状态（running / 未启动）
- 当前 chunk 总数
- rerank / verbose 开关状态
- 检索总次数、零命中次数、命中率（%）、平均每次返回 chunk 数
- Hook 观测：`hook_observability.total_enabled_attempts`、五种结果（`injected`、`no_results`、`timeout`、`service_unavailable`、`invalid_response`）的计数，以及 `latest` 的安全元数据
- 将 `timeout`、`service_unavailable`、`invalid_response` 明确标注为“已按 fail-open 策略放行”；这些结果不会阻塞用户对话
- **最近提交时间**（committed_at）与存储一致性状态

> 检索与 Hook 统计数据均为进程内计数，服务重启后重置；Hook 状态不包含 prompt、transcript、响应体或注入上下文。存储 manifest 跨重启保留。

服务未启动时提示用户运行 `./start.sh`。
