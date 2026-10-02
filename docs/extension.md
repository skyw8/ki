# 扩展（Extensions）

包是带 `extension.json` 的目录。声明式贡献并入 Snapshot；需要跑代码时只走 **NDJSON JSON-RPC 2.0 sidecar**（语言无关，不编进 ki）。实现包：`internal/extension`。所有扩展均独立编译为 executable，通过 NDJSON JSON-RPC 子进程启动；Go/Rust 扩展实现和 executable payload 不链接/嵌入 Ki host，源码包的 installer 也是外部构建子进程。

**不做**旧 `hook` / `intercept` / `intercept[]` 协议。代码能力对外只订事件（`lifecycle`）+ 可选 inbound Host 方法。

## 发现与开关

| 位置 | Scope |
|---|---|
| `{KI_HOME}/extensions/<name>/` | 全局 |

- `name` 是主键。`name` 须匹配 `^[a-z0-9][a-z0-9-]{0,62}$`，禁止 `ki.` 前缀。
- 启用开关是进程级 `{KI_HOME}/toggles.json` 的 `extensions.disabled`（缺省空 = 全开）。
- 禁用的包仍出现在列表（`enabled: false`），但不贡献、不拉起 sidecar。
- runtime 启动失败每 2s 重试；初始化成功后若不足 30s 就退出，首次立即重试，连续快速退出按 1/2/4/8/16/30s 退避，稳定运行 30s 后重置。修改或禁用该包立即唤醒等待；无关 catalog 变更不取消已有退避。安装/初始化尚未完成时遇到 catalog 代际变更或关闭，旧进程不能发布为运行态，会被关闭。
- session 快照可在全局 reload 前临时接纳新发现的包名；已被 catalog 移除/禁用的已知名字不能由旧快照重新启用。下一次全局 Configure 接管这些临时启动，并阻止旧代际初始化完成后再发布。
- 目录列表和 prompt/lifecycle 链均按全局包名排序。
- 仓库 `extensions/` 下的扩展使用 Go；`zvec-grep` 是使用原生 Rust 检索引擎的例外。协议仍然语言无关，第三方扩展可使用任何语言。
- 从仓库根运行 `go run ./scripts/build-extensions.go`，在 `var/extensions/<name>/` 生成分发包；可用 `-only goal,telegram-bot` 选择包，Go 包也支持 `-goos windows -goarch amd64`。将生成的目录复制到 `{KI_HOME}/extensions/<name>`。随包 manifest 直接启动 `bin/<name>`（Windows 文件为 `.exe`），并声明 `runtime.install=["go","run","./install/main.go"]`、`runtime.installWhen="missing"`。二进制存在时完全跳过 install，运行时不需要源码或 Go / Rust / Bun / Node / Python 工具链。
- 默认二进制分发包只含 manifest、声明式 locale/prompt 和编译后的可执行文件；install 元数据保留，但不附带源码或 install wrapper。删除二进制后须重新安装二进制包，或换用源码包，不能在缺失源码时自动编译。
- `go run ./scripts/build-extensions.go -source -out var/extensions-source`（可配 `-only`）在独立输出目录生成可复制到仓库外的源码包：包根为独立 `ki/extensions/<name>` Go module，通过 `require ki v0.0.0` 和 `replace ki => ./_ki` 使用随包的最小共享源码；`_ki` 保留版本锁定的根 go.mod/go.sum，仅含 `pkg/extensionrpc`、`pkg/extensionbuild`、`internal/state` 的非测试源码。源码包附带 Go/Rust 源码、编译所需的 schema/prompt、锁文件与 install wrapper，不包含 config/state/cache、密钥、构建产物或依赖缓存。缺失 executable 时 Go installer 在包根以 CGO 关闭的 native `go build .` 编译；Rust 例外用 `cargo build --release --locked`，发布相同字节到 `bin/zvec-grep` 和 `bin/zg`。编译成功后原子替换 binary，包内锁防止并发首次启动重复编译。
- Go sidecar 把运行必需的 schemas/catalog/prompt 文案嵌入二进制；Rust 检索 sidecar 将引擎、native 库及词典嵌入单个 executable，在启动时展开到 `{KI_HOME}/cache/extensions/zvec-grep/` 下按内容哈希命名的私有目录。`bin/zg` 是同一 executable 的 CLI 入口；其 PATH 声明为 `bin`。
- 源码包生成先规范化根 go.mod 的 LF/CRLF 行尾，再改写独立 module 名称；Windows checkout 不会保留 `module ki` 而丢失本地共享模块依赖。发现阶段先解析包根的真实路径，安装前的缺失 executable 路径校验使用同一规范化根，不把 macOS/Windows 临时目录别名误判成逃逸。
- Rust 包需要在目标平台用 Rust 1.98.0、C++、CMake、libclang 构建，首次构建还需网络；具体依赖和原生平台支持见 `extensions/zvec-grep/README.md`。新的 Rust 索引格式不同，旧 JavaScript 索引须由用户明确执行 `/zg-index --rebuild`；不会隐式重建。
- `internal/extension` 的测试校验随包 manifest、能力和 locale key 对齐，并实际启动不含源码且 PATH 不含语言工具链的 Go 分发包，并通过 Host 安装/启动复制到仓库外的全部 Go 源码包。Rust 构建与协议/真实检索测试单独运行，CI 同样提供源码剥离后的 Host 握手测试。
- Rust 单元测试统一使用 `node extensions/zvec-grep/test/native.mjs`（仓库根）；它先以 Rust 1.98.0 测 native，再编译 launcher 的测试目标。Linux 自动为 bindgen 补 GCC 标准 C 头文件路径，与 launcher 的构建回退一致；显式环境设置优先，继续复用锁文件和 Cargo 缓存。
- Telegram 附件下载直接流入目标旁的临时文件，成功关闭后 rename 发布；失败或取消不覆盖已有附件。50 MiB 下载上限保持不变，超过上限明确报错，不发布截断文件。

