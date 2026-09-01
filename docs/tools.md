# 工具契约

普通工具的对外名字和 input schema 跟 Claude Code；文本结果跟 pi。内置工具由已解析模型对应的 `ToolProfile` 选择，包入口见 `internal/tools/doc.go`。

工具执行两段化（对齐 pi prepare/execute）：先 **prepare**（找工具 → `ToolValidator.Validate` schema 校验 → `BeforeTool` / lifecycle `tool_call` sync，同步、无副作用；失败立即返回 error 结果，不执行），再 **execute**（并行/串行，`AfterTool` / `tool_result` 变换结果）。扩展订事件见 [extension.md](extension.md)。`BeforeTool` 和 `ToolResult.Terminate` 可标记 terminate：当批次内所有调用都 terminate 时主循环停止，不再请求模型（pi `shouldTerminateToolBatch`）。内置工具和扩展工具都校验 required 和参数类型。

| 工具 | 参数 | 结果 |
|---|---|---|
| `Read` | 文本模型：`file_path`、行分页 `offset` / `limit` 或字节分页 `byte_offset` / `byte_limit`；图片模型另有 `pages` | 原文，**不打** `cat -n`；返回结构化截断信息。只有 `input` 含 `image` 的模型能读图片和 PDF；`.ipynb` 按 cell |
| `Write` | `file_path`、`content` | `Successfully wrote N bytes to …`；不要求先 Read |
| `Edit` | 单次：`file_path`、`old_string`、`new_string`、`replace_all`；批量：`file_path`、`edits[]` | 精确替换；批量替换基于同一原文且不得重叠。模型只看到简短摘要，diff/patch 在 details |
| `apply_patch` | Responses custom freeform + Lark grammar | Codex 补丁格式：新增、删除、更新、移动；模型只收到 `A/M/D` 摘要或短错误，实际 diff 在 details |
| `Grep` | `pattern`、`path`、`glob`、`output_mode`、上下文/分页/类型参数 | 基于内置 ripgrep；支持 partial results、JSON/NUL 解析、EAGAIN 降级、正则、`.gitignore`、取消/超时和统计元数据 |
| `Glob` | `pattern`、`path`、`respect_gitignore` | 基于内置 ripgrep `--files`；返回按修改时间排序的路径、root、limit、截断和统计元数据 |
| `Bash` | `command`、`timeout`（毫秒）、`description`、`run_in_background` | 找到 Bash 时注册；stdout+stderr 混排并流式发送进度。非 0 当 error，前台 timeout 可转后台 |
| `PowerShell` | `command`、`timeout`（毫秒）、`description`、`run_in_background` | 仅 Windows 注册；PowerShell 原生命令、退出码、流式输出和后台任务与 Bash 使用同一生命周期 |
| `Agent` | `description`、`prompt`；可选 `subagent_type`、`model`、`run_in_background` | 从当前 session branch fork 一个 `forkMode=tree` 子 session；前台返回 `completed`，后台返回 `async_launched` 和 `outputFile` |
| `SendMessage` | `to`、`message`；可选 `summary` | 按稳定 `agentId` 在当前 run 边界 steer，或从 child transcript 续跑已完成/停止的后台 agent |
| `TaskOutput` | `task_id`、`block`、`timeout`（毫秒） | 查询或等待 shell/agent 后台任务；返回有界输出、状态、结果和输出文件路径 |
| `TaskStop` | `task_id`（或兼容的 `shell_id`） | 终止 shell/agent 后台任务并返回最终状态 |
| `Monitor` | `command`、`description` | 启动流式监控任务，逐行发送输出更新；结束时返回任务结果 |

## Read

- 相对路径按 session cwd 解析；返回原文，不添加行号。
- 普通文本和 shell spill 文件共用 `offset` / `limit` 分页；超过 2000 行或 50KB 时保留头部，并提示下一次读取的 `offset`。
- 超过 50KB 的单行使用零基 `byte_offset` / `byte_limit` 继续读取；字节分页与行分页互斥，并保证 UTF-8 字符边界。
- details 包含总字节/行数、当前输出大小、截断原因以及 `next_offset` 或 `next_byte_offset`。
- `ToolProfile.input` 含 `image` 时才支持图片、PDF 和 `pages`；文本模式在执行阶段也会拒绝图片和 PDF。
- 图片进入模型前限制到 2000×2000 和 4.5MB；需要时缩放并转成 PNG/JPEG，details 记录处理前后的尺寸、格式和大小。
- 文件访问通过可替换的 `ReadOperations`；默认本地实现会在每个文件操作前后检查取消。
- `.ipynb` 按 cell 返回。
- 不能读目录；列路径用 Glob。Bash 在部分环境上不会注册。

## Write

