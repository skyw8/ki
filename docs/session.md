# 会话格式

一个 session 一个目录，append-only jsonl 树。包入口见 `internal/session/doc.go`。

## 路径

`{sessions.root}/<encoded-cwd>/<timestamp>_<uuidv7>/`

- `encoded-cwd`：绝对路径去掉盘符，`/` `\` `:` 换成 `-`，两边加 `--`。
- `telemetry.jsonl` 是 session-local OTLP/JSON harness 诊断，不属于消息树，不通过 API/SSE 投影，也不随 fork 复制。
- 目录内：`events.jsonl` + `config.json`；忙时排队的 user 在 `queue.json`（最多 100 条，不进消息树直到出队开跑；队列项带 `lane`：`human` 是人提交的（`POST prompt` + `delivery=queue`），`system` 是服务端生成的（agent 完成通知、agent 发给 caller 的消息），带 `origin` 标记来源；完成通知另带 `completion:{taskId,generation}`，出队可跳过已消费的 generation；最终由与 live Inbox 相同的持久化认领决策阻止重复原始交付，不能只按稳定 task ID 去重）。**出队规则是 human 先于 system**，同一 lane 内保持 FIFO：完成通知是从子代理的 goroutine 入队并在 session 空闲时立刻 dispatch 的，若严格 FIFO，先到的通知会抢先成一轮、让人白等一轮；这条规则与 Claude Code 的队列一致（那边的 task-notification 取最低优先级，"user input is never starved by system messages"）。`EnqueueFront` 会把重试项插回**自己那条 lane**的头部，所以重试也不会插到人的前面；扩展 FIFO 在 `ext-queue.json`；不触发运行但要进入后续 prompt 的正常 user message 暂存于 `context-queue.json`（最多 100 条，按序提交）。`config.json` / `queue.json` / `ext-queue.json` / `context-queue.json` 经同目录临时文件 + rename 原子落盘，避免并发读到截断 JSON。写侧和读侧还必须按目录串行：`config.json` / `events.jsonl` 走 `fileGate`（`writeConfig`、`appendRaw` 持写锁，`Open`、`liteInfo`、`ReadConfig` 持读锁），队列文件走 `queueGate`。**为什么读侧也要上锁**：Windows 在 rename 替换期间会拒绝并发 open（`ERROR_SHARING_VIOLATION`，"being used by another process"），一次状态轮询/`POST prompt` 撞上就会报成 404 session not found；POSIX 的 rename 不动已打开的描述符，所以这个竞态只在 Windows 上出现。
- tool output spill 文件和 shell 任务日志不进入 jsonl；它们属于当前 serve 的 session runtime（`<os.TempDir>/ki-tool-output/run-<pid>-<rand>/<session>/`），模型只通过 `toolResult` preview 和 `Read` 引用访问。session 关闭删除该 session 目录，server 关闭删除整个 run root，重启后不保证旧路径仍存在；崩溃残留的 root 在下次启动时按 owner pid/心跳清扫（见 [tools.md](tools.md) 的「输出溢出」）。

## jsonl

第一行 header：`type=session`，含 `id` / `cwd` / `parentSession` / `forkMode`。`parentSession` 是直接来源 session 的 id；`forkMode` 为 `flat` 或 `tree`，普通 session 和普通 fork 默认为 `flat`。
之后每行 `{type,id,parentId,timestamp,…}`：`message`、`compaction`、`model_change`、`request_header`、`context_usage`、`patch_apply_updated`、`compaction_start`/`compaction_end`、sideband `extension_error`。entry id 为无连字符的 32 位 hex UUIDv7。

`request_header` 固定该轮的 `system`、`tools[]`（含 function/custom type 和 custom grammar format）、provider/model、thinking effort、catalog version 和价格快照。消息里的工具调用保存 `toolType` 和 freeform `input`，使 resume 能保持 `custom_tool_call` / `custom_tool_call_output` 配对。`context_usage` 保存 `usedTokens`、有效 `contextWindow` 与 `estimated`；`patch_apply_updated` 保存模型生成 patch 时的结构化非执行预览。两者沿 SSE 到 WebUI，且不进入 provider context。

toolResult message 可带结构化 `details`，以及工具完成时间 `timestamp`（Unix 毫秒）和从调用开始到完成的 `durationMs`。它随 jsonl 落盘并通过现有 session API/SSE 提供给 WebUI；历史 WebUI 可由这两个字段恢复工具开始时间。provider 回放只使用模型可见的 `content`，不会把 diff、patch、计时或任务诊断元数据送回模型。

## 细节

- Transcript 缓存的文件 identity 使用短命 open handle 的 `Stat` 在读取时捕获，不缓存句柄。Windows 的 `os.Stat(path)` 会延迟到 `os.SameFile` 才按路径读取 file ID：若两次读取之间原子替换，旧 `FileInfo` 也会绑定新文件，导致 size/mtime 未变时误命中旧正文。正文和元数据缓存均必须保存读取当时的 identity，同时及时关闭句柄，避免阻止 Windows 删除或替换会话。
- 新行永远 append 在文件末尾；`config.activeLeafId` 持久化当前分支。append 使用 `O_APPEND` 的短命句柄（写完即关），`Session` 不长期持有 `events.jsonl`：POSIX 允许删除仍被打开的文件，Windows 不允许，长期句柄会让「删除运行中的 session」和 `t.TempDir` 清理在 Windows 上失败；`session.Remove` 对仍有一瞬写入的目录做短暂重试。旧数据没有该字段时，重载以最后一条非 header 为 leaf。
- `SetLeaf` 只切换 active leaf 并写 config，旧行不删。edit/regenerate 从指定 parent append sibling branch。
- `MessagesToLeaf` 是跨 provider 的 portable projection：沿 parent 走到根，只选择最新 local compaction，先注入 summary，再取 `retainedTail`（新条目，压缩时最近消息原文落盘）；旧 jsonl 无 `retainedTail` 时回退 `firstKeptEntryId` 截断。remote compaction 不参与 portable projection，因此切换模型仍可从 append-only 原始消息重建上下文。`LastCompactionAt` 返回最近 compaction 时间戳（stale-usage 防护用）。
- Provider remote compaction entry 的 `responses` 保存严格绑定 `provider` / `api` / `baseUrl` / `model` / credential fingerprint / compaction protocol 的 canonical output item 数组。fingerprint 是 credential 的非秘密 SHA-256 identity，不保存 key/token；换 key、project credential 或 extension opaque credential 后不会复用旧密文。`ContextToLeaf(binding)` 仅在最新 compaction 的 binding 完全匹配时返回 opaque prefix + checkpoint 后 message suffix；否则回到标记过的 portable projection，也不会越过一个更新的不兼容 remote checkpoint 去复用旧密文；context meter 和 local/remote compact planning 对该展开历史使用完整序列化估算，不误用只覆盖 compact prefix 的旧 usage。空闲 session 切换模型后立即追加按新 binding 重算的 `context_usage`。item 使用 raw JSON 保留未知字段和顺序，单行上限 64 MiB；server-side assistant 与 checkpoint 在同一 file gate 内提交，短写/I/O/config 失败回滚 JSONL 尾部。session slim/index/compact/trace、精确 entry API 和 CLI full view 只显示固定的 remote-compaction 标签，绝不返回 encrypted payload。
- Public entry/index projections expose the derived `remoteContext: true` marker for remote checkpoints, including slim/compact pages and exact entry hydration. The marker survives provider-payload redaction; encrypted `responses` contents are never exposed. It identifies the checkpoint kind, not current provider-binding compatibility, and is not written to jsonl.
- Public entry/index projections carry view-only `contextEstimate: {system?, tools?, message?, summary?}` computed from complete stored content before metadata previews, truncation or unchanged-header omission. Each present number is an approximate UTF-8 byte count divided by four, rounded up per block/schema; `0` means known empty and an absent field is unknown/not applicable. Message estimates include text, thinking and tool-call name/input/pretty JSON arguments; tool estimates sum compact schema JSON and local summaries include their portable summary prefix. Images, opaque signatures and remote checkpoints are not priced as plaintext. These estimates preserve historical classification sizes without hydrating every body, remain distinct from provider usage/final encoded input, and are never persisted to jsonl or used for replay. The metadata scan keeps only estimates and previews; repeated schemas use a bounded digest/count cache, not retained full schemas.
- fork：`ForkAt` 新建 session 目录，只写 root → target 路径；新 header 使用新 id，`parentSession` 保存源 session id，`forkMode` 保存处理策略。`flat` child 与 parent 独立；`tree` child 由 server 在删除 parent 时递归清理。子 session 复制源的 `provider` / `model` / `thinkingEffort`，不回落到 registry 默认。
- server 删除先建立 session tombstone 和 agent subtree admission fence，等待已接纳 spawn commit/rollback 后再收集 tree；取消并等待 writers/release/完成回调后才关闭 manager 和删除目录。队列 dispatch 不能重占已删除会话；flat child 和其它 root 不被此 fence 级联。server shutdown 以共享完成屏障保证最后的进度回调结束后才清理输出文件。
- `spawn_agent` 创建 tree child 并继承 provider/model/cwd。fork_turns=all（默认）复制触发当前轮之前的已完成历史，none 创建干净 child，正整数复制 N 个完整 user turn；QueueOnly mailbox 消息不切分 user turn，工具调用/结果和附件仍配对。身份信封放在 child 首条 user 消息中。逻辑身份、代次、pending 和 delivery ledger 保存于 agent.json v3；普通消息只入 context queue，显式 follow-up 才启动新轮次。
- API 对外统一把 header 的 `parentSession` 映射为 `parentSessionId`，并同时返回 `forkMode`。`GET /v1/sessions`、`GET /v1/sessions/{id}` 和 fork response 使用同一组字段；`fork` body 可传 `{"entryId":"...","forkMode":"tree"}`，省略 mode 按 `flat` 处理。
- 列表行只需要 `config.json` + jsonl header + title fallback 的首条 user message：`List` 用浅扫描，遇到首条带正文的 user message 或 `config.title` 即停，不解析整份 transcript（旧实现为渲染一行 sidebar 而解码最大 12 MB 的 jsonl）。扫描遇到无法解码的普通行会跳过，不因一条坏行把整个 session 从列表里隐藏。server 用 `session.ListCache` 按 `events.jsonl` / `config.json` 的 size+mtime 复用行；文件仍是唯一事实来源，外部进程写入的变更下次调用即生效。`GET /v1/sessions` 的响应按渲染结果打 `ETag`，命中 `If-None-Match` 时返回 304。
- `config.json`：该 session 的 `provider` / `model` / `thinkingEffort` / `activeLeafId`，可选 `title` / `pinned` / `pinnedAt` / `metadata`。内置 tools、Skills、extensions 启用在 `{KI_HOME}/toggles.json`，不在 session 里。
- `context-queue.json`：`session.appendMessage` 的持久化暂存队列。消息提交后会变成普通 `message` entry，进入 `MessagesToLeaf()` 和后续 provider prompt；队列项带单调递增序号及可选 `idempotencyKey`。当前 prompt 入队时捕获边界序号，只提交边界之前的 context，之后到达的消息留给下一轮。扩展 prompt 的 `idempotencyKey` 另随 `ext-queue.json` 和首条 user entry 持久化。
- user message 可保存结构化 `text`、`workspace_file`、`file` 和带宿主绝对路径的 `image` content。站内浏览器选择的是 workspace 引用；粘贴/拖入文件按 SHA-256 保存到本 session 的 `attachments/`。jsonl 只存引用；server 在 provider 边界读取并编码图片，普通文件变成可供 `Read` 使用的路径说明。fork 复制附件并把新 jsonl 中的路径改到新 session 目录。
- 按 id 定位目录：serve 进程内维护 `session.Index`（id→dir 内存 map，见 `internal/session/index.go`），启动时由 `List` 的同一次 walk 顺路建好，零额外读盘。create/fork 后 `Add`、delete 后 `Remove`。命中即 O(1)；miss（别的进程建的会话、或目录被外部删除）回退到 `Find` 扫描并自愈，文件系统始终是唯一事实来源。
- `GET /v1/sessions/{id}` 默认返回 WebUI 的**对话尾部**：slim `entries`（当前 leaf 最新的 `limit` 条，默认 100；未变化的 `request_header` 省略 `system`/`tools` 并标 `promptUnchanged`，超过 24KiB 的正文截断并标 `truncated`）、`hasMore` / `oldestId`。窗口按条数上限和 **512KiB 序列化 entries 字节预算**切出，超预算时缩小连续页；单条过大时返回保留 id/统计的 `truncated` 摘要，至少推进一条。折叠工具文本预览最多 1KiB，全文通过现有 entry/entries 读取。窗口可能落在某个 turn 中间：响应额外带上开启那个 turn 的 user message（`BuildTail`/`BuildBefore` 的 `withTurnOpeningUser`），否则前端的 compact 视图会把首个 turn 画成「一条折叠行 + 最新几条回复」，上面没有提问（实测 125 个窗口里 43 个整窗没有 user entry）。`hasMore` 表示窗口之外是否还有更早的叶子历史：tail-first 读取只是文件的字节窗口，窗口起点未必是分支根，所以 `BuildTail` 在调用方声明窗口未到达根（`complete=false`）时即使 `tailStart==0` 也保留 `hasMore`，否则刚好装满 `limit` 条的分支会永远翻不到前面。`oldestId` 仍是窗口自己的边界，翻页照旧逐条向前，所以被跳过的那段中间内容仍然可达。不返回 `messages`。整棵 jsonl 树的 `index`（无正文行，供分支导航和轨迹表）需要 `?fields=index` 才返回：它使用独立的轻量元数据/文件偏移索引，冷读扫描整个 JSONL，但不保留历史正文、工具参数或 schema；追加只扫描新增完整行。放在默认响应里仍会增加扫描与几 MB JSON，而最新的几条消息并不需要它。比一次尾部读取还短的 session 本来就被整份读出，仍顺带返回 `index`，WebUI 因此不必发第二次请求。`?fields=runtime` 只带 catalog / queue / `runtime.ready`（不读 transcript）；`?fields=index` 只返回 `{id,index}`，支持内容 ETag / `private,no-cache`，不重复 tail/runtime；`?fields=index,runtime` 返回 tail + index + runtime 的完整组合。`?entry=` / `?entries=` 取未裁剪正文；`?before=` + `?limit=` 从 `oldestId` 沿 leaf 向前翻页。正文读取按文件 identity + size + mtime 走加权 LRU：全局 64MiB、单会话 8MiB、最多 256 会话；超大读取照常返回但不准入，淘汰只解除缓存所有权，不修改在途快照。`TailEntries`/`LeafTail` 缩回最近窗口，`AllEntries` 服务模型执行/诊断所需的完整历史，`OpenFrom` 直接用已读 entries 构造 Session。相同 system/tools 在各自快照或 Session 生命周期内共享存储，不设永久全局 intern 表。独立 `ReadTranscript` 元数据索引受全局 16MiB / 256 会话限制，精确正文、较早分页、turn 展开和 compact 先在轻量元数据中选择条目，再按文件偏移只解码所选正文，不提升完整正文缓存。原子替换会重建缓存，已失效偏移返回错误而非错误正文。预算是保守对象图估算，限制缓存拥有的存储；活动运行、在途读取和 Go 分配器保留页不属于这些预算。`GET` 带只读 `availableSkills` / `availableExtensions`（含该扩展加载的 `skills` / `tools` / `commands` / `promptAppend` / `providers`）、`commands[]`、`queued[]`、`extQueued[]`、`extensionUi[]` 和 `runtime.ready`。扩展 sidecar 在 server 监听后统一启动；打开该 session（create / GET by id / fork）只后台 Prepare 当前 session view；`GET /v1/sessions` 不启动 runtime。GET 不 await 握手。`runtime.ready` 在该次 Prepare 结束（失败也算）后为 true，并发 sideband `runtime_ready`。`PATCH` 的 `queued` 是保留的 id 列表（删除未列出的条目）；run 或独立压缩占用 session 时，`model` / `thinkingEffort` / `leafId` 变更返回 409，防止 model change 或分支切换推进压缩计划绑定的 leaf。`POST prompt` 的 `queueId` 按 id 从 `queue.json` 取出并 `delivery=steer` 插入当前 run。`enabled` 来自全局 `toggles.json`。忙碌默认发送策略是 `toggles.json` 的 `message.busy`（`GET/PATCH /v1/message`）。Prompt 的 Prepare 已预热则 no-op；失败项发事件，但不阻断其他 server 或内置工具。

- The lightweight `index` preserves tool-result `toolCallId`, `name`, `durationMs` and `isError` (omitted false means no failure). Code Mode audit start/update/end rows additionally preserve optional `parentCallId`, `cellId` and `requestedToolName`, reusing the same call/name/outcome fields. These are bounded scalar facts only: the metadata cache and index never retain nested arguments, progress or result bodies. Consumers follow the selected leaf's parent chain and deduplicate by tool call ID, including nested executions, rather than counting lifecycle frames or only body-loaded chat nodes. Unloaded audit projections establish invocation identity and failure status, not complete content; exact bodies remain available through the existing entry APIs.

会话 cwd 来自工作区 path（或临时 `{KI_HOME}/workspace/tmp+<时间戳>`），不是进程 Getwd。标题优先用 `config.title`。`Remove` 删除整个会话目录。工作区见 [workspace.md](workspace.md)。


### Compact turn projection

`GET /v1/sessions/{id}?view=compact&keep=N` 是面向 compact 阅读的稀疏投影，`keep` 为 0–20，默认 1。首次最多返回最近 4 个完整人工输入 turn；同一请求加 `before=<oldestId>` 每次返回一个更早的完整 turn。响应带 `entries`、`compactTurns`、`hasMore`、`oldestId`。`oldestId` 为最早返回 turn 的输入 id，不位于折叠区中途。每轮传人工 input、最后 N 个正常 assistant/tool 回复需要的正文、其后 runtime 通知，以及始终独立显示的压缩/中止状态；隐藏区用整轮计数、预览和统计表示。runtime user-role 消息（origin 非空且不以 `extension:` 开头，例如 subagent 指令、完成通知和 context-only 邮件）可以折叠，但不消耗 `keep`：首个保留或 live 的真实回复作为单一时间顺序边界，边界之前的普通回复和 runtime 通知一起折叠并计入 `hiddenCount`，之后保持原顺序显示。因此 `keep=1` 保留最后真实回复，不会被尾随通知挤掉；`keep=0` 且无 live 回复时所有通知与普通回复折叠。`keep` 足够保留所有真实回复时，首个真实回复之前的 runtime 指令仍折叠；完全没有真实回复的通知-only turn 也全部折叠。无 origin 或 `extension:` origin 的人工输入仍是 turn 边界并始终显示；runtime-only 会话仍保留首个可渲染节点的正文作为稳定 turn 锚点，即使该节点隐藏。跨大 turn 不会循环下载已折叠的工具往返。初次读取会用增量 transcript 缓存确定完整分支与 turn 边界，不传整树 index。

`compactTurns[]` 保存 `id`、原始 `parentId` / `tailId`、有序 `entryIds`、`visibleNodeIds`、少量共享 entry 的 `omittedNodeIds`、`hiddenCount`、`firstHiddenId`、`preview` 与整轮 `stats`。`entryCount` 是快照覆盖的整轮 entry 数（含 metadata），用于拒绝迟到的旧投影；`assistantAt` 是最近 assistant 的完成时间；`toolStates` 仅含最近 assistant 工具批次的 `{id,finished,isError?}`，即使 keep=0 也能将正在执行或刚结束的调用与 SSE 精确去重，而不是传输全轮所有隐藏 ID。`stats.startedAt` 是开头 user 的服务端时间，`stats.elapsedMs` 覆盖 assistant、工具结果和 compaction 的最大完成时间；`cumulativeElapsedMs` 累计至该轮，未加载 index 时也包含窗口之前的耗时。`stepCount` 是截至该轮的累计步骤数，`lastStep` 保存最近步骤的用量与耗时；即使 keep=0，composer 的轮数、步骤数和最新用量也不因折叠减少。持久化 parent 边不改写；前端只在读取稀疏链时跨过省略段，收到整树 index 或展开正文后优先采用原始链。可见工具所需 call/result 成对保留，同一 call entry 内隐藏的 assistant 正文不传；完整内容仍通过原正文接口可取。

无效、已切离当前分支的 `before`（含展开局部游标）返回 **409**，不能返回空成功页并假装已到根。客户端保留缓存并重新确认边界。

显式展开使用同一 GET 的 `turn=<turnId>`（可配 `before` / `limit`），返回限定在该轮内的 slim entry 页；其 `hasMore` / `oldestId` 是展开的局部游标，不能覆盖会话历史边界。`view=compact&turn=<entryId>&keep=N` 可重新投影某个已知 turn，也可把 detailed 页边界所在的半轮转换成完整 compact 轮。页预算通过缩小完整 turn 数量或进一步缩略可见正文满足，不能把一个 compact turn 截成两页。detailed 的计数分页契约保持不变。

### CLI browsing and diagnostics

`ki session list/search/show/trace/inspect` 是 jsonl 的只读浏览层。已有 serve
进程时 CLI 优先访问它；没有 serve 时直接使用本包读取磁盘，且绝不为了浏览历史启动
server。所有命令提供 `text`、`json`、`jsonl` 输出；默认文本隐藏完整 system prompt、
tool schema、工具参数和 thinking，`show` 的对应显式开关才展开。

`trace` 沿 active leaf 输出稳定的事件 DTO，可按 type、role、tool、失败、时间和 cache
miss 过滤，并一次附带命中项两侧的 context 行。`inspect` 汇总 token、context 峰值、
tool failure、compaction、system/tools 指纹变化和 cache miss。cache miss 分类与 compact
turn 共用同一实现：prompt 为 input + cache read + cache write；误差不超过 1024 忽略，
超过 20k 或上一 prompt 的 50% 才报告；尚未观察到 provider cache 指标时不推断，
compaction 后重置基线。

远程诊断复用现有 `GET /v1/sessions/{id}`：

- `view=trace`：支持 `type`、`role`、`tool`、`failed`、`cacheMiss`、`since`、
  `until`、`context`、`before`、`limit`，返回有界 trace 页。
- `view=inspect`：返回完整 active-leaf 聚合，但不带 transcript body。

两个投影都直接读取 session snapshot，不构建 runtime catalog，也不触发 extension
warmup。未知 `view` 返回 400，避免旧的“静默退化为 detailed”让自动化误读响应。

### Delivery identity

Messages may carry `clientRequestId` independently of their canonical jsonl entry
ID. Queue items preserve it through ordinary dispatch, promotion and live-run
handoff; internal messages receive an ID when accepted. Repeated identical text
with distinct entry/request IDs remains distinct input. A runtime completion also
carries `completion:{taskId,generation}`. Live and queued copies consult one
agent.Controller ownership ledger at actual parent persistence, never at enqueue.
Runtime-only compact history starts at turn 1. Turn clocks start at the first
human input, or first runtime input when no human exists, and are not reset by
later notifications.

The three queue documents now use versioned `internal/state` writes: queue and
extension queue have `{version:1,items:[...]}`, context queue has
`{version:1,next,items}`. Agent metadata is version 3 and records stable task paths, queued follow-up
request IDs, per-generation delivery ownership, and bounded run/lifetime progress. An undrained live Inbox is
atomically closed and transferred to the durable queue on run setup/exit races.
These files do not form a cross-file transaction with jsonl: crash-time
exactly-once delivery and persistence-failure recovery are not guaranteed.

## 独立运行时投影

session GET 的 runtime 附带 processes/agents，分别为进程 manager 与 agent.Controller 的当前投影。process_updated/agent_updated 追加为 sideband entry，不移动 transcript leaf；运行中的 writer 不会因外部进度事件与自己的 leaf 冲突。重启前 running agent 标为 interrupted；先恢复全部身份再调度持久 pending。OS process handle 只在当前 server 生命周期内有效。