## 包布局

```
my-ext/
├── extension.json
├── prompt/APPEND.md
├── skills/…/SKILL.md
├── commands/*.md
├── locales/en.json
├── locales/zh.json
└── bin/extension
```

路径相对包根，禁止 `..` 逃逸。

## extension.json

```json
{
  "name": "protected-paths",
  "version": "0.1.0",
  "description": "…",
  "capabilities": ["prompt.append", "skill", "command", "path", "tool", "lifecycle", "bus", "provider", "channel", "settings"],
  "failClosed": false,
  "prompt": { "append": ["prompt/APPEND.md"] },
  "skills": ["skills"],
  "commands": ["commands"],
  "i18n": {
    "defaultLocale": "en",
    "resources": { "en": "locales/en.json", "zh": "locales/zh.json" }
  },
  "providers": [{
    "id": "example-provider",
    "name": "Example Provider",
    "api": "example-responses",
    "baseUrl": "https://example.invalid/api",
    "auth": { "type": "oauth", "subscription": true },
    "models": [{ "id": "example-model", "contextWindow": 128000, "maxTokens": 16384, "input": ["text"] }]
  }],
  "runtime": { "kind": "rpc", "command": "bin/extension", "args": [], "install": [], "installWhen": "always", "env": {}, "path": ["bin"] }
}
```

