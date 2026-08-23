# 工具契约

普通工具的对外名字和 input schema 跟 Claude Code；文本结果跟 pi。内置工具由已解析模型对应的 `ToolProfile` 选择，包入口见 `internal/tools/doc.go`。

工具执行两段化（对齐 pi prepare/execute）：先**prepare**（找工具 → 可选 `ToolValidator.Validate` schema 校验 → `BeforeTool` hook，同步、无副作用；失败立即返回 error 结果，不执行），再 **execute**（并行/串行，`AfterTool` 变换结果）。`BeforeTool` 和 `ToolResult.Terminate` 可标记 terminate：当批次内所有调用都 terminate 时主循环停止，不再请求模型（pi `shouldTerminateToolBatch`）。

| 工具 | 参数 | 结果 |
|---|---|---|
| `Read` | 文本模型：`file_path`、`offset`、`limit`；图片模型另有 `pages` | 原文，**不打** `cat -n`；超限提示 `offset=`。只有 `input` 含 `image` 的模型能读图片和 PDF；`.ipynb` 按 cell |
| `Write` | `file_path`、`content` | `Successfully wrote N bytes to …`；不要求先 Read |
| `Edit` | `file_path`、`old_string`、`new_string`、`replace_all` | 精确替换；不唯一则失败。无 `edits[]` |
| `apply_patch` | Responses custom freeform + Lark grammar | Codex 补丁格式：新增、删除、更新、移动；返回 `A/M/D` 摘要 |
| `Grep` | `pattern`、`path`、`glob`、`output_mode`、上下文/分页/类型参数 | 基于内置 ripgrep；支持 partial results、JSON/NUL 解析、EAGAIN 降级、正则、`.gitignore`、取消/超时和统计元数据 |
| `Glob` | `pattern`、`path` | 基于内置 ripgrep `--files`；返回按修改时间排序的路径、limit、截断和统计元数据 |
| `Bash` | `command`、`timeout`（毫秒）、`description`、`run_in_background` | 找到 Bash 时注册；stdout+stderr 混排并流式发送进度。非 0 当 error，前台 timeout 可转后台 |
| `PowerShell` | `command`、`timeout`（毫秒）、`description`、`run_in_background` | 仅 Windows 注册；PowerShell 原生命令、退出码、流式输出和后台任务与 Bash 使用同一生命周期 |
| `TaskOutput` | `task_id`、`block`、`timeout`（毫秒） | 查询或等待后台任务，返回状态、完整输出、退出码、错误和输出文件 |
| `TaskStop` | `task_id`（或兼容的 `shell_id`） | 终止后台任务进程组并返回最终状态 |
| `Monitor` | `command`、`description` | 启动流式监控任务，逐行发送输出更新；结束时返回任务结果 |

## 细节

- 相对路径按 session cwd resolve。
- `ToolProfile` 从模型元数据生成：`input` 是否含 `image` 决定文本/富媒体 `Read`；`applyPatchToolType=freeform` 注入 `apply_patch`，否则注入 `Write` + `Edit`。两个编辑器族不会同时出现。
- 文本 `Read` 不只裁剪提示和 schema，执行时也拒绝图片/PDF，防止复用上一模型回合的旧调用。
- `apply_patch` 对齐 Codex 的 `*** Begin Patch` / `*** End Patch` 语法，默认将更新文件规范化为 LF；所有路径用 `filepath` 相对 session cwd 解析。
- server 启动时只解析一次 shell。Windows 的 Bash 查找顺序为 `KI_GIT_BASH_PATH`、`CLAUDE_CODE_GIT_BASH_PATH`、`ProgramFiles` / `ProgramFiles(x86)` 下的 Git Bash、PATH 中的 `bash.exe`；因此也接受 MSYS2、Cygwin 或 WSL Bash。Unix 依次检查 `/bin/bash` 和 PATH。
- 找不到 Bash 时 server 仍正常启动，但不注册 `Bash` 和依赖它的 `Monitor`；Windows 的 `PowerShell`、`TaskOutput` 和 `TaskStop` 不受影响。
- `PowerShell` 只在 Windows 注册，优先 `pwsh`，其次 `powershell.exe`；两者都找不到时工具仍可见，调用返回 unavailable。PowerShell 使用 `-NoProfile -NonInteractive -Command`，并用 `$LASTEXITCODE` / `$?` 保留 native exe 和 cmdlet 的失败状态。
- 每次 Bash/PowerShell 调用都是新进程，cwd 固定为 session cwd；`cd` 和 `Set-Location` 都不跨命令。
- Abort 杀掉整个 shell 进程组及其子进程、管道；结果区分 `Command aborted`、退出码和进程错误。
- 前台 `timeout` 到达时，普通命令转为 `backgrounded` 并返回 task id 和 `output_file`；Bash 的前导 `sleep` 及 PowerShell 的前导 `Start-Sleep` / `sleep` 仍终止。显式 `run_in_background` 不受 prompt abort 影响。
- 后台任务注册表属于 session，跨 prompt 保留；删除 session 或关闭 server 时终止仍运行任务并清理临时输出文件。Ki 重启后不恢复任务。
- `TaskOutput` 默认阻塞等待 30 秒；`block=false` 立即返回快照，超时返回 `retrieval_status=timeout`，不会终止任务。
- `TaskStop` 使用进程组终止任务；任务完成、失败或已停止后再次停止会返回不可停止错误。
- `Monitor` 启动独立 Bash 监控任务，每行 stdout/stderr 通过 `ToolExecutionUpdate` 和现有 SSE 发送；PowerShell 长任务直接使用 PowerShell 的 `run_in_background`。
- `Grep` 和 `Glob` 使用编译进 ki 的 ripgrep 15.2.0，不依赖系统 `rg`；运行时只在用户缓存目录物化对应平台的 helper。`KI_USE_SYSTEM_RIPGREP=1` 仅用于显式调试系统版本。
- `Grep` 使用 ripgrep JSON 输出，`Glob` 使用 NUL 分隔的 `--files` 输出；两者都通过 argv 启动，不经过 shell。默认搜索超时 20 秒，达到结果/输出上限会终止 rg 并返回截断提示；超时前已经解析的结果保留。
- ripgrep 返回 `EAGAIN` 或资源暂时不足时，自动用 `-j 1` 重试一次。
- Grep 结果附带文件数、匹配数和 `truncated`；Glob 结果附带文件数和 `truncated`。
- 文本截断：2000 行或 50KB。Read 留头，Bash/PowerShell 留尾。
- 内置工具（Read/Write/Edit/Grep/Glob/Bash/PowerShell/TaskOutput/TaskStop/Monitor）和 MCP 工具都实现 `ToolValidator`（`internal/loop/validate.go` 的最小 JSON Schema 子集：required + 类型检查），参数错误在 execute 前拦截，MCP 工具不再把坏参数透传给 server 等 400。
- 不做 `dangerouslyDisableSandbox`，prompt 里也不写。
