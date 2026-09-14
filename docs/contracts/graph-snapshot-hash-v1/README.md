# Graph Snapshot schema 1.0 公开哈希契约

本契约适用于 `PUT /v1/graphs/{namespace}/snapshots/{version}` 的 `schema_version: "1.0"`。它独立描述可供任意语言实现的客户端规则，不要求客户端导入服务端包或访问服务端存储。现有 schema 1.0 哈希行为保持不变；若未来需要改变空集合、数值或 Unicode 处理，必须使用新 schema。

## 固定输入与输出

`cases.json` 的 `request_json` 是完整请求的 JSON 文本，使用字符串封装，以保留数值拼写、重复对象成员和转义等边界。`base` 是 delta 已就绪基线的完整图。每个有效用例固定 `expected_canonical`（精确 UTF-8 文本，无结尾换行）和 `expected_hash`（这些字节的 SHA-256，小写十六进制）。`manifest.json` 固定夹具文件本身的 SHA-256。测试不得在运行中用待测实现重新生成预期结果。

客户端应原样复制本目录并复放有效和无效用例。服务端回归位于 `tests/contract/graph_snapshot_hash_fixture_test.go`；novel-agent 的独立实现以自己的图类型复放同一文件，没有导入 local-rag 的 `internal` 类型。

## 请求解码和预检

1. 请求必须是单个 JSON 对象，拒绝任何深度重复的对象成员（转义后相等的成员也算重复）、未知的请求或实体字段、非法 JSON、后续 JSON 值。`properties` 和 `provenance` 内可自由扩展成员。
2. `schema_version` 必须为 `"1.0"`，`mode` 为 `"full"` 或 `"delta"`，线上请求的 `content_hash` 必须是 64 位小写十六进制。
3. Node 必须有非空白的 `id`、`type`、`label`、`text`；Edge 必须有非空白的 `id`、`from`、`to`、`type`。空白检查不修剪实际值，不改大小写，也不规范化 Unicode。ID 以原始解码后的字符串匹配；`"a"` 与 `" a "` 不同。
4. 每个实体的 `properties` 和 `provenance` 必须是对象。字段缺失、`null`、数组或标量无效；空对象 `{}` 有效。对象内部的 `null`、布尔值、数组和嵌套对象原样保留。
5. Node ID 在节点中唯一，Edge ID 在边中唯一；两个集合互不影响。每条最终边的两端必须存在于最终节点集合。
6. `confidence` 转换为有限 IEEE-754 binary64 值。`explicit` 关系要求该值等于 1；`inferred` 要求位于闭区间 `[0,1]`。其他 `relation_kind` 无效。
7. `inferred` 的 `provenance` 还要求：生产者从 `producer`、`model`、`algorithm` 中按顺序取第一个非空白字符串；版本从 `producer_version`、`model_version`、`algorithm_version`、`version` 中按顺序取；`generated_at` 为可解析的 RFC3339 时间；证据从 `evidence`、`evidence_refs`、`evidence_references` 中按顺序找非空字符串数组。证据数组只要选中，其任何空白项都会导致拒绝。校验取值会忽略字段值首尾空白，但哈希仍保留原文本。显式关系只要求 provenance 为对象。

## full 和 delta 物化

`full` 的最终图来自 `nodes`、`edges`；`base_version` 必须为空，任何非空 upsert/delete 列表均不允许。schema 1.0 仍容许长度为零的其他模式列表。

`delta` 必须有非空 `base_version`，基线必须存在且已就绪；不允许非空 `nodes`、`edges`。以基线图建立 ID 映射，然后应用以下规则：

- upsert 是完整记录替换，也可新增，不是字段合并；同一种实体的 upsert ID 不可重复。
- delete ID 必须存在于基线，不能重复，也不能同时出现在同种实体的 upsert 中。
- 分别应用节点和边删除及 upsert 后，对完整最终图重新执行实体、来源和悬空边校验。
- 删除节点不会自动删除关联边；调用方必须显式删除或替换对应边。
- 基线及已存在的快照保持不可变。delta 的结果与等价 full 图必须产生同一个 hash。