- 相对路径按 session cwd 解析。
- 直接创建或完整覆盖文件，不要求先调用 `Read`。
- 返回实际写入的字节数。
- 提示词要求新建或完整重写使用 `Write`，修改已有文件优先使用 `Edit`。

## Edit

- 相对路径按 session cwd 解析。
- 精确匹配 `old_string`；默认要求唯一，`replace_all=true` 时替换全部匹配。
- `edits: [{old_string,new_string}]` 是互斥的批量模式：每项在同一份原文中必须唯一且各匹配区间不得重叠，最终只写一次文件。
- 基于原始字节做精确替换，未触及的 BOM 和换行符保持不变。
- 模型可见 content 只有替换数量和路径；展示 diff、统一 patch 和首个变更行保存在 tool-result details，不进入 provider context。
- 仅未启用 freeform `apply_patch` 的模型注册，与 `apply_patch` 不同时出现。

## 文件变更并发

- server 共享按规范化主机绝对路径索引的 mutation queue；同一路径的 `Write`、`Edit`、`apply_patch` 串行，不同路径仍可并行。
- `apply_patch` 对源路径和移动目标路径排序加锁以避免死锁，但仍保持现有逐文件应用语义。
- 等待路径锁及每个目录创建、读取、写入步骤前后检查取消；当前文件操作返回后才释放锁。

## apply_patch

- 仅 `applyPatchToolType=freeform` 的模型注册，与 `Write`、`Edit` 不同时出现。
- 使用 Codex 的 `*** Begin Patch` / `*** End Patch` 格式，支持新增、删除、更新和移动文件。
- 路径通过 `filepath` 相对 session cwd 解析；源路径和 move 目标继续使用主机绝对路径与共享 mutation queue，不引入远程 filesystem 或 environment ID。
- 第一次写盘前解析并预检整份 patch：读取 delete/update 原文、定位全部 hunk、计算新内容和 unified diff，并拒绝多个操作指向同一规范化源路径。预检不是跨文件事务；I/O 失败仍可能只提交前缀。
- 执行按 patch 顺序记录 committed delta。失败 details 包含已经确认的变更和 `exact`；写入失败可能已截断目标，因此标为不精确，move 删除源失败则保留已经写入目标的 add。
- 完整旧/新内容只在执行期内存中用于生成实际 diff；持久化 details 只有 `status`、`exact` 以及有序的 path/kind/move_path/unified_diff，不进入 provider context。
- 更新保留未触及行原有的 LF、CRLF、单独 CR 和混合换行；新增行采用文件第一个换行符，无换行文件采用 LF。与 Codex 的历史行为一致，update 后保证尾部换行。
- Responses 的 `custom_tool_call_input.delta` 由增量 parser 转成 `patch_apply_updated` 预览，最多每 500ms 发送一次并补发最终 pending 快照。预览不执行文件操作；事件写入 jsonl、经现有 SSE 发送并由 WebUI 展示，最终 committed details 覆盖预览。
- 权限、approval、sandbox、多环境、远程/虚拟 workspace，以及从 Bash/PowerShell 拦截 apply_patch 均不属于该工具契约。

## Grep

- 使用编译进 ki 的 ripgrep 15.2.0，不依赖系统 `rg`；helper 按平台物化到用户缓存目录。
- 通过 argv 启动并逐行解析 JSON 输出，不经过 shell；支持正则、glob、文件类型、上下文和分页。
- 默认超时 20 秒；达到结果或原始输出上限时立即终止子进程，并保留已经解析的 partial results。
- 资源暂时不足时自动以 `-j 1` 重试一次。
- 无匹配的退出码 1 是正常空结果；取消、超时和命令错误分别返回对应错误，已有结果的超时标为截断而不是全部丢弃。
- content 模式单行最多 500 字节、最终文本最多 20KB；截断保持 UTF-8 边界，匹配上限与文本上限分别提示。
- 结果包含文件数、匹配数和 `truncated`；路径来自 JSON 字段，不解析人类可读文本。`KI_USE_SYSTEM_RIPGREP=1` 仅用于调试。

## Glob

- 使用同一内置 ripgrep 的 `--files` 和 NUL 分隔输出，不依赖 shell，特殊文件名不会破坏解析。
- 默认最多返回 100 个结果、最终文本最多 100KB；结果按修改时间排序，并包含文件数、limit 和 `truncated`。
- 结果文本和 details 都包含规范化搜索根目录。`respect_gitignore=false` 是默认值并保留 `--no-ignore` 行为；显式设为 true 时先按 ignore 规则枚举，再应用 glob，避免 ripgrep 的白名单 glob 覆盖 ignore 文件。
- 默认超时 20 秒；达到上限时保留部分结果，资源暂时不足时以 `-j 1` 重试一次。
- `KI_USE_SYSTEM_RIPGREP=1` 仅用于调试。

## Bash

