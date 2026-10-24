# 工具契约

工具默认使用小写 snake_case 名称，接受对应的 PascalCase 别名（如 `exec_command` /
`ExecCommand`）。每个工具只向模型发布一个 canonical schema；文件/搜索参数沿用现有契约，
shell 使用进程交互契约，agent 使用协作契约。文本结果保持有界。内置工具由
`internal/tool/builtin.Set.Build` 构造。

明确声明能力的 Responses 模型使用原生 freeform `apply_patch`，其它模型使用 `write` +
`edit`，两组编辑器互斥。`read` 按模型是否支持图片在富/文本两种模式间切换。这些选择发生在
system prompt 组装阶段（见 [architecture.md](architecture.md)）。

## 模块边界

| 包 | 职责 |
|---|---|
| `internal/tool` | `Tool`、`Result`、`Spec`、可选执行接口、名称/别名、注册表和参数校验；不依赖 loop、内置实现或运行时 |
| `internal/tool/builtin` | 根据模型能力组装工具，并应用内置开关；`file`、`shell`、`agent`、`question` 子包只负责对应工具的 schema、参数和结果适配。文件变更队列归 `file` 所有 |
| `internal/tool/builtin/catalog` | 内置名称的唯一声明来源；工具实现使用其标识符，extension 仅依赖这份轻量目录检查保留名 |
| `internal/tool/output` | 完整输出文件、预览、配额和清理；loop 在 `AfterTool` 后应用统一策略，process 通过 `OutputSpool` 接口复用同一 store |
| `internal/agent` | 身份、调度、消息接纳、持久化和代次进度；server 将 `loop.Event` 转成有界 `agent.ProgressEvent`，运行时不直接依赖 loop |
| `internal/process` | shell、PTY、进程组及增量输出；shell 工具将调用上下文转换为显式 `process.Identity`，extension sidecar 复用进程组控制 |
| `internal/loop` | 编排工具准备/执行、hooks 和事件；`internal/server` 注入 session 所属运行时并组装每轮工具 |

模型工具名、参数和输出协议不受包组织影响。

## 工具清单

| 工具 | 参数 | 结果 |
|---|---|---|
| `read` | 文本模型：`file_path`、可选行分页 `offset` / `limit`，长行续读 `byte_offset`；图片模型另有 `pages` | 原文，**不打** `cat -n`；返回结构化截断信息。只有 `input` 含 `image` 的模型能读图片和 PDF；`.ipynb` 按 cell |
| `write` | `file_path`、`content` | `Successfully wrote N bytes to …`；不要求先 read。details 带 `path`、`bytes`、`lines`、`created` |
| `edit` | 单次：`file_path`、`old_string`、`new_string`、`replace_all`；批量：`file_path`、`edits[]` | 精确替换；批量替换基于同一原文且不得重叠。模型只看到简短摘要，diff/patch 在 details |
| `apply_patch` | Codex `*** Begin Patch` freeform grammar；每个路径只出现一次，同文件修改合并进一个 update block | 明确声明 native patch 能力的 add/update/delete/move 批量补丁；完整预检后才写入，details 带每个文件的 unified diff |
| `grep` | `pattern`、`path`、`glob`、`output_mode`、`respect_gitignore`、上下文/分页/类型参数 | 基于内置 ripgrep；默认尊重 `.gitignore`；details 与模型看到同一页 |
| `glob` | `pattern`、`path`、`respect_gitignore` | 基于内置 ripgrep `--files`；返回按修改时间排序的路径、root、limit、截断和统计元数据 |
| `exec_command` | `cmd`、可选 `workdir` / `shell` / `login` / `tty` / `yield_time_ms` / `max_output_tokens` / `include_summary` | 启动新进程，返回增量输出、退出码或数值 `session_id` |
| `write_stdin` | `session_id`、可选 `chars` / `yield_time_ms` / `max_output_tokens` / `include_summary` | 写入 PTY 或观察新输出；Ctrl-C 是中断请求 |
| `spawn_agent` | `task_name`、`message`、可选 `fork_turns` / `model` / `effort` | 异步创建具名 child，返回 canonical task path 和 agent/session ID；model/effort 未指定时按 parent 规则继承 |
| `send_message` | `target`、`message` | QueueOnly：接受上下文，唤醒正在等待的目标，不启动 idle agent |
| `followup_task` | `target`、`message` | TriggerTurn：在同一 agent 上开始新任务；busy 时持久排队，root 拒绝 |
| `wait_agent` | 可选 `timeout_ms` | 观察调用方 mailbox / user steer；不消费完成结果、不停止 agent |
| `interrupt_agent` | `target` | 中断目标及所有后代 turn，丢弃 pending tasks，返回目标之前状态；保留身份，shell 进程继续运行 |
| `list_agents` | 可选 `path_prefix` | root 范围内的身份/代次/状态；至多 128 项，另带 total/truncated |
| `request_user_input_async` | `questions[]`：必填 `title`，可选非空字符串数组 `options` | 持久接受后立即返回 `{"accepted":true}`；答案之后作为新的 user message 到达 |
| `exec` | raw JS 或 `{code}`，可选首行 pragma | 显式输出和 completed/failed/running 状态；running 返回 cell ID |
| `wait` | `cell_id`、可选 `yield_time_ms` / `max_tokens` / `terminate` | 仅新增 cell 输出或终态 |

