从项目生成的 zip 备份文件导入知识库。导入会替换当前全部数据；服务会先创建可恢复快照，并在校验、替换或重载失败时自动回滚。

输入参数：`$ARGUMENTS`（zip 文件路径，必填）

**第一步：确认操作**

使用 AskUserQuestion 工具向用户确认：

- 问题：`⚠️ 导入将替换当前全部向量库数据。服务会先备份并可自动回滚；确认继续？`
- 选项 A：`确认导入`
- 选项 B：`取消`（推荐）

**第二步：执行导入（仅用户确认后）**

```bash
curl -s -X POST http://127.0.0.1:8765/import \
  -F "confirm=true" \
  -F "file=@$ARGUMENTS"
```

报告响应中的 `status`、`stage`、`rolled_back` 和 `snapshot_path`：

- `status=ok`：导入成功；
- `stage=validate`：备份包被拒绝，当前知识库未修改；
- `rolled_back=true`：替换后失败，已恢复导入前知识库；
- `rolled_back=false` 且失败：恢复失败，应保留错误信息并建议检查服务日志。

旧格式、缺少 `manifest.json` 的 zip 会被拒绝；请使用当前版本重新导出后再导入。

服务未启动时提示运行 `./start.sh`。