- server 启动时解析一次可执行文件；Unix 依次查找 `/bin/bash` 和 PATH。
- Windows 依次查找 `KI_GIT_BASH_PATH`、`CLAUDE_CODE_GIT_BASH_PATH`、Git for Windows 安装目录和 PATH 中的 `bash.exe`。
- 找不到 Bash 时不注册 `Bash` 和 `Monitor`，server 仍正常启动。
- 每次调用都是新的 `bash -lc` 进程，会加载用户 login profile；cwd 固定为 session cwd，`cd` 不会影响后续调用。
- Bash、PowerShell、后台任务及其后代进程显式继承 Ki 启动时可见的 `HTTP_PROXY`、`HTTPS_PROXY`、`FTP_PROXY`、`ALL_PROXY`、`NO_PROXY`（含小写变体）；不会硬编码代理地址。
- stdout/stderr 混排并持续写入无损临时文件；实时增量最多每 100ms 通过 `ToolExecutionUpdate` 推送一次。
- 实时增量和模型可见结果使用跨 chunk 清理器去除 ANSI 及不可见控制字符，只保留换行、制表符和可显示文本；spill 文件仍保留原始字节。
- 超过 2000 行或 50KB 时只把 JobStore 的滚动尾部放进 tool result，并附完整输出临时文件的绝对路径，可用 `Read` 的 `offset` / `limit` 分页读取；生成最终结果不会重新把完整 spill 文件载入内存。
- 非零退出码、timeout 和取消分别返回 error、后台接管提示或 aborted 状态。
- Bash、PowerShell 和任务工具的 details 统一记录 task/status、timeout/cancel、退出码、截断统计和完整输出路径。
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

- 后台 shell 任务属于当前 serve 进程；agent 任务的 stable `agentId`、child
  session 和最近结果写入 child 目录的 `agent.json`，serve 重启时重建 agent/task
  索引。重启前仍在运行的 agent 标记为 `interrupted`，可用 `SendMessage` 续跑。
- 默认最多阻塞等待 30 秒；`block=false` 立即返回当前快照。
- 等待超时返回 `retrieval_status=timeout`，不会终止任务。
- 返回 task id/type、状态、描述、命令、PID、有界输出尾部、输出文件、退出码、错误、agent result、字节数、行数和开始/结束时间；超限日志不会被重新完整读入内存或 context。

## Agent

- `description` 是 3–5 个词的短任务名，`prompt` 是子 agent 的完整任务指令；`subagent_type` 省略时使用 `general-purpose`。
- `Agent` 在 tool call 所属 parent session 的当前 leaf 上调用 `session.ForkAt`，传入 `forkMode=tree`。子 session 复制 root → leaf 的 history、provider/model/thinking 和附件，然后把 directive 作为新的 user message 运行现有 loop。
- 子 agent 使用自己的 `runState`、extension Prepare、工具集和 `events.jsonl`；因此可以递归创建 tree child，且 child 的工具结果不会污染 parent context。主会话为深度 0，最多允许 Agent child 深度 3；深度 3 的 child 保留 `SendMessage`，但不再暴露 `Agent`。
- `run_in_background=true` 与 parent prompt 脱钩，立即返回 `{"status":"async_launched", "agentId":…, "outputFile":…}`；`TaskOutput` 可等待它，`TaskStop` 可取消它。前台 agent 返回 Claude Code 兼容的 `completed` 结果对象。
- `model` 覆盖只写入 child 的 `config.json` / `model_change`，不修改 parent session；空值继承 parent。
- `cwd` override 和 `worktree` isolation 不在模型可见 schema 中；child 始终继承 parent cwd，隔离依靠 session tree，不会静默提供未实现的隔离。
- 当前 Agent prompt/schema 只描述普通 parent → child delegation；不描述 Agent Teams 的命名成员、`team_name`、permission mode、roster 或 peer messaging。
- 后台 Agent 返回的 `agentId` 可传给 `SendMessage`；运行中消息在当前 model/tool round 后注入，已完成或停止的 child 从原 session transcript 续跑。`TaskOutput` 只读状态/结果，`TaskStop` 只停止当前 run。
- 删除 session 会终止并移除其 agent task 记录；关闭 server 会把运行中的 agent
  标记为 `interrupted`，保留 metadata 供下次启动恢复。shell 临时输出文件仍按
  原有 JobStore 生命周期清理。

## TaskStop

- 接受 `task_id`，并兼容 `shell_id`。
- 终止任务的整个进程组及其子进程、管道。
- 已完成、失败或停止的任务再次停止时返回不可停止错误。

## Monitor

- 仅找到 Bash 时注册，并启动独立的 Bash 监控任务。
- stdout/stderr 每行通过 `ToolExecutionUpdate` 和现有 SSE 流式发送。
- 命令结束后返回最终任务结果；PowerShell 长任务直接使用 `PowerShell.run_in_background`。