## 执行模型

### 异步用户提问

`request_user_input_async` 沿用 Codex 的 description 与 schema，不受模型名称或 Plan 模式限制。
普通主会话与子代理绑定相同 schema 和 host callback，保持继承历史时的 provider 缓存前缀。
工具 description、共享 system 与 child user 信封均说明仅主会话可以调用；callback 执行时
拒绝子代理，且不创建问题。只读旁聊不提供此工具。工具接受一批问题，
全部验证后一次提交，不等待用户、不占用等待 channel，也不结束当前 turn。它是直接控制工具，
不出现在 Code Mode 的 `tools` 命名空间。工具目录始终保留其名称及独立全局开关。

标题、选项必须为非空白字符串；问题数组及显式传入的选项数组不能为空，拒绝额外字段。
参数 JSON 总预算为 64 KiB。成功正文精确为 `{"accepted":true}`，内部 question ID 不由模型生成。
预选第一项只是 UI 草稿，未提交不是回答，时间流逝也不是批准。依赖回答的任务应等待新的
user 输入，而不是使用轮询、`sleep` 或重复调用工具来保持运行。

问题、显式答案投递和逐题只读旁聊的完整契约见
[异步提问与旁聊](async-user-input.md)。该工具不替代危险操作的执行权限或审核。

### 旁聊只读文件能力

旁聊只提供规范化的 `read`、`grep`、`glob`；没有 shell、写工具、Code Mode、MCP、
执行扩展或子代理。warmup / reload、输入、消息与生命周期钩子、compact 均跳过执行扩展。
冻结资源可用于提示背景，不赋予资源中描述的工具能力。

文件根来自提问时规范化的工作区及旁聊自有附件目录。逐层 `os.Root` 打开与文件身份核验
固定根，内容读取和搜索枚举都经过根句柄；验证后根或祖先换成符号链接不会让搜索进程越界。
旁聊搜索不启动外部 rg，采用 Go RE2 与 glob；不支持的正则或文件类型显式报错，可用 glob
过滤其他类型。ignore 只读取工作区内 `.gitignore` / `.ignore` / `.rgignore`，不读取宿主
全局配置。搜索上限为 5 秒、10000 个目录项、单文件 2 MiB、累计内容 32 MiB、1000 条
结果及 48 KiB 正文；触达预算明确返回截断提示，可缩小路径后继续。

### 准备与执行

工具执行两段化（对齐 pi prepare/execute）：

1. **prepare**：找工具 → `tool.Validator.Validate` schema 校验 → `BeforeTool` / lifecycle
   `tool_call` sync。同步、无副作用；失败立即返回 error 结果，不执行。
2. **execute**：并行/串行执行，`AfterTool` / `tool_result` 变换结果。

已持久化的 assistant toolCalls 必须逐一配对 toolResult。模型输出因中断或失败结束时，
其中的调用只生成 error toolResult，不执行；批次执行期间取消也会为尚未启动的调用生成
error toolResult，避免恢复后留下悬空调用或补执行已取消的副作用。

扩展订阅事件见 [extension.md](extension.md)。

`BeforeTool` 和 `tool.Result.Terminate` 可标记 terminate：当批次内所有调用都 terminate 时
主循环停止，不再请求模型（pi `shouldTerminateToolBatch`）。

内置工具和扩展工具都校验 required 和参数类型：mandatory null 拒绝，optional null 使用
工具默认值；整数拒绝小数、非有限值和越界转换。共享 schema 子集不执行 numeric bounds，
shell/agent 适配器在转换 duration/handle 前单独校验或 clamp。

## 输出溢出

所有工具结果在进入 provider prompt 前都经过统一的 output spool（`internal/tool/output`，
边界在 loop 的 `AfterTool` 之后）：

- 小结果直接进入 `toolResult.content`；
- 超过 preview budget 的文本完整写入当前 session 的私有临时文件，模型只收到有界
  preview、字节/行数和 `read(file_path, offset, limit)` 提示；jsonl 和 SSE 里记录的也是
  这个有界结果，完整内容只存在于 spill 文件；
- 默认 preview budget 为 16 KiB / 800 行；它只限制模型上下文，不改变 spill 文件的内容
  （`output.Config` 可覆盖）；
- 目录结构是 `<os.TempDir>/ki-tool-output/run-<pid>-<rand>/<session>/`，目录 `0700`、
  文件 `0600`，全部用主机 `filepath`；
- 图片、PDF 等非文本 content 不参与 spool，原样保留。

### details.spill

details 在保留工具原有字段的同时挂一个保留键 `spill`：

| 字段 | 含义 |
|---|---|
| `spill.path` | spill 文件路径 |
| `spill.bytes` | 文件实际存了多少 |
| `spill.totalBytes` | 工具产出多少 |
| `spill.lines` | 行数 |
| `spill.previewBytes` / `spill.previewLines` | 模型可见预览的大小 |
| `spill.truncated` | 是否被截断 |
| `spill.incomplete` | 文件只存了前缀 |

键名不是 `output`，因为 exec 的 cell 和 shell 快照都有自己的 `output` 字段，写在那里会把
工具结果覆盖掉。

### 配额与生命周期

- 单个 spill 文件最多 8 MiB、单个 session 最多 256 MiB。文件达到单文件上限时只存前缀并在
  note 里说明；session 预算用完时不再落盘，模型仍拿到有界 preview 和未保存的原因。
