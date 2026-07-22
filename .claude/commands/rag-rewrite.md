on/off 开启或关闭查询改写（Query Rewriting）模式，可选指定改写策略

输入参数：$ARGUMENTS（格式：`on` / `off` / `on --strategy <name>`）

解析参数，提取 on/off 和可选的 `--strategy` 值（expansion | hyde | multi_query），然后调用切换接口：

```bash
# 开启（使用当前策略）
curl -s -X POST "http://127.0.0.1:8765/retrieve/query-rewrite" \
  -H "Content-Type: application/json" \
  -d '{"enabled": true}'

# 开启并指定策略
curl -s -X POST "http://127.0.0.1:8765/retrieve/query-rewrite" \
  -H "Content-Type: application/json" \
  -d '{"enabled": true, "strategy": "multi_query"}'

# 关闭
curl -s -X POST "http://127.0.0.1:8765/retrieve/query-rewrite" \
  -H "Content-Type: application/json" \
  -d '{"enabled": false}'
```

三种策略说明：
- `expansion`（默认）：用 LLM 补充同义词/相关词，扩展为更丰富的查询
- `hyde`：让 LLM 生成"假设文档"，用文档向量代替查询向量（对模糊问题效果好）
- `multi_query`：生成 3 个语义相近的查询变体，分别检索后合并去重

告知用户当前 query_rewrite 状态及生效的策略。服务未启动时提示运行 `./start.sh`。
