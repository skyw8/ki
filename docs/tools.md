# 工具契约

普通工具的对外名字和 input schema 跟 Claude Code；文本结果跟 pi。内置工具由已解析模型对应的 `ToolProfile` 选择，包入口见 `internal/tools/doc.go`。

工具执行两段化（对齐 pi prepare/execute）：先 **prepare**（找工具 → `ToolValidator.Validate` schema 校验 → `BeforeTool` hook，同步、无副作用；失败立即返回 error 结果，不执行），再 **execute**（并行/串行，`AfterTool` 变换结果）。`BeforeTool` 和 `ToolResult.Terminate` 可标记 terminate：当批次内所有调用都 terminate 时主循环停止，不再请求模型（pi `shouldTerminateToolBatch`）。内置工具和 MCP 工具都校验 required 和参数类型。

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

## Read

- 相对路径按 session cwd 解析；返回原文，不添加行号。
- 超过 2000 行或 50KB 时保留头部，并提示后续读取使用的 `offset`。
- `ToolProfile.input` 含 `image` 时才支持图片、PDF 和 `pages`；文本模式在执行阶段也会拒绝图片和 PDF。
- `.ipynb` 按 cell 返回。

## Write

- 相对路径按 session cwd 解析。
- 直接创建或完整覆盖文件，不要求先调用 `Read`。
- 返回实际写入的字节数。

## Edit

- 相对路径按 session cwd 解析。
- 精确匹配 `old_string`；默认要求唯一，`replace_all=true` 时替换全部匹配。
- 仅未启用 freeform `apply_patch` 的模型注册，与 `apply_patch` 不同时出现。

## apply_patch

- 仅 `applyPatchToolType=freeform` 的模型注册，与 `Write`、`Edit` 不同时出现。
- 使用 Codex 的 `*** Begin Patch` / `*** End Patch` 格式，支持新增、删除、更新和移动文件。
- 路径通过 `filepath` 相对 session cwd 解析；更新文件统一为 LF。

## Grep

- 使用编译进 ki 的 ripgrep 15.2.0，不依赖系统 `rg`；helper 按平台物化到用户缓存目录。
- 通过 argv 启动并解析 JSON 输出，不经过 shell；支持正则、glob、文件类型、上下文和分页。
- 默认超时 20 秒；达到结果或输出上限时终止搜索，并保留已经解析的部分结果。
- 资源暂时不足时自动以 `-j 1` 重试一次。
- 结果包含文件数、匹配数和 `truncated`；`KI_USE_SYSTEM_RIPGREP=1` 仅用于调试。

## Glob

- 使用同一内置 ripgrep 的 `--files` 和 NUL 分隔输出，不依赖 shell。
- 结果按修改时间排序，并包含文件数、limit 和 `truncated`。
- 默认超时 20 秒；达到上限时保留部分结果，资源暂时不足时以 `-j 1` 重试一次。
- `KI_USE_SYSTEM_RIPGREP=1` 仅用于调试。

## Bash

- server 启动时解析一次可执行文件；Unix 依次查找 `/bin/bash` 和 PATH。
- Windows 依次查找 `KI_GIT_BASH_PATH`、`CLAUDE_CODE_GIT_BASH_PATH`、Git for Windows 安装目录和 PATH 中的 `bash.exe`。
- 找不到 Bash 时不注册 `Bash` 和 `Monitor`，server 仍正常启动。
- 每次调用都是新进程，cwd 固定为 session cwd；`cd` 不会影响后续调用。
- stdout/stderr 混排并流式发送；超过 2000 行或 50KB 时保留尾部，非零退出码返回 error。
- 前台 timeout 时，普通命令转入后台并返回 task id 和输出文件；以 `sleep` 开头的命令直接终止。
- abort 终止整个进程组及其子进程、管道；显式后台任务不受 prompt abort 影响。
- 不提供 sandbox 开关，prompt 中也不声明 `dangerouslyDisableSandbox`。

## PowerShell

- 仅 Windows 注册；优先使用 `pwsh`，其次使用 `powershell.exe`。
- 两者都找不到时工具仍注册，调用返回 unavailable。
- 使用 `-NoProfile -NonInteractive -Command`，并通过 `$LASTEXITCODE` 和 `$?` 保留 native exe 与 cmdlet 的失败状态。
- 每次调用都是新进程，cwd 固定为 session cwd；`Set-Location` 不会影响后续调用。
- 输出、截断、进程组终止和后台任务生命周期与 `Bash` 一致；前导 `Start-Sleep` 或 `sleep` 在 timeout 时直接终止。
- 不提供 sandbox 开关，prompt 中也不声明 `dangerouslyDisableSandbox`。

## TaskOutput

- 后台任务属于 session，可跨 prompt 查询；ki 重启后不恢复。
- 默认最多阻塞等待 30 秒；`block=false` 立即返回当前快照。
- 等待超时返回 `retrieval_status=timeout`，不会终止任务。
- 返回任务状态、完整输出、退出码、错误和输出文件。
- 删除 session 或关闭 server 时终止运行中的任务，并清理临时输出文件。

## TaskStop

- 接受 `task_id`，并兼容 `shell_id`。
- 终止任务的整个进程组及其子进程、管道。
- 已完成、失败或停止的任务再次停止时返回不可停止错误。

## Monitor

- 仅找到 Bash 时注册，并启动独立的 Bash 监控任务。
- stdout/stderr 每行通过 `ToolExecutionUpdate` 和现有 SSE 流式发送。
- 命令结束后返回最终任务结果；PowerShell 长任务直接使用 `PowerShell.run_in_background`。
