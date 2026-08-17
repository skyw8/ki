# 工具契约

对外名字和 input schema 跟 Claude Code；文本结果跟 pi。包入口见 `internal/tools/doc.go`。

工具执行两段化（对齐 pi prepare/execute）：先**prepare**（找工具 → 可选 `ToolValidator.Validate` schema 校验 → `BeforeTool` hook，同步、无副作用；失败立即返回 error 结果，不执行），再 **execute**（并行/串行，`AfterTool` 变换结果）。`BeforeTool` 和 `ToolResult.Terminate` 可标记 terminate：当批次内所有调用都 terminate 时主循环停止，不再请求模型（pi `shouldTerminateToolBatch`）。

| 工具 | 参数 | 结果 |
|---|---|---|
| `Read` | `file_path`、`offset`、`limit`、`pages` | 原文，**不打** `cat -n`；超限提示 `offset=`。图回 `image` 块（base64 + mime）；PDF 抽文本；`.ipynb` 按 cell |
| `Write` | `file_path`、`content` | `Successfully wrote N bytes to …`；不要求先 Read |
| `Edit` | `file_path`、`old_string`、`new_string`、`replace_all` | 精确替换；不唯一则失败。无 `edits[]` |
| `Bash` | `command`、`timeout`（毫秒）、`description`、`run_in_background` | stdout+stderr 混排；非 0 当 error。无 sandbox |

## 细节

- 相对路径按 session cwd resolve。
- 每次 Bash 新进程，cwd 固定为 session cwd；`cd` 不跨命令。
- `run_in_background`：立刻回 task id 和 `output_file`，之后用 Read 看输出。
- 文本截断：2000 行或 50KB。Read 留头，Bash 留尾。
- 内置工具（Read/Write/Edit/Bash）和 MCP 工具都实现 `ToolValidator`（`internal/loop/validate.go` 的最小 JSON Schema 子集：required + 类型检查），参数错误在 execute 前拦截，MCP 工具不再把坏参数透传给 server 等 400。
- 不做 `dangerouslyDisableSandbox`，prompt 里也不写。