- 写盘失败和配额拒绝都不会把工具结果变成错误：模型始终拿到有界 preview，原因写在 note。
- session 删除或 runtime 关闭（`closeJobs`）删除该 session 的目录并释放预算；server 关闭
  删除整个 run root。这些文件不属于 session 历史，重启后不保证旧路径仍存在。
- 崩溃残留靠启动时清扫：每个 run root 写 `owner.json`（pid/host/heartbeat）；启动时扫描
  base 目录，同机且进程已死的 root 立即删除，心跳超过 24 小时的（其它主机、或无法判定
  存活）按 TTL 删除，持有者进程仍存活的 root 永不清理。

### 不参与 spool 的结果

- 已带完整输出文件的工具结果（`exec_command` / `write_stdin` 的 `output_file`）和已带分页
  游标的工具结果（`read` 的 `next_offset` / `next_byte_offset`）原样保留：它们本身就是「完整内容 + 续读方式」，
  再 spool 一次会让 read 指向自己那一页。
- 用 `read` 读 spill 文件不会二次落盘（store 只 spill 不属于自己的路径）。
- `read` 的一页（2000 行 / 50 KB）与 shell 的 incremental output budget 各自生效，不叠加
  统一 preview budget。

`exec_command` 的完整输出文件也由该 store 创建：进程日志落在同一个 session 目录里，随
session 关闭一起删除。生产 store 的终端 writer 按独立配额计费，创建失败直接报告；不退回不计费的文件绕过配额。

### 遥测

每次工具完成还会向 session 的 `telemetry.jsonl` 写一条不对外投影的 OTLP log：

- `status`：completed / rejected / failed / cancelled / timed_out；
- `kind`：参数、前置条件、外部命令非零、生命周期和内部错误；
- `fault_domain`：model_input / workspace_state / external_command / cancellation /
  environment / extension / harness。

模型仍通过原有 `IsError` 理解失败；例如测试 exit 1 仍是 error tool result，但 telemetry 记为
completed + command_nonzero，而不是 Ki harness failure。

## 全局开关

内置工具的全局启用状态保存在 `{KI_HOME}/toggles.json` 的 `tools.disabled`。`GET/PATCH
/v1/tools` 提供目录和开关；设置目录始终列出 `write`、`edit` 和 `apply_patch`，并用
`available` 标出当前模型实际使用的互斥编辑器，因此切换模型或其它工具时不会丢失隐藏工具
的全局禁用状态。开关在下一次 occupy 生效；已在运行的请求继续使用其 request header 中
固定的工具集。

这套开关只过滤 `internal/tool/builtin.Set.Build` 产生的内置工具，扩展工具仍由 extension
的启用状态和 session 生命周期控制。

关闭 `spawn_agent` 只影响后续创建，不取消已有 agent。六个 agent 工具和两个 shell 工具分别
有开关。全局开关、extension hooks、事件和遥测都使用 canonical 名；工具调用与配对的
toolResult 保留模型请求的拼写。`requestedToolName` 在执行事件中记录原始名称。extension
的原始注册名仍用于 `tool.execute` RPC，模型看到 snake_case，同时接受原始名与 PascalCase。
非法名、canonical/alias 冲突以及内置工具保留名冲突在注册时原子拒绝。

## 文件工具

### read

- 相对路径按 session cwd 解析；返回原文，不添加行号。
- 普通文本和所有 tool-output spill 文件共用 `offset` / `limit` 分页；超过 2000 行或 50 KB
  时保留头部，并提示下一次读取的 `offset`（这类结果自带游标，不再进 spool）。
- `offset` / `limit` 保持行范围分页；两者可单独使用，也可一起使用。单行超过 50 KiB 时返回 UTF-8 安全前缀及 `next_byte_offset`，不伪造跳到下一行的游标。续读使用零基原始文件字节 `byte_offset`，必须位于 UTF-8 边界，不与 `offset` 同时使用；`limit` 仍限制行数。
- details 包含精确总字节/行数（末尾换行计入空行）、当前输出大小、截断原因以及 `next_offset` 或 `next_byte_offset`。文本按 64 KiB chunk 扫描并验证整文件 UTF-8，只保留有界页/行片段；为精确 totals 仍是 O(file) 读取，不建立全文件字符串或行索引，CRLF 原样保留。
- `ToolProfile.input` 含 `image` 时才支持图片、PDF 和 `pages`；文本模式在执行阶段也会拒绝
  图片和 PDF。
- 图片进入模型前限制到 2000×2000 和 4.5 MB；需要时缩放并转成 PNG/JPEG，details 记录
  处理前后的尺寸、格式和大小。
- 文件访问通过可替换的 `ReadOperations`（Stat / Open / ReadFile）；文本流扫描每个 chunk 检查取消，图片、PDF、notebook 仍走格式专用的完整读取路径。
- `.ipynb` 按 cell 返回。不能读目录；列路径用 `glob`。

### write

- 相对路径按 session cwd 解析。
- 直接创建或完整覆盖文件，不要求先调用 `read`。
- 返回实际写入的字节数。
- 提示词要求新建或完整重写使用 `write`，修改已有文件优先使用 `edit`。

### edit

