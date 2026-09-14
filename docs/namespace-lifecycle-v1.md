# RAG-04 namespace_lifecycle_v1

适用 local-rag OpenAPI 文档版本 `1.2.0`，运行能力以 `GET /health` 中 `namespace_lifecycle_v1=available` 为准。原 graph schema `1.0`、已有快照和任务查询保持兼容，namespace 删除使用独立任务资源。此协议不调用全局 reset。

## 公开请求与响应

所有路径前缀为 `/v1`，成功体直接返回资源，无主项目 `data` 包装。未知 JSON 字段拒绝；错误沿现有 `GraphError`，包含 code/message/retryable/details/request_id。

| 方法与路径 | 请求 | 成功响应 |
|---|---|---|
| POST `/graphs/{namespace}/snapshots/query` | `{cursor?:string,limit?:integer}`，默认20，最大100 | `namespace,items:[{version,content_hash,status,query_ready}],next_cursor:null|string,generation:string,manifest_hash:string,active_version:null|string,references:[{owner,reference_id,version,kind}],reference_revisions:{owner:revision},state` |
| POST `/graphs/{namespace}/references/sync` | `{owner,expected_revision,references:[{reference_id,version,kind}]}`；空集合必须 `[]` | `{owner,revision,references,generation,manifest_hash}` |
| POST `/graphs/{namespace}/delete` | `{expected_generation,manifest_hash,confirm:true}`，必填 `Idempotency-Key` | 202 namespace 删除任务 |
| GET `/namespace-deletion-tasks/{task_id}` | 无请求体 | 200 namespace 删除任务 |
| POST `/namespace-deletion-tasks/{task_id}/retry` | `{}` | 202 原 namespace 删除任务 |

删除任务字段：`task_id,task_kind="namespace_delete",namespace,state="pending|draining|complete|failed",phase="draining|complete",status_path,generation,manifest_hash,scope:[{version,content_hash,status,query_ready}],remaining_artifacts:[],retained_artifacts:[],retryable,complete,replayed,created_at,updated_at,error_code?`。`failed` 的 `error_code=NAMESPACE_CLEANUP_FAILED`，`retryable=true`；未完成时 remaining 包含 `namespace_online_data`。

## 固定范围和引用 CAS

快照按 `version` 升序；游标绑定 namespace、limit 和代次。客户端后续页沿用 limit。每页的 generation、manifest_hash、引用集合及 owner revisions 描述整个 namespace；items 仅包含本页。

新增/删除版本、活动指针变化、引用同步推进持久代次。generation 是不透明字符串，不得自行推算。manifest_hash 覆盖 namespace、代次、全部版本/hash、活动指针、引用和 owner revision；构建 readiness 不改变身份范围。分页中范围变化返回409 `NAMESPACE_GENERATION_CONFLICT`，调用方重做预览，不得把多代次页面拼接。确认也在同一写事务比较 generation 和 hash。

引用按 owner 独立持有。初始 revision=0，成功递增，陈旧 revision 返回409 `REFERENCE_REVISION_CONFLICT`，不覆盖其他 owner。每个 reference_id 在 owner 内唯一，version 必须实际存在于该 namespace；kind 可以为 chapter/run/experiment/backup 等业务类型。任何仍有效引用都会阻塞 namespace 和旧单快照删除，返回409 `SNAPSHOT_REFERENCED`。移除引用由调用方提供已审阅业务处置，本服务不推测释放备份引用。

引用同步本身改变范围。主项目应先同步已确认保留引用，再生成远端预览；如果用户确认了引用释放，释放成功后必须重新取远端范围，并将受审阅处置和更新后的固定范围保存在主项目任务中。不得将同步前 hash 用于删除或因409自动清空其他 owner。同步响应不确定时先查询 owner revision/集合对账。

## 禁写、收束与恢复

确认事务原子保存删除 intent、幂等记录和 namespace 禁写状态。同一 namespace、key、请求复用原 task；同 key 不同载荷或该 namespace 不同 key 返回409 `IDEMPOTENCY_CONFLICT`，完成后仍可重放。

确认后新 PUT、重建、激活、单快照删除、引用写入及查询读取被409 `NAMESPACE_DELETING` 拒绝；namespace 清单与删除任务仍可查询。数据库准入触发器阻止旧适配器重新创建 namespace 或新增任务。

已接受 queued/running 构建继续自然结束，任务保持原有恢复规则；删除显示 draining。已开始的 SQLite 读取及其请求内图缓存完成后再清理；新读取不能进入。进程重启会恢复构建任务，并自动扫描 pending/draining 删除任务。failed 只在显式 retry 后恢复原范围。

每个 namespace 在单一 SQLite 事务中移除图源、边、组件、staging、FTS 文档与虚表索引、vector 映射与虚表向量、检索代次、图索引、活动指针、构建/重建任务和引用 owner。事务完成同时保存 complete 收据。事务中断完整回滚，重试不会扩大范围或影响其他 namespace。未使用跨请求图缓存；读取缓存随完整读回调释放。

## 保留与回退边界

`retained_artifacts` 固定包含 `independent_backups`、`service_logs`、`deletion_receipt`。complete 表示在线目标图数据、索引与活动元数据清理完成，不是磁盘物理擦除或独立备份/日志擦除。保留最小 namespace tombstone、固定版本/hash 清单和幂等收据，防止延迟写入复活及支撑审计。没有自动过期策略，不暗示日志/备份已删除。

迁移为增量 `namespace_lifecycle/1`，不改 graph/1..4 历史 SQL checksum 和旧快照事实。升级前按现有 SQLite 一致性备份流程备份。回退应用功能时停止创建 complete 清理，保留新表、收据和准入触发器；旧快照仍可通过旧接口读取，引用保护触发器继续阻止绕过删除。不承诺旧二进制会显示新任务，也不得删掉 tombstone/触发器来强行重用 namespace。恢复到同一已删除 namespace 需未来明确的 RAG-05 恢复协议；本版本应使用新 namespace 重放。

实现和权威 schema 位于 `/Users/zouyi/Documents/myspace/local-rag/api/openapi.yaml`。任何服务未声明该能力时，主项目只能提供 soft 或如实标注 partial，不发送此扩展，不冒充 complete。
