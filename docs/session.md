# 会话格式

一个 session 一个目录，append-only jsonl 树。文件系统是唯一事实来源；进程内缓存只加速
读取，不参与正确性。

## 路径

`{sessions.root}/<encoded-cwd>/<timestamp>_<uuidv7>/`

`encoded-cwd` 是绝对路径去掉盘符，`/` `\` `:` 换成 `-`，两边加 `--`。会话 cwd 来自工作区
path（或临时 `{KI_HOME}/workspace/tmp+<时间戳>`），不是进程 Getwd。标题优先用
`config.title`。`Remove` 删除整个会话目录。工作区见 [workspace.md](workspace.md)。

目录内容：

| 文件 | 用途 |
|---|---|
| `events.jsonl` | 消息树，append-only |
| `config.json` | provider / model / thinking / leaf / title / pin |
| `queue.json` | 用户排队 prompt（最多 100 条） |
| `ext-queue.json` | 扩展 FIFO |
| `context-queue.json` | 不触发运行、但要进入后续 prompt 的 user message（最多 100 条） |
| `agent.json` | 逻辑 agent 身份、代次、pending 与 delivery ledger |
| `telemetry.jsonl` | session-local OTLP/JSON 诊断，不属于消息树 |
| `attachments/` | 内容寻址的粘贴/拖入文件 |

`telemetry.jsonl` 不通过 API/SSE 投影，也不随 fork 复制。tool output spill 文件和 shell
任务日志同样不进入 jsonl：它们属于当前 serve 的 session runtime
（`<os.TempDir>/ki-tool-output/run-<pid>-<rand>/<session>/`），模型只通过 `toolResult`
preview 和 `Read` 引用访问。session 关闭删除该 session 目录，server 关闭删除整个 run
root，重启后不保证旧路径仍存在；崩溃残留的 root 在下次启动时按 owner pid/心跳清扫（见
[tools.md](tools.md#输出溢出)）。

### 并发与原子性

`config.json` / `queue.json` / `ext-queue.json` / `context-queue.json` 经同目录临时文件 +
rename 原子落盘，避免并发读到截断 JSON。写侧和读侧都必须按目录串行：

- `config.json` / `events.jsonl` 走 `fileGate`（`writeConfig`、`appendRaw` 持写锁，
  `Open`、`liteInfo`、`ReadConfig` 持读锁）；
- 队列文件走 `queueGate`。

**为什么读侧也要上锁**：Windows 在 rename 替换期间会拒绝并发 open
（`ERROR_SHARING_VIOLATION`），一次状态轮询或 `POST prompt` 撞上就会报成 404 session not
found；POSIX 的 rename 不动已打开的描述符，所以这个竞态只在 Windows 上出现。

### 追加与删除

新行永远 append 在文件末尾，`config.activeLeafId` 持久化当前分支。append 使用 `O_APPEND`
的短命句柄（写完即关），`Session` 不长期持有 `events.jsonl`：POSIX 允许删除仍被打开的
文件，Windows 不允许，长期句柄会让「删除运行中的 session」和 `t.TempDir` 清理在 Windows
上失败；`session.Remove` 对仍有一瞬写入的目录做短暂重试。旧数据没有 `activeLeafId` 时，
重载以最后一条非 header 为 leaf。

## jsonl

第一行 header：`type=session`，含 `id` / `cwd` / `parentSession` / `forkMode`。
`parentSession` 是直接来源 session 的 id；`forkMode` 为 `flat` 或 `tree`，普通 session 和
普通 fork 默认为 `flat`。

之后每行 `{type,id,parentId,timestamp,…}`，entry 类型：

| 类型 | 内容 |
|---|---|
| `message` | user / assistant / toolResult；assistant 工具调用保存 `toolType` 和 freeform `input` |
| `compaction` | local summary 或 remote checkpoint |
| `model_change` | 模型切换 |
| `request_header` | 该轮 `system`、`tools[]`、provider/model、thinking effort、catalog version 和价格快照 |
| `context_usage` | `usedTokens`、有效 `contextWindow`、`estimated` |
| `patch_apply_updated` | 模型生成 patch 时的结构化非执行预览 |
| `compaction_start` / `compaction_end` | 压缩生命周期 |
| `question_requested` / `question_updated` | 异步问题事实和修订，显式记录来源分支；不进入 provider context、不移动 active leaf |
| `extension_error` 等 | sideband |

entry id 为无连字符的 32 位 hex UUIDv7。`request_header` 的 `tools[]` 含 function/custom
type 和 custom grammar format；消息里的 `toolType` 与 freeform `input` 让 resume 能保持
`custom_tool_call` / `custom_tool_call_output` 配对。`context_usage` 与
`patch_apply_updated` 沿 SSE 到 WebUI，且不进入 provider context。

toolResult message 可带结构化 `details`，以及工具完成时间 `timestamp`（Unix 毫秒）和从
调用开始到完成的 `durationMs`。它随 jsonl 落盘并通过现有 session API/SSE 提供给 WebUI；
历史 WebUI 可由这两个字段恢复工具开始时间。provider 回放只使用模型可见的 `content`，
不会把 diff、patch、计时或任务诊断元数据送回模型。

### 异步问题与旁聊

`question_requested` 按批持久保存公开问题列表和一份私有 `snapshot`，冻结完整协议前缀、
provider/model/thinking、规范化 CWD、prompt 资源及必要的补充用户内容。后续
`question_updated` 只保存公开修订，投影继承原始快照；不会按题数或修订次数复制资源。
sideband 写入在打开文件前校验编码行不超过 64 MiB，拒绝不会留下不可扫描的记录。

问题状态为 `pending`、`dismissed`、`answer_queued`、`answered`。接受答案先记录
`answer_queued`，随后经 Inbox 或新轮次投递；下次明确启动恢复排队答案，停止不会自行
重启。匹配来源祖先、question 和 submission 身份的 typed user entry 才派生 `answered`。
回执全局首次提交有效，切换分支不重投历史答案；`sourceBranchValid` 表示问题来源是否在
当前分支，`answerBranchValid` 表示已投递答案是否在当前分支。

提交可带 `answerForm: {selected, text}` 保存原选项与补充文本；selected 必须为空或原选项，
两字段 trim 后以空行拼接必须等于 answer。首次接受时与答案一同落盘，GET/SSE 返回公开
回执，重开、刷新或另一客户端不依赖浏览器草稿恢复。已有答案的重复提交不修改表单；同时
携带表单的重试须匹配已存表单，省略表单的同答案重试仍幂等。列表导航摘要不保存表单正文。

只读旁聊用 `purpose=question_side`、`sourceSessionId`、`sourceQuestionItemId` 和
`sideBoundaryEntryId` 关联来源；首次旁问时才创建，固定提问时 CWD 与资源；provider/model/thinking 跟随来源主会话，
空闲旁聊在主会话切换后同步，忙碌旁聊在下一轮读取来源最新模型。初始化阶段
保存来源身份以便崩溃重试找回已有子会话；补充消息的附件复制并改写到旁聊目录。
GET/SSE 只显示 boundary 后历史，模型仍接收安全继承背景。公开 metadata 和列表移除
资源与内部路径，历史索引仅缓存公开问题字段；私有快照只在需要时按偏移读取请求事实。

### 分支

`SetLeaf` 只切换 active leaf 并写 config，旧行不删。edit/regenerate 从指定 parent append
sibling branch。

fork：`ForkAt` 新建 session 目录，只写 root → target 路径；历史批量写入时一次提交 JSONL 与最终 config，避免每条历史 entry 单独 fsync；新 header 使用新 id，
`parentSession` 保存源 session id，`forkMode` 保存处理策略。`flat` child 与 parent 独立；
`tree` child 由 server 在删除 parent 时递归清理。子 session 复制源的 `provider` / `model`
/ `thinkingEffort`，不回落到 registry 默认。

API 对外统一把 header 的 `parentSession` 映射为 `parentSessionId`，并同时返回 `forkMode`。
`GET /v1/sessions`、`GET /v1/sessions/{id}` 和 fork response 使用同一组字段；`fork` body 可
传 `{"entryId":"...","forkMode":"tree"}`，省略 mode 按 `flat` 处理。带 `entryId` 时运行中
也可 fork（复制的是已落盘的完整前缀）；省略 `entryId`（fork 活动 leaf）且会话运行中仍
409。

### 上下文投影

中断或崩溃可能留下已持久化的 assistant toolCall，却没有对应 toolResult。上下文投影在
`run_aborted`、后续 `request_header` 或 assistant 消息边界为仍缺失的调用补充 error
结果，保留原调用 ID、名称和 toolType，并明确执行结果未知、重试前应核实副作用。投影先
检查所选分支内真实的后续结果，不为尚有真实结果的调用重复补充；单独插入的 steering
user 消息不构成中断边界。投影不改写原始 jsonl，也不改变 active leaf。

空闲会话启动下一轮时，server 为活动 leaf 末尾仍未闭合的调用追加同样的 error
结果，再追加新用户 prompt；重复恢复不会重复写入，也不会重新执行工具。显式 parent
可选择空闲活动 leaf 或已跨越中断边界的历史前缀；落在历史工具交换中间、尚无结果的
parent 仍拒绝，运行中携带 parent 仍返回 409。

`MessagesToLeaf` 是跨 provider 的 portable projection：沿 parent 走到根，只选择最新 local
compaction，先注入 summary，再取 `retainedTail`（新条目，压缩时最近消息原文落盘）；旧
jsonl 无 `retainedTail` 时回退 `firstKeptEntryId` 截断。remote compaction 不参与 portable
projection，因此切换模型仍可从 append-only 原始消息重建上下文。`LastCompactionAt` 返回
最近 compaction 时间戳（stale-usage 防护用）。

Provider remote compaction entry 的 `responses` 保存严格绑定 `provider` / `api` / `baseUrl`
/ `model` / credential fingerprint / compaction protocol 的 canonical output item 数组。
fingerprint 是 credential 的非秘密 SHA-256 identity，不保存 key/token；换 key、project
credential 或 extension opaque credential 后不会复用旧密文。

`ContextToLeaf(binding)` 仅在最新 compaction 的 binding 完全匹配时返回 opaque prefix +
checkpoint 后 message suffix；否则回到标记过的 portable projection，也不会越过一个更新的
不兼容 remote checkpoint 去复用旧密文。context meter 和 local/remote compact planning 对
该展开历史使用完整序列化估算，不误用只覆盖 compact prefix 的旧 usage。空闲 session 切换
模型后立即追加按新 binding 重算的 `context_usage`。

item 使用 raw JSON 保留未知字段和顺序，单行上限 64 MiB；server-side assistant 与
checkpoint 在同一 file gate 内提交，短写/I/O/config 失败回滚 JSONL 尾部。session
slim/index/compact/trace、精确 entry API 和 CLI full view 只显示固定的 remote-compaction
标签，绝不返回 encrypted payload。

## 配置与队列

### config.json

该 session 的 `provider` / `model` / `thinkingEffort` / `activeLeafId`，可选 `title` /
`pinned` / `pinnedAt` / `metadata`。

fork 还会写入 `cacheKey`：子会话继承 fork 源的缓存亲和身份（源自身的 `cacheKey`，否则源
ID），尽量复用父会话已写入的前缀缓存；它只影响 provider 缓存路由，不保证命中。
只读旁聊的 system/tools 变化、附件路径重写或 provider 的缓存策略都可能导致 miss。
session/thread 身份仍是子会话自己的 ID。`CreateChild` 不复制任何 transcript，因此也不继承该字段。

内置 tools、Skills、extensions 启用在 `{KI_HOME}/toggles.json`，不在 session 里。

`thinkingEffort` 可保存扩展模型声明的组合选择，如 `high fast` 或 `off fast`；fork/resume
与 request-header 快照原样保留，不另存全局 Fast 开关。模型变更由 registry 校验并尽量保留
基础强度与可用的 Fast 意图。provider 请求的 `turnId` 是一次逻辑用户 turn 的传输身份，跨
工具 continuation/retry 不变，不等于 WebUI 的逐模型 round 事件。

### 队列文件

**`queue.json`**：忙时排队的 user prompt。不进消息树直到出队开跑。队列项带 `lane`：
`human` 是人提交的（`POST prompt` + `delivery=queue`），`system` 是服务端生成的（agent
完成通知、agent 发给 caller 的消息），带 `origin` 标记来源；完成通知另带
`completion:{taskId,generation}`，出队可跳过已消费的 generation。

**出队规则是 human 先于 system**，同一 lane 内保持 FIFO：完成通知是从子代理的 goroutine
入队并在 session 空闲时立刻 dispatch 的，若严格 FIFO，先到的通知会抢先成一轮、让人白等
一轮。`EnqueueFront` 会把重试项插回**自己那条 lane**的头部，所以重试也不会插到人的前面。

**`ext-queue.json`**：扩展 FIFO，与用户队列分轨；occupy release 后先用户 queue，再扩展
FIFO。

**`context-queue.json`**：`session.appendMessage` 的持久化暂存队列。消息提交后会变成普通
`message` entry，进入 `MessagesToLeaf()` 和后续 provider prompt；队列项带单调递增序号及
可选 `idempotencyKey`。当前 prompt 入队时捕获边界序号，只提交边界之前的 context，之后到达
的消息留给下一轮。扩展 prompt 的 `idempotencyKey` 另随 `ext-queue.json` 和首条 user entry
持久化。

三个队列文档都使用 versioned `internal/state` 写入：queue 和 extension queue 为
`{version:1,items:[...]}`，context queue 为 `{version:1,next,items}`。

### 消息内容与附件

user message 可保存结构化 `text`、`workspace_file`、`file` 和带宿主绝对路径的 `image`
content。站内浏览器选择的是 workspace 引用；粘贴/拖入文件按 SHA-256 保存到本 session 的
`attachments/`。jsonl 只存引用；server 在 provider 边界读取并编码图片，普通文件变成可供
`Read` 使用的路径说明。fork 复制附件并把新 jsonl 中的路径改到新 session 目录。

### 消息身份

消息可带 `clientRequestId`，独立于其 canonical jsonl entry ID。队列项在普通 dispatch、
promotion 和 live-run handoff 中保留该 ID；内部消息在被接纳时获得 ID。文本相同但
entry/request ID 不同的仍是不同输入。运行时完成还带 `completion:{taskId,generation}`。

live 与 queued 副本在实际写入 parent 时查询同一个 agent.Controller ownership ledger，而不是
在入队时。runtime-only 的 compact 历史从第 1 轮开始。轮次时钟从首个人工 input 起算（没有
人工 input 时用首次 runtime input），后续通知不重置。

这些文件与 jsonl 不构成跨文件事务：崩溃时的 exactly-once 交付和持久化失败恢复不作保证。
未 drain 的 live Inbox 在 run setup/exit 竞态中会被原子关闭并转入 durable queue。

## 读取与分页

### 列表行

列表行只需要 `config.json` + jsonl header + title fallback 的首条 user message：`List` 用
浅扫描，遇到首条带正文的 user message 或 `config.title` 即停，不解析整份 transcript（旧
实现为渲染一行 sidebar 而解码最大 12 MB 的 jsonl）。扫描遇到无法解码的普通行会跳过，不因
一条坏行把整个 session 从列表里隐藏。

server 用 `session.ListCache` 按 `events.jsonl` / `config.json` 的 size+mtime 复用行；文件
仍是唯一事实来源，外部进程写入的变更下次调用即生效。`GET /v1/sessions` 的响应按渲染结果
打 `ETag`，命中 `If-None-Match` 时返回 304。列表读取不调用 `ReadTranscript`，不进行 token
估算，也不占用聊天 transcript 缓存。全局锁只管理行索引；读盘与单会话重建在独立锁下进行。

公开 `questionLinks`（questionItemId/title/status/revision/sideSessionId）从 session 目录的
`question-list.json` 派生缓存读取，缓存仅保存父子关系、问题导航事实、回答校验摘要和已读
偏移，不保存消息正文、回答原文、冻结资源或补充用户输入。缓存带 `version: 1`，通过
`internal/state` 原子写入；缺失或损坏时在后台重建，更高版本不覆盖。首次重建和追加追赶
期间保留已知入口并返回 `questionLinksLoading: true`，完成后通过现有 sessions invalidation
通知刷新。两个后台 worker 限制并发，停止服务时取消并等待任务结束。

追加只扫描新字节；size/mtime 与末尾校验摘要保护读取位置，截断或同大小重写触发重建。
切换分支按当前 leaf 在缓存关系中投影，无需重读历史。普通浅扫描 `List` 保持原行为；问题
修订和加载状态也参与响应 ETag。会话删除与缓存提交共享文件 gate，禁止缓存重建已删除目录。

### 目录索引

serve 进程内维护 `session.Index`（id→dir 内存 map，见 `internal/session/index.go`），启动
时由 `List` 的同一次 walk 顺路建好，零额外读盘。create/fork 后 `Add`、delete 后 `Remove`。
命中即 O(1)；miss（别的进程建的会话、或目录被外部删除）回退到 `Find` 扫描并自愈。

### 尾部窗口

`GET /v1/sessions/{id}` 默认返回 WebUI 的**对话尾部**：

- slim `entries`：当前 leaf 最新的 `limit` 条，默认 100；
- 未变化的 `request_header` 省略 `system`/`tools` 并标 `promptUnchanged`；
- 超过 24 KiB 的正文截断并标 `truncated`；
- `hasMore` / `oldestId`。

窗口按条数上限和 **512 KiB 序列化 entries 字节预算**切出，超预算时缩小连续页；单条过大
时返回保留 id/统计的 `truncated` 摘要，至少推进一条。折叠工具文本预览最多 1 KiB，全文
通过现有 entry/entries 读取。

窗口可能落在某个 turn 中间：响应额外带上开启那个 turn 的 user message（`BuildTail` /
`BuildBefore` 的 `withTurnOpeningUser`），否则前端的 compact 视图会把首个 turn 画成「一条
折叠行 + 最新几条回复」，上面没有提问。

`hasMore` 表示窗口之外是否还有更早的叶子历史：tail-first 读取只是文件的字节窗口，窗口
起点未必是分支根，所以 `BuildTail` 在调用方声明窗口未到达根（`complete=false`）时即使
`tailStart==0` 也保留 `hasMore`，否则刚好装满 `limit` 条的分支会永远翻不到前面。
`oldestId` 仍是窗口自己的边界，翻页照旧逐条向前，被跳过的中间内容仍然可达。不返回
`messages`。

### 查询参数

| 参数 | 行为 |
|---|---|
| `fields=runtime` | 只带 catalog / queue / `runtime.ready`，不读 transcript |
| `fields=index` | 只返回 `{id,index}`，支持内容 ETag / `private,no-cache` |
| `fields=index,runtime` | tail + index + runtime |
| `entry=` / `entries=` | 取未裁剪正文 |
| `before=` + `limit=` | 从 `oldestId` 沿 leaf 向前翻页 |
| `view=compact&keep=N` | compact 稀疏投影（见下） |
| `turn=<id>` | 展开某一轮的隐藏 entry |
| `view=trace` / `view=inspect` | CLI 诊断投影 |

整棵 jsonl 树的 `index`（无正文行，供分支导航和轨迹表）需要 `?fields=index` 才返回：它使用
独立的轻量元数据/文件偏移索引，冷读扫描整个 JSONL，但不保留历史正文、工具参数或 schema；
追加只扫描新增完整行。放在默认响应里仍会增加扫描与几 MB JSON，而最新的几条消息并不需要
它。比一次尾部读取还短的 session 本来就被整份读出，仍顺带返回 `index`，WebUI 因此不必发
第二次请求。

`index` 始终是 JSON 数组，空索引返回 `[]` 而非 `null`；header-only session 也遵守此数组
契约，避免客户端误将 nil 索引标记为已完整加载。

### 缓存

正文读取按文件 identity + size + mtime 走加权 LRU：全局 64 MiB、单会话 8 MiB、最多 256
会话；超大读取照常返回但不准入，淘汰只解除缓存所有权，不修改在途快照。独立
`ReadTranscript` 元数据索引受全局 16 MiB / 256 会话限制；精确正文、较早分页、turn 展开
和 compact 先在轻量元数据中选择条目，再按文件偏移只解码所选正文，不提升完整正文缓存。

相同 system/tools 在各自快照或 Session 生命周期内共享存储，不设永久全局 intern 表。原子
替换会重建缓存，已失效偏移返回错误而非错误正文。预算是保守对象图估算，限制缓存拥有的
存储；活动运行、在途读取和 Go 分配器保留页不属于这些预算。

`TailEntries` / `LeafTail` 缩回最近窗口，`AllEntries` 服务模型执行/诊断所需的完整历史，
`OpenFrom` 直接用已读 entries 构造 Session。

### 文件 identity

Transcript 缓存的文件 identity 使用短命 open handle 的 `Stat` 在读取时捕获，不缓存句柄。
Windows 的 `os.Stat(path)` 会延迟到 `os.SameFile` 才按路径读取 file ID：若两次读取之间
原子替换，旧 `FileInfo` 也会绑定新文件，导致 size/mtime 未变时误命中旧正文。正文和元数据
缓存均必须保存读取当时的 identity，同时及时关闭句柄，避免阻止 Windows 删除或替换会话。

## Compact 投影

`GET /v1/sessions/{id}?view=compact&keep=N` 是面向 compact 阅读的稀疏投影，`keep` 为
0–20，默认 1。首次最多返回最近 4 个完整人工输入 turn；同一请求加 `before=<oldestId>` 每次
返回一个更早的完整 turn。响应带 `entries`、`compactTurns`、`hasMore`、`oldestId`。
`oldestId` 为最早返回 turn 的输入 id，不位于折叠区中途。

每轮传人工 input、最后 N 个正常 assistant/tool 回复需要的正文、其后 runtime 通知，以及
始终独立显示的压缩/中止状态；隐藏区用整轮计数、预览和统计表示。runtime user-role 消息
（origin 非空且不以 `extension:` 开头，例如 subagent 指令、完成通知和 context-only 邮件）
可以折叠，但不消耗 `keep`：首个保留或 live 的真实回复作为单一时间顺序边界，边界之前的
普通回复和 runtime 通知一起折叠并计入 `hiddenCount`，之后保持原顺序显示。

因此 `keep=1` 保留最后真实回复，不会被尾随通知挤掉；`keep=0` 且无 live 回复时所有通知与
普通回复折叠。`keep` 足够保留所有真实回复时，首个真实回复之前的 runtime 指令仍折叠；完全
没有真实回复的通知-only turn 也全部折叠。无 origin 或 `extension:` origin 的人工输入仍是
turn 边界并始终显示；runtime-only 会话仍保留首个可渲染节点的正文作为稳定 turn 锚点，即使
该节点隐藏。跨大 turn 不会循环下载已折叠的工具往返。初次读取会用增量 transcript 缓存
确定完整分支与 turn 边界，不传整树 index。

### compactTurns 字段

`compactTurns[]` 保存 `id`、原始 `parentId` / `tailId`、有序 `entryIds`、`visibleNodeIds`、
少量共享 entry 的 `omittedNodeIds`、`hiddenCount`、`firstHiddenId`、`preview` 与整轮
`stats`。

- `entryCount` 是快照覆盖的整轮 entry 数（含 metadata），用于拒绝迟到的旧投影；
- `assistantAt` 是最近 assistant 的完成时间；
- `toolStates` 仅含最近 assistant 工具批次的 `{id,finished,isError?}`，即使 keep=0 也能将
  正在执行或刚结束的调用与 SSE 精确去重，而不是传输全轮所有隐藏 ID；
- `stats.startedAt` 是开头 user 的服务端时间，`stats.elapsedMs` 覆盖 assistant、工具结果和
  compaction 的最大完成时间；`cumulativeElapsedMs` 累计至该轮，未加载 index 时也包含窗口
  之前的耗时；
- `stepCount` 是截至该轮的累计步骤数，`lastStep` 保存最近步骤的用量与耗时；即使 keep=0，
  composer 的轮数、步骤数和最新用量也不因折叠减少。

持久化 parent 边不改写；前端只在读取稀疏链时跨过省略段，收到整树 index 或展开正文后优先
采用原始链。可见工具所需 call/result 成对保留，同一 call entry 内隐藏的 assistant 正文不
传；完整内容仍通过原正文接口可取。

无效、已切离当前分支的 `before`（含展开局部游标）返回 **409**，不能返回空成功页并假装
已到根。客户端保留缓存并重新确认边界。

显式展开使用同一 GET 的 `turn=<turnId>`（可配 `before` / `limit`），返回限定在该轮内的
slim entry 页；其 `hasMore` / `oldestId` 是展开的局部游标，不能覆盖会话历史边界。
`view=compact&turn=<entryId>&keep=N` 可重新投影某个已知 turn，也可把 detailed 页边界所在的
半轮转换成完整 compact 轮。页预算通过缩小完整 turn 数量或进一步缩略可见正文满足，不能把
一个 compact turn 截成两页。detailed 的计数分页契约保持不变。

## 只读投影

以下字段由 server 从完整持久化内容派生，只用于展示，不写回 jsonl，也不进入 provider
请求。

### remoteContext

Public entry/index projections 对 remote checkpoint 暴露派生的 `remoteContext: true` 标记，
包括 slim/compact 页和精确 entry hydration。该标记在 provider payload 脱敏后仍然保留；
encrypted `responses` 内容永不暴露。它标识 checkpoint 类型，不表示当前 provider binding
是否兼容，也不写入 jsonl。

### contextEstimate

`contextEstimate: {system?, tools?, message?, summary?}` 在元数据预览、截断或未变化 header
省略之前，从完整存储内容计算。每个存在的数字是近似 UTF-8 字节数除以四，按 block/schema
向上取整；`0` 表示已知为空，字段缺失表示未知或不适用。

Message 估算包含 text、thinking 和 tool-call name/input/pretty JSON arguments；tool 估算
累加 compact schema JSON，local summary 包含其 portable summary prefix。图片、opaque
签名和 remote checkpoint 不按 plaintext 计价。这些估算在不 hydrate 每个正文的前提下保留
历史分类尺寸，与 provider usage/最终编码输入保持区分，且永不写入 jsonl 或用于回放。元数据
扫描只保留估算和预览；重复 schema 使用有界 digest/count 缓存，不保留完整 schema。

### cacheMiss

Public assistant `Entry` 投影可带 `cacheMiss: {missedTokens, missRatio}`。它按 active-leaf
路径上的 assistant usage 计算，阈值与 trace、compact-turn 统计一致；compaction 重置比较
基线。

compact、detailed tail/before 页、精确 entry hydration 和实时 `message_end` 因此识别同一
条 assistant，即使更早的 entry 被隐藏或不在传输页中。这是 view-only 元数据：不写入
jsonl，不加入 provider `Usage`，也不回传 provider。默认 detailed tail 扫描/缓存无正文的
transcript 元数据来计算该投影，但仍只 hydrate 和传输请求的 entry 正文，不返回完整 index。

### 工具审计索引

轻量 `index` 保留 toolResult 的 `toolCallId`、`name`、`durationMs` 和 `isError`（省略
`false` 表示无失败）。Code Mode 审计的 start/update/end 行额外保留可选 `parentCallId`、
`cellId` 和 `requestedToolName`，复用同一组 call/name/outcome 字段。

这些只是有界标量事实：元数据缓存和索引永不保留嵌套参数、进度或结果正文。消费方沿所选
leaf 的 parent 链按 tool call ID 去重（含嵌套执行），而不是统计生命周期帧或只数已加载
正文的聊天节点。未加载的审计投影只确立调用身份和失败状态，不表示完整内容；精确正文仍
通过既有 entry API 获取。

## CLI 浏览与诊断

`ki session list/search/show/trace/inspect` 是 jsonl 的只读浏览层。已有 serve 进程时 CLI
优先访问它；没有 serve 时直接使用本包读取磁盘，且绝不为了浏览历史启动 server。所有命令
提供 `text`、`json`、`jsonl` 输出；默认文本隐藏完整 system prompt、tool schema、工具参数
和 thinking，`show` 的对应显式开关才展开。

`trace` 沿 active leaf 输出稳定的事件 DTO，可按 type、role、tool、失败、时间和 cache miss
过滤，并一次附带命中项两侧的 context 行。`inspect` 汇总 token、context 峰值、tool
failure、compaction、system/tools 指纹变化和 cache miss。cache miss 分类与 compact turn
共用同一实现：prompt 为 input + cache read + cache write；误差不超过 1024 忽略，超过 20k
或上一 prompt 的 50% 才报告；尚未观察到 provider cache 指标时不推断，compaction 后重置
基线。

远程诊断复用现有 `GET /v1/sessions/{id}`：

- `view=trace`：支持 `type`、`role`、`tool`、`failed`、`cacheMiss`、`since`、`until`、
  `context`、`before`、`limit`，返回有界 trace 页；
- `view=inspect`：返回完整 active-leaf 聚合，但不带 transcript body。

两个投影都直接读取 session snapshot，不构建 runtime catalog，也不触发 extension warmup。
未知 `view` 返回 400，避免旧的「静默退化为 detailed」让自动化误读响应。

## 运行时投影

session GET 的 runtime 附带 `processes` / `agents`，分别为进程 manager 与
agent.Controller 的当前投影。`process_updated` / `agent_updated` 追加为 sideband entry，不
移动 transcript leaf；运行中的 writer 不会因外部进度事件与自己的 leaf 冲突。

重启前 running agent 标为 interrupted；先恢复全部身份再调度持久 pending。OS process handle
只在当前 server 生命周期内有效。

## 删除

server 删除先建立 session tombstone 和 agent subtree admission fence，等待已接纳 spawn
commit/rollback 后再收集 tree；取消并等待 writers/release/完成回调后才关闭 manager 和删除
目录。队列 dispatch 不能重占已删除会话；flat child 和其它 root 不被此 fence 级联。server
shutdown 以共享完成屏障保证最后的进度回调结束后才清理输出文件。