- 相对路径按 session cwd 解析。
- 精确匹配 `old_string`；默认要求唯一，`replace_all=true` 时替换全部匹配。
- `edits: [{old_string,new_string}]` 是互斥的批量模式：每项在同一份原文中必须唯一且各匹配
  区间不得重叠，最终只写一次文件。
- 某些 function-calling provider 会给未使用模式补空值或 no-op 占位符。edit 会分别验证两种
  模式：只有一种包含有效修改时执行它，忽略另一种模式的字段，并在成功文本和 details 的
  `ignored_fields` 中提醒；两种模式都包含有效修改时仍拒绝，避免猜测导致误改。
- 基于原始字节做精确替换，未触及的 BOM 和换行符保持不变。
- 模型可见 content 包含替换数量、路径，以及发生兼容降级时的简短提醒；展示 diff、统一
  patch、首个变更行和忽略字段保存在 tool-result details，不进入 provider context。

### apply_patch

- 仅 Responses 模型 capability `applyPatchToolType: "freeform"` 启用；扩展 provider 的私有
  API 由其声明并兑现 freeform 契约。内置 OpenAI Responses GPT 和 bundled `codex-oauth` GPT
  模型声明该能力。覆盖为 Completions / Anthropic 后仍使用 `write` / `edit`；启用 patch 时
  替代它们，避免两套编辑接口同时诱导模型。
- 使用 Responses custom tool 和 Lark 约束的 Codex patch grammar，不把 patch 包进 JSON。
- 支持 add、delete、update 和 move；整份 patch 的路径、源文件和上下文在第一次写入前预检，
  同一路径的多个操作按规范化主机路径拒绝。
- 更新匹配容忍行尾空白和 Unicode 标点差异，同时保留未触及内容原有的 LF、CRLF、bare CR
  或混合换行。
- 多个纯插入 chunk 保持 patch 中的声明顺序；`*** End of File` 只匹配文件尾，不回退到较早
  的同名片段。
- Responses 的 custom-tool 输入 delta 由增量 parser 转成 `patch_apply_updated` 语法预览，
  最多每 500ms 发送一次并补发最终 pending 快照；预览不执行文件操作，最终 committed details
  覆盖它。
- 结果 details 记录 `status`、`exact` 和逐文件 `changes[].unified_diff`。不可避免的中途 I/O
  失败只报告确定已提交的前缀，并用 `exact=false` 标记无法精确证明的状态。

### 文件变更并发

- server 共享按规范化主机绝对路径索引的 mutation queue；同一路径的 `write`、`edit`、
  `apply_patch` 串行，不同路径仍可并行。
- 等待路径锁及每个目录创建、读取、写入步骤前后检查取消；当前文件操作返回后才释放锁。

## 搜索工具

### grep

- 在支持的目标上使用编译进 ki 的 ripgrep 15.2.0，不依赖系统 `rg`；helper 与 fd 一起物化到
  `ki/tools/<goos>-<goarch>/`，该目录同时暴露给 shell。不支持内嵌二进制的目标默认没有搜索
  引擎；设置 `KI_USE_SYSTEM_RIPGREP=1` 后才会显式使用宿主 `rg`。
- 通过 argv 启动并逐行解析 JSON 输出，不经过 shell；支持正则、glob、文件类型、上下文和
  分页。
- 默认超时 20 秒；达到结果或原始输出上限时立即终止子进程，并保留已经解析的 partial
  results。
- 默认尊重 `.gitignore`，无需任何配置；仅 `respect_gitignore=false` 时改为 `--no-ignore`。
- 资源暂时不足时自动以 `-j 1` 重试一次。
- 无匹配的退出码 1 是正常空结果；取消、超时和命令错误分别返回对应错误，已有结果的超时
  标为截断而不是全部丢弃。
- content 模式单行最多 500 字节；结果文本本身不再做 20 KB 级别的截断（完整匹配交给
  output spool 保存，只有 16 MiB 的内存保护上限），模型看到的是统一 preview。截断保持
  UTF-8 边界，匹配上限与文本上限分别提示。
- 结果包含文件数、匹配数和 `truncated`；路径来自 JSON 字段，不解析人类可读文本。
- details 与模型看到的同一页：`mode`、`files`、`matches`、`truncated`、`offset`、`limit`，
  以及按模式二选一的 `hits[{path,line,text,truncated}]`、`counts[{path,count}]` 或
  `paths[]`。

### glob

- 使用同一内置 ripgrep 的 `--files` 和 NUL 分隔输出，不依赖 shell，特殊文件名不会破坏
  解析。
- 默认最多返回 100 个结果；结果按修改时间排序，并包含文件数、limit 和 `truncated`。结果
  文本不做 100 KB 级别的截断，超过统一 preview budget 的部分由 output spool 保存。
- 结果文本和 details 都包含规范化搜索根目录；details 另带同序的 `paths[]`。默认尊重
  `.gitignore`，仅当显式传入 `respect_gitignore=false` 时才改为 `--no-ignore`。尊重 ignore
  时先按 ignore 规则枚举，再应用 glob，避免 ripgrep 的白名单 glob 覆盖 ignore 文件。
- 默认超时 20 秒；达到上限时保留部分结果，资源暂时不足时以 `-j 1` 重试一次。

### 内置 rg 和 fd

