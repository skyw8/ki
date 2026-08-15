# 工具契约

对外名字和 input schema 跟 Claude Code；文本结果跟 pi。包入口见 `internal/tools/doc.go`。

| 工具 | 参数 | 结果 |
|---|---|---|
| `Read` | `file_path`、`offset`、`limit`、`pages` | 原文，**不打** `cat -n`；超限提示 `offset=`。图 / PDF / `.ipynb` 单独处理 |
| `Write` | `file_path`、`content` | `Successfully wrote N bytes to …`；不要求先 Read |
| `Edit` | `file_path`、`old_string`、`new_string`、`replace_all` | 精确替换；不唯一则失败。无 `edits[]` |
| `Bash` | `command`、`timeout`（毫秒）、`description`、`run_in_background` | stdout+stderr 混排；非 0 当 error。无 sandbox |

## 细节

- 相对路径按 session cwd resolve。
- 每次 Bash 新进程，cwd 固定为 session cwd；`cd` 不跨命令。
- `run_in_background`：立刻回 task id 和 `output_file`，之后用 Read 看输出。
- 文本截断：2000 行或 50KB。Read 留头，Bash 留尾。
- 不做 `dangerouslyDisableSandbox`，prompt 里也不写。