## 唯一参与哈希的对象

哈希对象只有以下字段，其中每个 Node/Edge 包含其上述全部实体字段（包括空对象）：

```json
{"schema_version":"1.0","nodes":[...],"edges":[...]}
```

不包括 namespace、目标或基线版本、mode、请求 hash、任务、时间、嵌入、组件及激活元数据。实体 provenance 中用户提供的时间属于实体字段，仍参与哈希。

1. 先按实体 ID 的 **UTF-8 字节字典序**升序排列 nodes、edges。不能使用依赖区域设置的排序；这与下面 JCS 对对象成员的排序不同。
2. **任何空节点/边集合都序列化为 `null`**，包括输入 `[]`、`null`、省略字段或 delta 删除全部实体。不能将它改成 `[]`。空图的精确规范文本为 `{"edges":null,"nodes":null,"schema_version":"1.0"}`，hash 为 `ea2f3969fd4da4f50197a2a83a19f4516f43f7792effc9e7d6358cf855e110a4`。
3. 对完整对象执行 [RFC 8785 JSON Canonicalization Scheme](https://www.rfc-editor.org/rfc/rfc8785)。对象键递归按 UTF-16 码元排序，实体内部其他数组保留原顺序；采用 JCS 字符串转义和 ECMAScript 数值表示。
4. Unicode 不执行 NFC/NFD 转换。组合字符和预组合字符保持不同。有效的字面字符和等价 JSON 转义产生相同文本。schema 1.0 的类型化字符串解码沿用 Go `encoding/json` 的历史行为（非法 UTF-8 或孤立代理项可被替换为 U+FFFD）；`properties/provenance` 保留原始 JSON，其中非法代理转义在 JCS 阶段拒绝。客户端不要发送非法 Unicode。
5. 数值采用 IEEE-754 binary64 的历史舍入语义，**没有额外的安全整数拒绝规则**。例如 `9007199254740993` 规范为 `9007199254740992`，`-0` 为 `0`，`1.0000000000000001` 为 `1`，`1e-999` 下溢为 `0`；数值原始拼写不影响等价结果。超出有限 binary64 的 `1e400` 无法规范化。需要精确的大整数或十进制时，应在业务 properties 中使用字符串。
6. 计算规范 UTF-8 字节的 SHA-256，编码为 64 位小写十六进制。发送方和服务端都对最终完整图计算；线上提供的 hash 与结果不同则拒绝提交。

参考实现当前固定使用 `github.com/gowebpki/jcs v1.0.1`；这只是实现参考，公开规范和夹具是跨语言依据。

## 错误边界与兼容性

无效夹具的 `reject` 表示首次拒绝阶段或稳定错误码：`decode`（JSON 解码）、`canonical`（规范化失败）、`hash`（合法格式但内容不符）、`INVALID_SNAPSHOT_REQUEST`、`DUPLICATE_NODE_ID`、`DUPLICATE_EDGE_ID`、`DANGLING_EDGE`、`INVALID_RELATION_PROVENANCE`、`INVALID_DELTA_OPERATION`。`hash` 对应线上 `CONTENT_HASH_MISMATCH`。这些输入均不可落入新快照。

schema 1.0 的历史服务端对规范化库失败返回 `INTERNAL_ERROR`；本次契约不悄悄将其改为另一套数值/Unicode接受规则。客户端应先拒绝这些输入，不能盲重试。对已存在版本的同 hash 重放，服务端可能直接返回原提交；同版本不同 hash 为 `CONTENT_HASH_CONFLICT`。新客户端仍须先校验其请求内容，不能只凭声称的 hash 跳过本地验证。

本目录只冻结图内容哈希契约。组件就绪、能力协商、条件激活及幂等重建仍由公开 OpenAPI 和服务能力描述约束。