- ki 在发布支持的三个 `GOOS/GOARCH` 目标（`linux/amd64`、`darwin/arm64`、
  `windows/amd64`）上把 rg（ripgrep 15.2.0）和 fd（10.5.0）编译进二进制，按目标只嵌入
  当前目标的那一份，安装后不需要系统 `rg`/`fd`；Linux 产物使用静态 musl 构建。
- 首次使用把当前目标可用的内嵌可执行文件物化到用户缓存 `ki/tools/<goos>-<goarch>/`（缓存
  不可写时退化为进程级临时目录），用 SHA-256 判断是否需要重写；`ToolsDir` 结果在进程内
  缓存一次。其它目标不提供内嵌搜索可执行文件。
- 在支持的目标上，`exec_command` 把该目录放到子进程 `PATH` 最前，因此 shell 里的 `rg`/`fd`
  始终是 ki 自带的版本，不受宿主环境影响；不支持的目标不会凭空提供这些可执行文件，shell
  只保留宿主 `PATH`（以及扩展目录）。`KI_USE_SYSTEM_RIPGREP=1` 只影响 `grep` / `glob`
  引擎，不影响 shell 的 `PATH`。
- Bash 额外通过 `BASH_ENV` 注入一个 shim：`bash -lc` 先读 `/etc/profile` 和用户 profile，
  而 profile 可能整体重置 `PATH`（例如 Debian 的 `/etc/profile`），所以只在子进程环境里
  prepend `PATH` 并不可靠；shim 在 startup files 之后再次把工具目录放到 `PATH` 最前，并
  通过 `KI_ORIG_BASH_ENV` 串联用户已有的 `BASH_ENV`，不覆盖用户配置。
- `fd` 默认尊重 `.gitignore` 并跳过隐藏文件；`-H` 包含隐藏路径，`-I` 关闭 ignore。这条规则
  放在 system prompt 的内置追加指令里（`prompt.DefaultAppendSystemPrompt`，见
  [system_prompt.md](system_prompt.md)）：shell 命令或管道中优先使用 `rg`/`fd` 而不是
  `grep`/`find`（PowerShell 下即 `Select-String`/递归 `Get-ChildItem`）；不支持内嵌二进制
  的目标不能假定 `rg`/`fd` 存在。它是 harness 层规则，因此 `exec_command` 的工具描述不再
  重复这段。
