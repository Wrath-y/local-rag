# 条件激活与活动版本对账

接口文档版本 `1.1.0` 新增 `conditional_activation_v1`。最低兼容条件是
`service=local-rag`、`schema_version=1.0`、`api_versions` 包含 `v1`、
`supported_schema_versions` 包含 `1.0`，以及该能力状态为 `available`。
`service_version` 继续由实际构建注入，开发构建为 `0.0.0-dev`；没有已发布
二进制版本可作为最低发布版本证明，不能仅用版本字符串推断能力。

## 请求与并发保证

`POST /v1/graphs/{namespace}/snapshots/{version}/activate` 的可选 JSON：

- 不传 body 或传 `{}`：保留旧单写模式。
- `{"expected_active_version":null}`：必须尚无活动版本。
- `{"expected_active_version":"old"}`：活动版本必须等于 `old`。

服务先检查目标就绪，再在同一 SQLite 写事务中比较期望指针并切换。
独立连接或进程同时使用同一旧版本竞争不同目标时，仅一个成功，另一个
收到 HTTP `409`、`code=ACTIVE_VERSION_CONFLICT`、`retryable=false`。
`details` 含 `namespace`、可为 null 的 `expected_active_version` 和
`active_version`。比较优先于幂等判断：目标已活动但期望已过期仍然冲突。
只有期望匹配且目标本就活动时，成功返回 `changed=false`。

成功保持既有响应：`{"namespace":"project","active_version":"new","changed":true}`。
错误 body、空版本、未知成员、重复成员或非对象请求返回
`400 INVALID_SNAPSHOT_REQUEST`。目标不存在返回 `404 SNAPSHOT_NOT_FOUND`；
目标未就绪返回 `409 SNAPSHOT_NOT_READY`。缺少实际实现的服务后端不会接受并
忽略条件，而会返回 `503 GRAPH_STORE_UNAVAILABLE` 并标明缺失能力。

## 未确认结果的对账

`POST /v1/graphs/{namespace}/active/query` 接受空 body 或 `{}`，只读。
已活动时返回 `{"namespace":"project","active_version":"new","content_hash":"…"}`；
无活动版本或 namespace 不存在时返回 HTTP 200，两个字段均为 null。
指针与存储 hash 通过一条 SQL 读取，避免从不同时间点拼接。

请求超时、连接断开，或提交后的保留策略处理失败，均可能令调用方未确认
已成功的激活。先读取活动指针，只有目标版本和预期 hash 同时匹配才能确认
当下已发布。其他版本表示竞争或未完成，不能用目标快照存在、ready 或 task
成功推断已激活。明确的 `409` 保留冲突结论，并读取活动状态帮助调用方决策；
不要删去条件后重试。

## 升级与回退

此扩展复用现有 `graph_namespace_heads`，不新增物理迁移，不修改既有迁移
checksum、快照 schema、实体内容、namespace/version、content_hash 或 task_id。
停止写入并做一致性数据库备份后升级，启动时检查健康；旧快照可继续按固定
版本 GET、traverse、paths 读取。原有派生索引保留规则也不因协议升级改变。

回退前停止新协议调用并关闭进程。只回退到理解当前 graph 数据库 schema 的
版本，无需 down migration；旧服务缺失能力时，客户端必须禁用条件发布，或
在明确唯一写入者与分支锁约束下使用旧激活。旧模式未确认响应可以在锁内重发
同目标的幂等激活进行确认，但不能保证外部并发写入安全。若无法保证单写，应
禁用自动发布，不能在新语义请求失败时静默切换到旧模式。

验证覆盖独立数据库连接竞争、显式 null、严格 CAS 重放、HTTP 应答丢失后
活动读取、旧路径、升级重开、旧 hash 和固定版本读取。测试在执行宿主平台
运行；Windows/Linux 真实运行和已发布服务版本须由对应平台验收另行记录，
交叉编译不能代替它们。