- `capabilities`：门闸。未声明的能力：对应字段忽略；`initialize` 多报的 tools/commands 丢弃并 `extension_error`。
- `failClosed`：缺省 `false`。仅 **sync** 生命周期入口：`tool_call` 失败 → 合成 block；`before_provider_request` 失败 → canned stop。
- `runtime.kind`：`none`（缺省）| `rpc`。`rpc` 须声明 `tool` / `lifecycle` / `command` / `bus` / `provider` / `channel` / `settings` 之一。
- `runtime.command`：无路径分隔符（`node` / `bun` / `npx`）走 **PATH**；带 `/` 的相对路径相对包根（`bin/extension`）；绝对路径原样用。
- `runtime.install`：可选 argv，sidecar **启动前**在包根执行（装依赖或构建 executable）。stdout 并进 stderr，避免污染 NDJSON。失败则不拉起 sidecar。
- install 使用独立进程组（Windows job）；取消安装时同时终止 installer/compiler 后代，避免只杀 `go run` 启动器而遗留后台构建和安装锁。等待退出后释放平台进程组资源。
- `runtime.installWhen`：缺省/`always` 时每次启动都执行 install；`missing` 时仅当包内相对 `runtime.command` 文件不存在时执行（Windows 同时识别 `.exe`）。`missing` 必须用于 RPC，并配包内相对 command；不允许依赖 PATH/绝对路径来判断是否缺失。仅文件存在即可跳过 install；不是普通文件或无法 stat 时返回错误，不隐式覆盖。
- `runtime.path`：可选的包内目录列表，声明后并入 shell 工具子进程的 `PATH`（详见下文「扩展 PATH 目录」）。需要 `path` 能力；缺声明但写了 `runtime.path` 会按 manifest 错误禁用整个包。目录必须相对包根且不得逃逸（绝对路径、`..`、空串都拒绝）；绝对路径按「任意平台」判定，`/`、`\` 开头或带盘符（`C:`）都拒绝，不随读取 manifest 的宿主变化（Windows 上 `filepath.IsAbs("/bin")` 为假）；**不校验目录是否存在**，因为 `node_modules/.bin` 之类由 `runtime.install` 在这些校验之后创建。
- `i18n`：可选的扩展自有文案包。`resources` 将 locale 映射到包内的 UTF-8 JSON 文件；文件内容是扁平的 `key -> string` 字典，扩展可以自行使用点号组织 key。`defaultLocale` 缺省时优先使用 `en`，再使用字典中排序最前的 locale。路径必须留在包根内，单个资源最多 256 KiB。
- i18n 资源是展示数据。资源文件缺失、格式错误、超限或包含非法 UTF-8 时，该 locale 会被忽略，不能阻止扩展运行；WebUI 会回退到其它 locale、`fallback` 或 key。
- Host 只读取并转发经过校验的 catalog，不合并扩展 key，也不在 Go 或 WebUI 的 host 字典中维护扩展文案。

## 支持的能力

| 能力 | 类型 | 作用 |
|---|---|---|
| `prompt.append` | 声明式 | system 第 6 层 |
| `skill` | 声明式 | 额外 skill 根 |
| `path` | 声明式 | 把包内目录加入 shell 工具的 `PATH`（`runtime.path`） |
| `command` | 声明式 + 代码 | markdown slash；sidecar `command.invoke` |
| `tool` | sidecar | `tool.execute`；模型裸名 |
| `lifecycle` | sidecar | 订事件：`initialize.subscriptions` |
| `bus` | sidecar | 扩展间总线 |
| `provider` | 进程级 sidecar | 注册模型/认证元数据并接管 provider stream |
| `channel` | 进程级 sidecar | 接入外部消息渠道并调用 Host session 能力 |
| `settings` | 进程级 sidecar | 声明全局配置 schema，由 Host 脱敏、校验和通知变更 |

## 扩展 PATH 目录

声明 `path` 能力并列出 `runtime.path` 后，这些目录会出现在 shell 工具 exec_command派生的子进程 `PATH` 里，使扩展自带的 CLI 对模型可见，而不要求用户全局安装。

顺序：**ki 内嵌 rg/fd 目录 → 扩展目录（按扩展名排序）→ 用户原 `PATH`**。ki 的目录永远最前，扩展无法顶掉 `rg`/`fd`；扩展目录在用户 `PATH` 之前，保证 shell 里敲到的版本与 sidecar 使用的一致。重复声明的目录会去重。

- 只有**已启用**（`toggles.json` 未禁用）且 manifest 无错误的包贡献目录；目录来自 session 的资源快照，因此启用/禁用与 reload 语义和 skills / prompt 一致：下一轮 prompt 生效，运行中的 run 保持旧环境。
- 目录若此刻不存在会被跳过（例如依赖尚未安装），下一次组装工具时重新解析，所以 `runtime.install` 建出的目录不需要额外 reload。
- 生效范围只有 ki shell 工具派生的子进程：不影响用户终端、扩展 sidecar 自身的环境、`Grep`/`Glob` 的内嵌引擎。
- `bash -lc` 会先 source `/etc/profile`，Debian 的 `/etc/profile` 会整体重置 `PATH`，所以 exec_command 的 Bash 解释器额外通过 `BASH_ENV` 的 shim 在 profile 之后按 `KI_EXTENSION_PATH_DIRS` 再前置一次；PowerShell 不需要 shim。
- 扩展目录位于用户 `PATH` 之前，理论上可以 shadow 用户的同名命令。这是声明式、可见的（明细见 `runtime.path` 与 `docs/tools.md`），且目录必须留在包根内，不能指向 `/usr/bin` 之类的任意位置。

## 订事件

`initialize` result：

```json
{
  "tools": [],
  "commands": [],
  "fallback": false,
  "subscriptions": [
    { "event": "tool_call", "mode": "sync" },
    { "event": "agent_end", "mode": "async" }
  ]
}
```

- 未知 `event` 或该点不允许的 `sync`：**该条拒载**。
- 声明了 `lifecycle` 但没有任何有效订阅：**整包加载失败**。
- **sync**：停靠点 `lifecycle.invoke`（`event` + payload + `ctx`），await，应用 result。
- **async**：persist/SSE **之后** notification `lifecycle.event`；瘦 DTO；fail-open。同一 run 的通知保持 loop 产生顺序，尤其 `message_end` 必须先于该 run 的 `agent_settled`。
- Host 按具体事件筛选订阅者；无人订阅时不分配 fan-out payload，同一事件的 JSON payload 只编码一次供全部订阅者复用。写入仍在原事件调用内按扩展顺序完成，不增加保留流式消息快照的异步队列。
- 异步 `tool_execution_start/end` DTO 带 Unix 毫秒 `timestamp`；end 还带 `durationMs`，便于扩展记录工具执行耗时。
- 同一 event：先 sync 链，再 async（async 见最终态）。
- 链序：全局按名。
- `before_agent_start`：**每个 occupy 一次**；steer 不重跑。每 turn 改 messages 用 `context`。
- sync 载荷带紧凑 `ctx`：`idle`、`model`、`aborted`。`before_agent_start` 可见 system 全文。

### Event 目录

| event | sync | async | 控制效果 |
|---|---|---|---|
| `before_agent_start` | 是 | 是 | 改 system / messages |
| `context` | 是 | 是 | 改 messages |
| `before_provider_request` | 是 | 是 | 改 request；`shortCircuit` |
| `before_provider_headers` | 是 | 是 | 改 URL/headers |
| `after_provider_response` | 否 | 是 | — |
| `provider_error` | 是（需 initialize `fallback`） | 是 | fallback text |
| `tool_call` | 是 | 是 | block / 改 args / terminate |
| `tool_result` | 是 | 是 | 改 result / terminate |
| `input` | 是 | 是 | 改写或吞掉用户输入 |
| `message_end` | 是 | 是 | 替换同 role 最终 message |
| `session_before_compact` | 是 | 是 | cancel / 定制 summary |
| `agent_start` `agent_end` `agent_settled` | 否 | 是 | — |
| `turn_start` `turn_end` | 否 | 是 | — |
| `message_start` `request_header` `message_update` | 否 | 是 | — |
| `tool_execution_update` | 否 | 否（不投） | — |
| `tool_execution_start` `tool_execution_end` | 否 | 是 | — |
| `compaction_start` `compaction_end` | 否 | 是 | — |
| `queue_changed` `steer_accepted` `run_aborted` | 否 | 是 | — |
| `context_usage` `extension_error` `extension_notice` | 否 | 否 | — |

`agent_settled`：occupy 结束且 Host 内部收尾（含 auto-compact）完成，可接受新 occupy。**不含**扩展 FIFO 已空。

### 工具批

默认并行执行一批 tool call。`tool_call` sync 按助手源序逐个跑完再执行。任一次 `terminate`（block 或 result）且该批**每个**已完成结果都 terminate 时，主循环不再请求模型。

## Sidecar 协议

NDJSON JSON-RPC 2.0。环境：`KI_EXTENSION`、`KI_HOME`、`KI_EXTENSION_ROOT` + 平台必需的 PATH/locale、profile（含 Windows USERPROFILE/LOCALAPPDATA/APPDATA）及 temp 变量 + Ki 启动时继承的 `HTTP_PROXY`、`HTTPS_PROXY`、`FTP_PROXY`、`ALL_PROXY`、`NO_PROXY`（含小写变体）+ `runtime.env`。`runtime.env` 对同名变量拥有最终覆盖权。install 命令继承 Ki 父进程的完整环境，再覆盖上述扩展作用域 KI_* 值和 runtime.env，因而保留 Go/Rust/native 编译所需的工具链与缓存变量；运行时 sidecar 仍使用上述 allowlist，其子进程继承 sidecar 环境。全局 install/sidecar 清除父进程继承的 `KI_SESSION_ID`/`KI_CWD`，不固定到首个 session；session 相关 RPC 显式携带 `sessionId`。

超时：`initialize` 10s；sync 生命周期 2s；`tool.execute` 120s（超时后先发 `cancel`，再宽限 2s 收取 sidecar 在途的部分结果）；`command.invoke` 15s；provider stream start 10s。

### Host → sidecar

| method | 门闸 | 说明 |
|---|---|---|
| `initialize` | — | params：home/extensionRoot/capabilities/scope/providers；全局扩展的 `sessionId`/`cwd` 为空；result：`{tools,commands,fallback,subscriptions}` |
| `shutdown` | — | 关闭 |
| `tool.execute` | `tool` | `{sessionId,...}`；进度：`tool.progress` |
| `command.invoke` | `command` | `{sessionId,name,args}`；result：`{handled,notice,prompt}` |
| `lifecycle.invoke` | `lifecycle` + 该 event sync | `{sessionId,...}`；同步改流 |
| `lifecycle.event` | `lifecycle` + 该 event async | `{sessionId,...}` 通知 |
| `cancel` | — | `{id}` |
| `session.open` / `session.close` | — | `{sessionId,cwd}` / `{sessionId}`；通知全局 sidecar 建立或释放该 session 的业务视图 |
| `ui.action` / `ui.submit` | UI 投影 | 用户点了面板 |
| `bus.event` | `bus` | 他方 emit / 广播 |
| `provider.stream.start` | `provider` | `{requestId,request}`；一次传入完整 model、credential 和 loop request，`request` 使用 lower camelCase 字段名，返回 `{accepted:true}` |
| `provider.stream.cancel` | `provider` | `{requestId}`；取消一个 provider stream |
| `provider.compact` | `provider` | 模型声明 `compaction.standalone` 后的 standalone remote compaction；接收完整 model、credential、已应用 `context` / `before_provider_request` 的 loop request，返回 `{items:[...],usage?}` 完整有序 canonical window；单条 RPC 上限 65 MiB。 |

`config.updated` 是 Host 发给全局 sidecar 的配置变更通知，参数包含脱敏后的
`config`；sidecar 应重新读取自己的私有配置文件。

异步消息生命周期事件的瘦 payload 只包含路由和展示所需字段。`message_start`、
`message_update`、`message_end` 会携带 `role`、`text`；最终消息还可能携带
`stopReason`、`errorMessage` 和 `isError`。当 `stopReason=error` 时，扩展应丢弃
已经收到的 partial 文本，并向用户展示错误信息，而不是把 partial 当成最终答案。

Provider auth RPC（同样只发给进程级 provider sidecar）：

| method | 说明 |
|---|---|
| `provider.auth.start` | `{requestId,provider,mode}`，`mode` 为 `browser` 或 `device_code`；立即返回 accepted |
| `provider.auth.input` | `{requestId,provider,value}`；提交 redirect URL 或手工 authorization code |
| `provider.auth.cancel` | `{requestId,provider}`；取消未完成的登录 |
| `provider.auth.refresh` | `{provider,credential}`；sidecar 决定是否刷新，返回新的 opaque credential |

sidecar 通过 `provider.auth.event` notification 报告 `auth_url`、`device_code`、`completed`、`error`。`completed` 的 credential 只在 sidecar 与 server auth broker 之间传递，server 对 WebUI/CLI 只返回状态、URL 和设备码。

provider capability 使用进程级 sidecar，不随 session 各拉起一个进程。provider 只能从 `{KI_HOME}/extensions` 声明；`providers` 是扩展清单中的离线目录，provider sidecar 只负责对应 provider 的认证/网络/响应解析；宿主只保留模型目录、凭据状态、取消、背压和 loop 适配。一次 stream 的结果通过 sidecar → Host 的 `provider.stream.event` notification 回传：

```json
{"jsonrpc":"2.0","method":"provider.stream.event","params":{"requestId":"stream-1","type":"text_delta","contentIndex":0,"delta":"hello"}}
```

事件类型首版为 `start`、`text_start`/`text_delta`/`text_end`、`thinking_start`/`thinking_delta`/`thinking_end`、`toolcall_start`/`toolcall_delta`/`toolcall_end`、`custom_tool_call_input_delta`、`done`、`error`。`done` 可携带完整最终 `message`。只有模型声明 `compaction.inline:"openai"` 且该 occupy 实际启用 server-side compaction 时，sidecar 才可在 completed `done` 的私有 `responsesItems` 返回完整 canonical suffix；standalone-only sidecar 的同字段会被丢弃。

provider sidecar 的生命周期、凭据和流都是全局进程级资源；session 只通过 `requestId` 复用同一个 sidecar。Reload 时保留仍注册的 sidecar，移除或禁用的 provider 会关闭对应进程。

### sidecar → Host（inbound request）

`readLoop`：有 `method`+`id` 且不是 pending response → Host 方法。**快速返回**；禁止在 sync 生命周期栈里等待整轮 run。

| method | 门闸 | 说明 |
|---|---|---|
| `session.create` | — | 全局创建 session；可传 `workspaceId`、`cwd`、model 和 metadata；`workspaceTitle` 只在新建 workspace 时生效，用于给频道会话可读的显示名 |
| `session.list` / `session.get` | — | 查询 session，可按 metadata 过滤；不绑定当前 session |
| `session.new` | — | 当前 session 创建同配置的新 session，可选新 `cwd` |
| `session.reload` | — | 重载当前 session 的资源和扩展视图 |
| `session.enqueue` | — | `{sessionId,...}`；`content`、`deliverAs`=`queue`\|`steer`\|`nextTurn`（默认 queue）、`when`=`now`\|`settled`、`idempotencyKey`、`kind`=`user`\|`custom` |
| `session.snapshot` | — | `{sessionId}`；idle、running、queues、provider/model、tools、commands |
| `session.appendMessage` | — | `{sessionId,message,idempotencyKey}`；追加正常 user message；不启动模型 |
| `session.appendEntry` | — | `{sessionId,...}`；jsonl custom；强制本扩展名；不进 provider context |
| `session.abort` | — | 同 POST abort |
| `session.compact` | — | 同 HTTP compact；可传 `{instructions}` |
| `session.patch` | — | model / thinkingEffort |
| `session.setActiveTools` | — | 会话级；未知名 warn，不静默清空 |
| `tools.register` | `tool` | 下一 occupy 生效 |
| `ui.setStatus` / `ui.setPanel` / `ui.clearPanel` | — | 当前 session 的内存投影 + SSE；不进 jsonl。面板是通用壳，不解析业务。可展示的文案字段接受原始字符串或扩展自有的 `UIText` |
| `ui.setGlobalStatus` / `ui.setGlobalPanel` / `ui.clearGlobalPanel` | — | server 级内存投影；不进 jsonl；通过 `/v1/extensions` 返回，适合首页可见的全局状态；global panel 只读，配置走 config API |
| `ui.confirm` / `ui.select` | — | WebUI 弹层；**120s** 超时 = 取消 |
| `bus.emit` | `bus` | 深拷贝 fan-out；result 为合并后 data |
| `bus.broadcast` | `bus` | fire-and-forget，不等待 |
| `bus.subscribe` / `bus.unsubscribe` | `bus` | 运行中改订阅 |

error tone 的完整文案会显示在统一扩展 Modal 标题下的独立红条，详情/配置切换不隐藏；runtime/manifest 错误也在此显示。进程 ready 只表示 sidecar 可调用，不能代表索引任务已恢复。zvec-grep 的建索引失败不按时间清空，下次索引任务才替换；成功状态 30 秒后清空，下次任务会取消旧计时器。索引版本或 embedding 不兼容时，查询/建索引错误明确提示 `/zg-index --rebuild`。

除 `session.create`、`session.list`、`session.get` 和 `ui.setGlobal*` / `ui.clearGlobalPanel` 外，上表 inbound 方法都必须带 `sessionId`，bus 订阅也按 session 维护。`session.open` 是 Host→sidecar 的 session 生命周期通知。`ui.setPanel` 和 `ui.setGlobalPanel` 由 WebUI 按通用壳渲染，Host 不解析扩展语义。壳的面、投影、字段表和 `ui.action` / `ui.submit` 见 [webui.md 扩展 UI 壳](webui.md#扩展-ui-壳)。

`origin` 一律 `extension:<name>`，并写进该次 occupy 的 user message（WebUI 气泡可区分）。扩展 FIFO 与用户 `queue.json` **分轨**；occupy release 后 **先用户 queue，再扩展 FIFO**。`when=settled` 在 `agent_settled` 后只写入扩展 FIFO（不直接 occupy），再走同一套 dispatch。`nextTurn` 挂到下次**用户** occupy，注入 messages，不自触发 occupy。`session.setActiveTools` 忽略未知名并发 `extension_notice` warn；全部未知名则保留上一套工具。`session.patch` 与 HTTP PATCH 同一套 ResolveSpec / thinking 校验。

`session_before_compact` 在 Host 完成 portable plan 后调用，payload 为 `{reason,willRetry,instructions?,preparation}`；preparation 含 strategy、source/kept entry id、待摘要消息、split-turn prefix、retained tail、tokensBefore 和 previousSummary，不含 Responses items、binding、credential 或 provider-facing transformed request。返回 `{cancel:true}` 可取消，或 `{result:{summary,usage?,details?}}` 可替换本地生成；Host 仍校验并控制 cut/tail/tokens。strict remote 不接受 custom local result/instructions。结束后的 `compaction_end` 提供 `status`、`strategy`、`fromExtension`、`firstKeptEntryId`、`tokensBefore`、`usage` 及 overflow 的 `willRetry`。

`session.appendMessage` 只接受 `role=user`，追加的是 provider 可见的正常
`message` entry，但本身不 occupy、不调用模型。Host 会先把消息写入持久化
`context-queue.json`；session 空闲时立即提交，session 忙时在下一条 prompt 的
边界前按序提交。请求可带持久化 `idempotencyKey`，Telegram 等外部渠道重试时不
会重复写入。prompt 入队时记录当时的 context 序号，因此 prompt 之后到达的
context message 不会泄漏到当前 prompt，只会进入下一次 prompt。

`session.enqueue` 的 `idempotencyKey` 同样会写入扩展 FIFO；prompt 运行后会写入
首条正常 user entry。重试时 Host 先检查已提交 entry 和持久化 FIFO，再决定返回
`duplicate` 或原 queue id。

## 扩展总线

Host 不解析 channel。协作协议（如 `workflow:mutex:v1`）由扩展自行实现。mutex：emit `{sessionId,group,busy:false}`，持锁方置 `busy=true`。

## 失败策略

默认 occupy fail-open：RPC/超时 → `extension_error`，本 occupy 该扩展进 skip 集，不写 `toggles.json`。`failClosed: true` 仅 `tool_call` / `before_provider_request`。

加载期失败会禁用包：manifest 校验失败、`sidecar_start`、以及 initialize 报了未声明的 tools/commands（`undeclared`）都会写入 `toggles.json` `extensions.disabled`。sidecar 在仍启用时会自动重试；session Prepare 一旦上报 `sidecar_start`，Host 将其关掉。

## 生命周期

1. Scan 只读 Discover；sidecar 是进程级资源，每个扩展最多启动一个。
2. **server 完成监听后**，所有启用且声明可执行 runtime 的扩展统一启动，不按能力类别或 session 懒加载；sidecar 在仍启用时失败会重试，catalog 暴露 runtime 状态。
3. `GET /v1/sessions/{id}` 带 `runtime.ready`；未就绪也可先出 transcript。`runtime_ready` SSE（sideband，不进 jsonl）。失败也算 ready。
4. `Prepare` 只建立当前 session 的扩展视图，复用已运行的 server sidecar，并发送 `session.open`。
5. Steer 不重新 Prepare，不重跑 `before_agent_start`。
6. Reload / Close：Reload 重新扫描 manifest、配置并重启有变化的 sidecar；server Close 才杀全局 sidecar 进程组。忙时 Reload 排到 occupy release 之后。

## HTTP

| 方法 | 路径 | 作用 |
|---|---|---|
| GET / PATCH | `/v1/extensions` | 全局列表 / `disabled`，包含 runtime 状态、全局 `ui` 投影、声明的 `pathDirs`，以及已加载的 skills / tools / commands / promptAppend / providers |
| GET / PATCH | `/v1/extensions/{name}/config` | 读取脱敏配置 / 校验并保存扩展配置 |
| GET | `/v1/sessions/{id}` | `availableExtensions`（含已加载的 skills / tools / commands / promptAppend / providers / `pathDirs`）、`commands`、`extensionUi`、`queued`、`extQueued`、`runtime.ready` |
| POST | `/v1/reload` | 重新扫描并协调 sidecar |

`path` 只展示，不当 `href`。

扩展 catalog 展示启用配置、manifest 错误、server 级 runtime 状态、global UI 投影、可选 i18n catalog，以及该包已加载的 skills / tools / slash 命令 / prompt append / providers；`pathDirs` 逐条给出声明的绝对路径与当前是否存在，让“目录没生效”和“PATH 覆盖了同名命令”两件事都能被发现。查询不会启动 sidecar。sidecar 尚未握手时 tools/runtime commands 可能为空，session Info 在 `runtime.ready` 后再拉一次。manifest 校验失败不会拉起 runtime，并写入 `extensions.disabled`。sidecar 启动失败在仍启用时自动重试；session Prepare 上报 `sidecar_start` 后同样禁用。配置接口只返回 schema、脱敏值和 i18n catalog，敏感字段写入时保留、读取时显示 `<configured>`。私有 `config.json` 通过 `internal/state` 原子读写，顶层 `version: 1` 是存储头，不进入 schema 或 HTTP 值；请求不能修改此头，更高版本拒读和拒写。全局 extension chip 和 goal 等 session status chip 共用同一个扩展 Modal；左侧导航列出全部已启用扩展。Extensions 设置里每个已启用扩展都有 Configure，打开并定位到同一个页面（不要求必须有 config schema）；停用的扩展没有该按钮。不在 session Info 或设置页内嵌第二份编辑器。

## 工具名称与别名

registration 和动态 registerTools 保留 sidecar 原始 ToolSpec.Name，用于 tool.execute RPC。模型 schema、目录、hooks、事件和 activeTools 保存 snake_case；调用接受 snake_case、PascalCase 和原始注册名（包含 acronym 拼写）。每个工具只发布一个 schema。注册前统一验证名称与所有 alias，冲突或内置保留名导致整个注册批次拒绝，已有能力不被覆盖。静态能力按全局注册校验，动态能力按 session 与当前静态集合校验；不同 session 同名动态工具不互相冲突。消息上下文中的 ContextOnly/完成身份不向 sidecar 或 provider 暴露。

内置工具保留名由 `internal/tool/builtin/catalog` 统一声明，别名和 schema 校验来自 `internal/tool`；sidecar 的进程组创建、启动登记和树终止来自 `internal/process`。extension 无需依赖内置工具实现即可验证注册并管理 sidecar 进程。