- 子进程 `PATH` 顺序是 **ki 内嵌 rg/fd 目录 → 扩展声明的目录 → 用户原 `PATH`**（扩展目录
  见 [extension.md](extension.md#扩展-path-目录)）。Bash 在 login profile 之后由 `BASH_ENV`
  shim 按 `KI_EXTENSION_PATH_DIRS` 再前置一次扩展目录；PowerShell 直接在子进程环境里前置。

## Shell 工具

`process.Manager` 按 session 拥有进程，生命周期独立于一次工具调用或 agent turn。每次 exec
都是新进程，默认 cwd 为 session cwd；显式 workdir 也不会改变后续调用的 cwd。

```json
{"cmd":"bun run dev","tty":true,"yield_time_ms":1000}
```

随后用返回的数值 handle 调用 `write_stdin`；两个工具没有 task_id，也没有
run_in_background、command、timeout 参数。

### 观察预算

- `login=true`、`tty=false`、`yield_time_ms=10000`、`max_output_tokens=10000` 为默认值。
- exec 观察范围 250–30000ms；非负整数 yield 在转换 duration 前 clamp，负值拒绝。输出预算
  必须为正整数，内部封顶 10000 tokens，结果公开 `requested_output_tokens` / `effective_output_tokens`。按每 token 约 4 bytes 计算共享文本预算；默认总计 40000 bytes。`include_summary=true` 时，`output_summary` 至多占一半预算，增量 `output` 使用余量；关闭摘要时全部给增量。`summary_bytes` 包含摘要分隔标记，`summary_omitted_bytes` 只计未保留的源字节。
- 达到观察预算返回 handle，命令继续运行，没有前台提升或 sleep 特例。短观察期限可能在
  shell/子进程启动、尚无输出时返回 running（尤其 Windows Git Bash/ConPTY）；需要终态的
  调用方应使用 `write_stdin` 在合理期限内继续观察，而不能把首次 running 当作命令失败或
  pager 卡住。
- 空 chars 的观察默认 5000ms、范围 5000–300000ms；非空 chars 默认 250ms、范围
  250–30000ms。
- 取消工具观察只结束等待并返回有效 session 快照，不杀进程；启动前/排队中的取消不写
  stdin、不消费输出。已经进行的观察返回本次已读增量并正常推进游标，预算外输出留待续读。
- handle 是最多 53 位的正整数，不能跨 session 使用。

### Shell 选择与终端

- Windows 默认优先 PowerShell（pwsh，其次 Windows PowerShell），再回退 Git Bash；Unix 默认
  使用发现的 Bash。可显式选 bash/sh/zsh/pwsh/powershell 或主机绝对路径。找不到所选 shell
  时执行报错，server 可正常启动。
- Unix 使用 PTY，Windows 使用 ConPTY。只有 `tty=true` 支持普通 stdin 输入；pipe 模式拒绝
  普通输入，但 `chars="\u0003"` 仍能请求进程组中断。Ctrl-C 不保证进程退出，强制停止由
  session abort 的 process scope 执行。

### Shell snapshot

- Linux/macOS 的 Bash login 调用可缓存 profile 初始化后的变量（含非 export 变量）、函数、alias、选项、umask 与 cwd。Windows/PowerShell、`login=false` 和不支持的 shell 保持原启动方式。
- 缓存按 manager 隔离；shell 身份、cwd、继承环境、常见系统/用户 profile 及 BASH_ENV 的 stat/内容变化失效，reload 调用 `InvalidateShellSnapshots`。30s TTL 限制未跟踪的任意 source 依赖/外部状态的陈旧时间，不承诺实时追踪任意依赖。
- capture 最多 2s、文件最多 1 MiB；索引最多 16 个、manager 生命周期最多保留 64 个 capture 文件，超过后回退原启动。失败、有 profile 输出或不支持环境均回退，不吞 profile 输出；失败捕获可能已执行 profile 副作用，回退可能再次初始化。快照不重放后台任务或 trap，不是持久交互 shell。

### 输出

- stdout/stderr 混排；原始字节写入完整输出文件。实时增量去除 ANSI/控制字符，UTF-8 边界
  安全。内存采用最多 1 MiB 的环形缓冲，不在每次溢出时复制整个窗口；模型每次收到尚未读出
  的增量并受输出预算限制，预算超过上限会被截断；预算内 raw 日志可通过 `read` 分页。可选头尾摘要独立放在 `output_summary`，不消费或跳过增量游标：游标只按 `output` 实际返回字节推进。
- 实时快照保留 16 KiB 尾部，delta 至多 8 KiB；进度观察节流 100ms，单进程最多 10000 个中间
  更新。manager listener 异步交付，只保留最新 pending progress；慢持久化不能阻塞原始输出
  drain。中间预览可合并或省略，不保证 delta 拼接能还原日志；初始/终态保留，finish 发布
  剩余 delta。最终输出以日志及 write_stdin 游标为准。

### 进程容量与回收

- 每个 session 最多 64 个 live 进程；已退出且输出已读完的 handle 可回收，记录最多保留
  256 个。已消费完的退出进程立即释放 retention window，仍保留不可变的 16 KiB 预览和游标，
  避免每 manager 额外钉住 256 MiB 的无用已读缓冲。达到 live 上限直接拒绝创建，不淘汰仍在
  运行的进程。
- shell raw 日志由计费 writer 创建，独立默认上限为 64 MiB/file、512 MiB/session、2 GiB/service，分别由 `output.Config.MaxProcessFileBytes` / `MaxProcessSessionBytes` / `MaxProcessServiceBytes` 覆盖，不套普通结果的 8 MiB。writer 预留配额后在全局锁外写盘；session close 关闭活 writer、释放账额并拒绝后续写入。
- 配额耗尽或 I/O 失败后继续 drain 外部命令，结果以 `error`、`output_file_bytes`、`output_file_incomplete` 明示 raw 仅能恢复已写前缀，不能声称文件仍完整。内存尾部/增量可继续显示后续输出。生产计费 store 创建失败直接报告；无 store 的独立 manager 和旧测试 spool 才使用临时文件回退。
- 进程退出只释放 OS 资源；显式停止终止进程树，已退出对象不再发送 raw process-group
  signal。session 删除/server shutdown 才关闭 manager 并清理日志；Close 完成包含最后
  listener 回调，不能在自己的 listener 中同步关闭同一 manager。
- root 退出后的输出 drain 有 200ms 上限：Unix 强制 PTY 截断明确报告，Windows ConPTY 正常
  EOF 依赖终端关闭的路径不误报为截断。进程树控制不是 sandbox，不承诺回收 Unix 上显式脱离
  原 shell 生命周期的 daemon；不通过对已退出 PID/PGID 再发信号猜测归属。

### 环境

- 子进程继承 Ki 的代理环境和扩展 PATH；Bash profile 之后仍通过 BASH_ENV shim 恢复内嵌
  rg/fd 和扩展路径。PowerShell 的 `login=false` 使用 NoProfile，pipe 模式使用
  NonInteractive；错误及原生命令非零退出继续传播。
- shell 子进程设置空 PAGER/GIT_PAGER/GH_PAGER、`NO_COLOR=1`、`TERM=dumb`、空 `COLORTERM`，
  避免继承的 pager 将 `tty=true` 命令停在不可见交互界面；不依赖 Windows 的外部 cat，也不
  修改 host 环境、locale 或 extension sidecar 环境。命令仍可显式启动交互程序或覆盖这些
  环境变量。

### 失败语义

非零退出码返回 error tool result，同时遥测记录 completed / command_nonzero /
external_command。工具参数、观察取消与进程交互故障分别记录，避免把外部命令错误算作
harness 故障。

增量输出回归使用真实子进程的 readiness 和显式 gate：首段就绪后才消费，释放 gate 后验证
第二段、游标和完整日志；不假设 Windows shell 在 250ms 内输出首字节，也不用固定 sleep 分隔
两段。

## Agent 协作

`agent.Controller` 保存逻辑身份、结构父子关系、运行代次和 pending follow-up，shell
manager 单独拥有进程。工具层只依赖 server 实现的 `agent.Runtime`。

### 身份与命名

- 根为 `/root`。child 的 task_name 使用 1–64 个小写 ASCII 字母、数字或下划线，不能为
  root；同一 root 的完整路径不可重复。child 可用相对自身的路径，或 `/root/...` 绝对逻辑
  路径；`..`、`.`、跨 root 以及旧 parent/main 保留名不解析。
- spawn 立即返回，child 始终 detached。身份、初始 generation 和持久输入在同一次接纳中
  发布，不暴露可被 stop/follow-up 抢占的半初始化 task。
- 新的 API 不保留 Agent / TaskOutput / TaskStop / Bash / PowerShell 的执行入口。
  SendMessage 仅为 send_message 的 PascalCase 别名，参数为 target/message。

### fork_turns

`fork_turns` 默认 all，可为 none 或正整数（字符串），首尾空白统一忽略；复制完整的已完成
user turn，排除触发当前轮的 user 输入及之后内容。QueueOnly mailbox 消息不算新的 user
turn；继承工具调用/结果配对与附件路径。child 默认使用 parent 的 provider/model/cwd；可选 `model` 指定 child 模型，指定新模型而未指定 `effort` 时使用该模型默认 thinking effort，身份放在
首条 user 信封里。默认 system/tools 前缀相同；全局禁用项同样生效，extension activeTools
保持 session-scoped 选择，不是继承的安全权限。

```json
{"task_name":"review","message":"Review the changes and report concrete issues.","fork_turns":"all"}
```

### 容量

没有固定 depth 限制。Settings → Agents 的 `maxThreads` 默认 4，表示 root + child 的总线程上限；
root 自己占 1 个，因此默认最多 3 个活跃 child turn。限制按 root 会话生效，正在 `wait_agent`
的 child 仍占名额；完成/被中断的身份不占活跃执行名额且持续可寻址。修改上限只影响后续
启动的 child turn，不中断已经运行的 turn。

### 消息投递

- `send_message` 先写 context queue，再通知 live Inbox；idle 时等待下次显式任务。
- `followup_task` 使用稳定 `clientRequestId` 持久排队：idle 且有名额时开新代次；busy 或
  容量满时排队，并在代次结束/名额释放时调度。它不取消或重启正在执行的轮次。代次结束后仍
  有 pending 且容量不足时保持 Pending/waiting_resource，不能以 Completed 隐藏已接受工作；
  仅取消 pending 不抑制上一代完成结果。
- `wait_agent` 默认 30s，范围 10s–1h；显式越界值拒绝，而不是 Codex 的低值 clamp。收到
  mailbox 或用户 steer 时醒来，观察超时/取消不影响其它 agent。模型随后正常 drain
  mailbox；list/wait 都不认领完成通知。
- 每代次完成结果自动发给结构 parent，按 taskId/generation 在实际落入 transcript 时做
  ledger 去重。live parent 在下一轮 model request 前消费；idle parent 接受上下文但不自动
  启动模型轮次。跨 queue、transcript、agent.json 不存在事务，不承诺崩溃时 exactly-once。

### 中断与停止

`interrupt_agent` 拒绝 root/self 及包含调用方的祖先子树，终止目标和所有深度后代的 turn，
并丢弃它们的 pending tasks；即使目标已空闲，仍会清理活跃后代。所有身份保留，可由
`followup_task` 再次启动；独立 shell 进程继续运行。祖先拒绝避免同步清理等待调用方自己。

普通 session abort 默认 `scope=turn`，取消自身并级联中断所有后代 agent、清除其 pending
任务；root 已空闲时也执行级联。extension abort 先取消自身，再清理后代，避免等待自身
工具调用。级联临时阻止该子树的新 spawn/follow-up，先向所有目标发出取消，再等待全部
turn 和已预约的 spawn 清理完成，最后释放屏障。仅清理 pending 的 agent 保留上一代完成
结果的通知权；其它 sibling/root 的任务不受影响。

`scope=process` 配合数值 session_id 强制停止进程；`scope=tree` 另外停止子树拥有的进程。
递归 tree stop 还临时阻止 session occupy/dispatch，再中断已有 agent，最后停止它们拥有
的进程；清理结束后释放临时屏障，稳定身份仍可 follow-up，保留的显式 session 队列工作
才可重新 dispatch。

### 持久化与恢复

`agent.json` version 3 经 `internal/state` 迁移，保留稳定路径、pending 输入及 delivery
ledger。server 恢复先注册全部身份，再恢复 pending tasks；重启前 running 改为 interrupted，
不恢复 OS 进程 handle。

每代次 settlement 释放自身 context。host `Controller.CloseContext` / `Server.Shutdown` 使用
共享清理屏障；调用方 deadline 只结束观察并返回 context 错误，已开始的 owned cleanup 继续。
server 先 fence/cancel 并开始关闭已注册 manager，及时停止 OS 进程；最后的共享文件/输出
store/extension 清理等待已接纳 spawn、所有代次 root/child writer、release、完成回调和
manager final publication。不能逐个等待 2s 后假定 writer 已结束。

删除先 fence agent subtree 和 session occupy/dispatch，再等已接纳 spawn 和 writers 收尾、
遍历 tree 并清理 manager/目录。删除 tombstone 防止队列或 late root setup 重新创建 owner；
flat fork 和其它 root 不受 tree cascade 影响。

### 迁移

toggles v1 的旧禁用项迁移到相应的新能力并取保守并集，避免升级后意外重新启用工具；当前
设置保存 canonical 名称。

## 运行进度与统计

agent snapshot 的 revision 单调递增；phase 区分 starting / executing / waiting_message /
waiting_resource / settled / interrupted。`current_tools` 最多 8 项，`waiting_for` 指向
mailbox；`pending_tasks` 与 `queue_wait_ms` 表示显式任务排队。`last_activity_at` 由执行/
输入事件更新。

`run_stats` 保存当前代次的 tools、requests（model rounds）、tool_failures、
input/output/cache-read/cache-write/total/context tokens 与 `mailbox_wait_ms`；
`agent_lifetime_stats` 累加已观察代次，`lifetime_stats_complete` 标记历史是否完整。空正文
不会复用旧结果。所有统计在事件边界归约，不反复扫描继承 transcript，不保存推理正文。

进程带 originating run_id/tool_call_id/agent_id/generation 与 revision，UI 即使 agent 已
完成仍能显示其 live 进程。`session inspect --json` 的 `analysis.runtime` 与
`trace --json` 的 runtime/sideband 显示 last-known 投影；`analysis.timing` 按工具/消息等待
区间并集统计，未知时间保留 `unknownMs`。工具 telemetry 带 generation、原始请求名、通信
收据及 wait `wake_reason`，避免并行 duration 相加。

Unix PTY 使用独立 close-on-exec pollable descriptor，关闭终端可释放堵塞的 stdin 写入；
输出 spool 写入或收尾失败明确报告 harness I/O 错误。

## Code Mode

Code Mode 固定为 `mixed`，在普通工具之外发布 `exec/wait`，没有 TOML 或 Settings 模式设置。
工具开关仍作用于底层能力，`exec/wait` 自身也出现在全局内置目录并可关闭。扩展工具只能来自
本次 occupy 已 Prepare、过滤后的集合。

`GET /v1/tools` 返回 `{items, mcp}`，`PATCH` 只接受可选的 `disabled` / `mcpDisabled`，省略
的字段不会改变；未知字段（包括已移除的 `codeMode`）返回 400。各设置和自动扩展禁用共用
写入锁，避免不同字段的并发保存互相覆盖。

MCP 使用官方 Go SDK，配置和生命周期见 [mcp.md](mcp.md)。`search_tool` 可用时，允许的 MCP
工具保留在执行 registry，但完整 schema 延迟披露；关闭/排除 search_tool 则直接发布，不保留
无搜索入口的 Deferred。JS `tools`/`ALL_TOOLS` 已有全部允许的 MCP 能力，搜索不授予权限。

### exec 与 wait

- `exec`：只有同时使用 Responses 且明确声明 `execToolType: "freeform"` 的模型使用 raw-JS
  custom/freeform；扩展私有 API 可显式声明并自行兑现同一契约。其它模型（包括 DeepSeek
  Responses）及 Completions / Anthropic 均使用 `{code: "…"}` function tool。该能力与
  `applyPatchToolType` 独立，支持 custom `apply_patch` 不代表支持 custom `exec`。两者都
  接受首行 `// @exec: {"yield_time_ms":10000,"max_output_tokens":10000}`，且只接受这两个
  非负 JS-safe integer 字段。
- `wait`：`{cell_id, yield_time_ms?, max_tokens?, terminate?}`。默认等待和输出预算均为
  10000（毫秒 / 估计 tokens），显式零有效。等待最多 60 秒，是观察期限，不是脚本存活期限。

### 运行时

- 每次执行使用新 goja VM；`tools.xxx` 返回 Promise，参数对象用于 function，字符串用于
  freeform。`text`/`image` 显式输出，表达式值不自动返回。`ALL_TOOLS` 提供允许的工具
  元数据。
- `store`/`load` 是 session 内存中的 JSON 快照/提交机制；完成（包括 JS 错误）才提交写入，
  yield 不提交，终止丢弃。状态不恢复到新 server、fork 或关闭后的 worker。
- `notify` 发布有归属的即时进度，文本在下次 `exec`/`wait` 观察返回；不注入 Codex 的独立
  `custom_tool_call_output`。`yield_control` 可提前返回当前输出，后续 `wait` 只返回新增
  内容。

### 调度与审计

普通工具与嵌套工具共用 `loop.ToolDispatcher` 的 prepare/execute。JS 取得 **AfterTool 后、
模型 preview 截断前**的中间结果（包括 `content`/`details`/`isError`），受独立 RPC 预算
限制；jsonl/SSE 使用另行有界的审计副本。嵌套调用的 `AfterTool` 失败时 fail closed，不把
未经策略处理的原始结果返回 JS。每个嵌套调用的身份由父进程分配，记录
`parentCallId`/`cellId`，不得调用 `exec`/`wait` 自身。审计/RPC 错误必须使 cell 失败，不能
被 JS catch 后继续执行。

只有外层 provider 发出的 `exec`/`wait` 调用生成 transcript `toolResult`，嵌套调用通过
结构化执行事件持久化。occupy 结束先取消并 join 全部 cell/callback，再关闭 hooks、
telemetry 和 session；已通过 shell 工具启动的 session-owned 进程仍遵循既有生命周期，不因
观察取消被误杀。

Code Mode 的调用取消先请求 worker 停止 cell，再取消父进程 callback；callback binding 不
直接继承原始调用的取消/deadline，避免取消错误抢先使仍活跃的 cell 失败并提交待丢弃的 store
写入。

进程关系、限制和相对 Codex 的差异见 [code-mode-design.md](code-mode-design.md)。
