# WebUI

`ki serve` 同域出页面：一个二进制，静态资源嵌在 `web/dist`，API 仍是 `/v1/*`。所有文本响应（SPA 资源、`/v1` JSON）统一 gzip，`text/event-stream` 除外（SSE 逐事件 Flush，压缩缓冲会拖延流）；`assets/` 是 Vite 内容哈希产物（包括浏览器 tab 使用的 Ki SVG favicon；页面内容不重复展示该品牌图标），响应带 `Cache-Control: public, max-age=31536000, immutable`，端口转发/慢链路下刷新不再逐个回源校验；SPA HTML 仍 `no-store`。

浏览器打开 `http://127.0.0.1:19800/`，或经 SSH/IDE **端口转发** 打开同一端口；`scripts/run.sh` 默认监听 `0.0.0.0:19800` 时，应使用主机的 LAN IP。首次打开时在登录页输入本机 `server.json` 中的 token，WebUI 通过 `POST /v1/auth/login` 换取短期 HttpOnly session cookie；SPA HTML 和 URL 都不包含 server token。浏览器 API 写请求同时带同源 CSRF header，前端只用同域相对路径调 `/v1/*` 和 `/assets/*`，不把宿主文件路径写进 `href`，也不用系统选目录。

## 页面

- 侧栏：工作区树（创建 / 重命名 / 删除登记和会话日志、组内 `+`、pin、拖拽、每组默认 5 条「显示更多」）。subagent 子会话（`forkMode=tree`）常驻嵌套在父会话下方：默认折叠，点箭头展开/收起、可多级递归展开，点行打开会话（打开子会话时切到对话页；当前会话的父链会自动展开）；子行按深度缩进并有引导线，不可拖拽排序，菜单不提供 pin；孤儿 / 跨 workspace / 环这类不可信父子边退化为顶层行。普通 `flat` fork 继续平铺，`localStorage` 不持久化展开态（刷新即折叠）。标题+正文搜索只命中非 subagent 会话；选目录、未分组只给旧脏数据
- 对话：气泡、Markdown（Streamdown + remend 补全未闭合标记，`@streamdown/cjk` 处理中日韩强调；`mermaid` / `plantuml`（含 `puml` 别名）围栏统一走 `DiagramBlock`：顶部左侧 diagram/source 切换，右侧下载 PNG/SVG 与复制（复制在最右），图居中且可点击放大（全屏查看器，支持缩放条/滚轮缩放和拖拽平移）；mermaid 用 `@streamdown/mermaid` 在浏览器内渲染，plantuml 没有本地渲染器，改由 PlantUML 服务器出图（默认 `https://www.plantuml.com/plantuml`，可用 `localStorage['ki.plantumlServer']` 覆盖为自建/内网服务，失败时回退到源码 + 重试）；GFM 表格自带、工具条复制为 Markdown；外观仍走 `.md` 设计 token，不用 Streamdown 自带的 Tailwind/shadcn 外壳）、Think、默认折叠的工具行（Read 行号 / Edit diff / Bash 终端 / IN·OUT、Inspect；Bash `description` 和路径预览可选择、可复制）、用量脚注下 copy/fork/regen、离底「回到底部」、右侧请求导航（两轮以上才出现：桌面悬停或点按右侧三条杠，浮层列出当前分支 user 请求，高亮当前视口那条，点一项滚到该气泡；不占第三栏。超长对话沿已有虚拟列表 `scrollToIndex`，很多请求时列表自己虚拟化，12 条以上可筛选）。每个已结束的 turn（user → 该 turn 最后一条节点）在末尾挂一条分割线，两侧各一段 hairline、中间是该 turn 的汇总统计（`lib/model.ts` 的 `turnStats`，按 turn 最后一条节点 id 索引）：轮次、耗时（user 时间戳到该 turn 最后一条节点的时间戳，含工具执行；无时间戳时退回各步延迟之和）、步数、首步 TTFT、吞吐（步的 decode span 合并）、整轮输入/输出（含 cacheRead/cacheWrite）、缓存命中率与 cost。仍在流式或工具运行的 turn 标 `live` 而不渲染，且 run 进行中最新 turn 的汇总线一律延后到 `agent_end`（步数还在增长，避免逐个 delta 闪烁），期间由底部「正在运行…」提示；虚拟列表和非虚拟列表都在同一个 item 里追加，`jumpToId`/视口高亮仍按节点索引。窄屏（`max-width: 760px`）隐藏两侧 hairline，改成整宽分隔线 + 居中的统计。不展示 SYSTEM / system prompt 行（首次和变化都只在轨迹里看）。
composer（命令按钮 + 行首 `/` 打开 slash 面板，数据来自 session `commands[]`；点选只填入输入框，回车才发送；面板用不透明 `bg-layer-1`，描述单行省略。thinking 未选时显示该模型 `defaultThinking`，优先 medium 而不是列表第一项 off）。composer 下方一条统计：轮/步是当前分支总数，TTFT、吞吐、缓存命中、输入/输出和 cost 只取最近一条 assistant（不是整条分支的平均）；实时 `message_end` 的最新节点优先（streaming 跳过），否则从 `GET /v1/sessions/{id}` 的 `entries` 沿 leaf 取最近一条 assistant 或 compaction（压缩掉的 assistant 仍可见），没有新 HTTP 接口。edit 在原 user 位置展开为占满整列宽的编辑卡片：标题行（「编辑消息」+ Ctrl/⌘+Enter 发送 / Esc 取消提示）、自动增高并按视口封顶的输入区和附件条，文本与附件一起形成当前 session 内的 sibling branch；分支用 `‹ 1 / N ›` 切换。fork 从最终 assistant entry 创建并打开新的 session 目录（沿用源 session 的 provider/model/thinking），运行中仍可用（复制的是已落盘的完整前缀，不影响当前 run）；regenerate 留在当前 session，运行中按钮置灰、点击 toast 提示「当前对话正在运行」，避免与进行中的 loop 抢写。侧栏「新会话」和工作区 `+` 把当前 composer 的模型配置发给 `POST /v1/sessions`；本浏览器 `localStorage` 记住上次选用的模型与 thinking，server 同时记住模型。冷启动没有记录时落到第一个可用模型。侧栏会话灯在本端开始 listen 时立刻变绿（不等列表刷新），其它客户端起的 run 与删除由 push 的 `invalidate` 帧驱动；列表刷新做合并，多个 run 同时结束或连续 pin/move/delete 只发一次请求外加一次尾随刷新，响应 ETag 未变时不重建侧栏状态
- 附件：底部和 edit composer 共用附件条与宿主机文件浏览器；选择器支持图片、纯文本/代码和 PDF.js 翻页预览，文本最多显示前 1 MiB，HTML/SVG 只作纯文本，不执行宿主内容。composer 中图片用可放大的缩略图，文件用带名称与类型/大小的卡片；发送前/编辑态 composer 和已发送 user 气泡显示图片预览，缩略图可打开同一全屏查看器。浏览器文件拖到 WebUI 任意位置都会显示全屏 drop target，并进入当前 edit composer，否则进入底部 composer；剪贴板文件仍跟随获得焦点的 composer。远端预览经带鉴权的同源 `/v1/fs` Blob 响应读取，不把宿主绝对路径导航给浏览器，也不把 token 放进资源 URL。粘贴/拖入文件上传成 session 内的内容寻址副本。文件引用 host-absolute path，图片在 provider 边界读取。编辑移除只移除新消息引用，不删除工作区文件或旧分支仍引用的 blob
- 轨迹：按 turn/step 展示 SYSTEM / USER / ASSISTANT / TOOL / COMPACTED；Overview 固定为 Input / Model / Tools 三条 lane。每个 `request_header` 都保留为 request 边界和 effective prompt，但只有首次或 system/tools 发生变化时才显示 SYSTEM 记录。检查器提供 Summary / Preview / Raw，以及 system prompt、tools、context diff；跟尾在用户滚离尾部时暂停（80px 松手、16px 贴回），运行中可以翻看前面的记录；工具 description 可复制，工具耗时显示在行尾和 Summary 中
- 操作结果（slash 回执、parentId/slash 的 409、Reload/切模型/会话操作失败）走右上角 toast，portal 到 `body`，不嵌进 composer。提交（底部发送、edit 发送、Ctrl/⌘+Enter）立即清空输入框与附件条并关闭 edit 卡片，不等服务端返回；`/compact` 这类同步 slash 命令在压缩进行中不会残留命令文本。仅在请求被拒绝（409、网络/校验失败）时恢复提交前的草稿，若用户期间已输入新内容则不覆盖。忙时停止和发送并存：发送走 `message.busy` 默认。Enter 带内容按默认发送（queue 则入队）；Ctrl+Enter 带内容为 `delivery=steer`；Ctrl+Enter 空输入且 `queued[]` 非空则 `queueId` 提升队尾进本轮。空输入 Enter 仍 abort；Ctrl+Enter 不 abort。composer 上方列出 `queued[]`（用户）与 `extQueued[]`（扩展 FIFO，标 origin）：队尾标 Ctrl+Enter，每条有 Steer 按钮，可删。`steer_accepted` / `run_aborted` 为 live SSE（后者兼 sideband）。成功约 3.5 秒消失，错误需手动关闭。对话气泡里的模型错误、目录/附件列失败、表单 JSON 校验仍贴在原处。扩展 `enqueue` 与 subagent completion notification 的消息带 origin，与用户气泡可区分；非人类 user 气泡使用虚线框。
- slash：命令按钮 + 行首 `/` 打开面板，锚在整块 composer 卡片上、优先出现在输入框**上方**（不挡住 textarea）。有 session 时数据来自 session `commands[]`；尚未创建 session 时通过 workspace-scoped `/v1/commands` 预加载内置、prompt template 和 skill 命令。点选只填入输入框，不覆盖已有正文；已有正文时命令默认插入开头。面板中 Tab 补全当前高亮项（Shift+Tab 反向移动），首次回车只选择/关闭命令，面板关闭后再次回车才发送。两级：`/` 下列 `/{name}` + description（`argumentHint` 灰色写在名后，不 dump `completions`）；光标在 `/name` 或 `/name ` 且有 `completions` 时换成子命令列表。
- 会话历史里的压缩状态：`compaction_start` 在消息流末尾插入一条黄色「正在压缩上下文…」（带转圈）的行，`compaction_end` 原地变成「上下文已压缩 · ~N tokens」（`reason=empty` 显示「无需压缩」，失败显示红色）。手动 `/compact` 是同步请求，没有 run stream，进度由 push 流（`GET /v1/events`，session sideband）推送；压缩完成后重新读取 session，用 jsonl 里的 `compaction` entry（含 `tokensBefore`）替换实时行。
- 扩展 UI 壳见下一节。打开会话（新会话和点开历史同一套）立刻渲染标题和气泡；`runtime.ready === false` 时锁 composer（输入、附件、`/`、发送），placeholder「正在加载扩展…」。就绪或预热失败后解锁。
- Info：本会话只读元数据、skills、extensions、slash 命令；内容右侧提供 sticky outline。每个 extension 下列出它加载的 skills、tools、slash 命令、prompt append 文件和 providers。`path` 只展示字符串，不当 `href`。来源 session（`parentSessionId`）渲染为可点链接，点击切到该 parent 会话。subagent 子会话在侧栏内联展开，本页不再有独立的 Tree 浏览器。Reload 清资源快照，复用全局 extension sidecar。Edit 打开设置。不在此页开关。标题按 h2 / h3 / h4 分层。
- 设置 / 选模型：各自弹窗。设置页签为「模型供应商」「Skills」「Tools」「Extensions」「Message」「通知」「主题和语言」。通知页开关会话完成通知：开启时在用户手势里向浏览器申请 `Notification` 权限，权限被拒或非安全上下文（局域网 http）时禁用并提示；开启状态存本浏览器 `localStorage['ki-notify']`。完成感知不搭在选中会话的 run 流上（切会话就断了），也不为每个 running session 单开连接：WebUI 每个 tab 只有一条 `GET /v1/events` push 流（server 在 run 结束时把带 `sessionId` 的 `agent_end` 也投给它，见 [events.md](events.md)），所以切到别的 session 或别的程序时，先前的 run 跑完仍会通知。发送条件是「没有聚焦的 tab 正在看这个 session」：聚焦 tab 把当前 session 写进跨 tab 共享的 `localStorage['ki-focused-session']`（含 tab 标识，失焦/隐藏/关闭时清掉自己那条），完成时读取它，只有等于完成 session 才静默——切到别的 session（值不同）、切到别的程序（无值）都会通知；多开的 ki tab 共享同一份标记，因此前台 tab 正在看的那个 session 完成时其它 tab 也不会重复提醒，且多 tab 同时观察到同一次完成时按 session 去重（Notification `tag`），不会堆叠。subagent session（`forkMode=tree`）一律不通知：它挂在 parent 下、由 parent 的 run 决定通知，逐个子会话提醒是用户无法处理的噪音。用户主动 abort 的 run（先收到 `run_aborted`）不提醒。通知标题取会话标题，正文为「`{cwd}` · 会话已完成」（标题相同的短 prompt 用工作目录区分）；点击聚焦窗口。只覆盖本浏览器已知 `running` 的 session（本 tab 发起的 run、以及经 push 刷新后见过的 running 行；push 流对每个 `agent_end` 都会到达，客户端用这个已知集合过滤，避免给 CLI 起的 run 弹通知）。**端口转发注意**：`Notification` 是安全上下文 API（和 `navigator.clipboard` 同一条规则，见下文），`http://localhost:<port>` / `http://127.0.0.1:<port>` 的转发（ssh -L、IDE/Codespaces、kubectl port-forward）可用；用 `http://<主机名或 LAN/Tailscale IP>:<port>` 明文访问时浏览器根本不暴露该接口，此时通知页显示「非安全上下文」并提示改用 localhost 转发或 HTTPS（Tailscale serve / 反向代理），而不是静默失败——`ki serve` 自身只跑 HTTP，HTTPS 由隧道/代理提供。另外 push 由单条 `GET /v1/events` 承载，连接数不随同时运行的会话数增长；明文 HTTP/1.1 下仍会与 run 流、静态资源共用同源 6 连接，高延迟链路建议走 HTTPS/HTTP2。页里还有「发送测试通知」按钮，用于验证权限和系统通知中心是否可见。Tools/Skills/Extensions 开关和 Message 忙碌策略写 `{KI_HOME}/toggles.json`；Tools 只列出当前 session/model 可用的内置工具，扩展工具不在此处管理。扩展的全局配置、goal 等 panel 和 Telegram 等表单都从统一的扩展 Modal 进入。扩展表单里凡是有选模型控件的地方（Telegram 回复模型、deep-web-search 的 Codex 模型和摘要模型），都在同一行并排一个 thinking effort 下拉框，选项取自所选模型支持的思考等级，样式与模型按钮等高；模型不支持思考时只显示模型按钮，effort 留空则跟随模型默认。顶栏 chip 和 Extensions 设置里每个**已启用**扩展的 Configure 打开同一页面（goal 没有 config schema 也一样）；停用的扩展没有 Configure，避免打开空的或不相关的 inspector。全局 UI 在没有 session 时也能显示；选中 session 后，session UI 覆盖同名的全局状态。无信任按钮、无原生文件选择器。选模型支持按 provider、model ID、显示名称和完整 spec 进行不区分大小写的子串 / 顺序模糊搜索。没有「设为默认」：composer 里切模型或 thinking 即记住；server 把上次选用的模型写入 `models.json`，本浏览器另存 thinking。冷启动没有记录时落到第一个可用模型。页签和主按钮与对话页同一套 tab / 主按钮样式。供应商页的外层不滚动，左侧供应商列表只显示名称，有凭据且启用的排在前面，与右侧连接、凭据、模型编辑区分别独立滚动，新增供应商和使用「编辑」打开的模型高级 JSON 都用二级弹窗。模型高级 JSON 编辑保留 `input` 和 `applyPatchToolType` 等能力字段。目录只在本机维护，不在线刷新目录。Base URL、API 协议等表单控件共享尺寸和排版；API 协议与 thinking effort 共用 ARIA combobox/listbox 组件，支持方向键、Enter、Escape，并按可用空间向上或向下展开。主题（默认浅色）和语言（中 / 英）存在本浏览器 `localStorage`
- 扩展 OAuth：供应商页对 `auth.type=oauth` 的 provider 显示 Browser login、Device code login 和 Logout；登录进度通过同源 `/v1/providers/{id}/auth/{requestId}` 轮询，页面只显示授权 URL、设备码和脱敏错误。Browser flow 还允许粘贴 redirect URL/code，适用于端口转发；不会在页面中打开外部窗口，也不会要求用户填 access token。

数据来自 session jsonl 和本次 run 的 SSE。`GET /v1/sessions/{id}` 默认给 leaf 尾部的 slim entries 加整棵树 `index`；Chat/Trace 在条数多时用虚拟列表只画视口附近的行，时间线按权重绝对定位并在过密时合并。向上滚到顶沿 `before` 再取更早的 leaf；Inspect / 展开截断工具行时用 `entry`/`entries` 补全文。conversation 和 trajectory 根据 `leafId` 沿 `parentId` 只渲染 active path，`index` 保留 sibling。`message_end` SSE 带持久化后的 `entryId`，所以刚完成的消息可以立即 edit/fork。工作区见 [workspace.md](workspace.md)。Sidecar 协议见 [extension.md](extension.md)。

## 扩展 UI 壳

WebUI **不加载扩展 JS**，不 `window.open`，不按扩展名写死控件。每个扩展只投一份投影，壳按同一套布局渲染。goal 和以后别的包用同一组接口。扩展文案由扩展包自己的 `extension.json -> i18n.resources` 提供，Host 只读取、校验并随 catalog 转发；WebUI 只负责按当前浏览器语言解析通用 `UIText`，不认识任何扩展 key。

### 面上有什么

| 面 | 数据 | 行为 |
|---|---|---|
| 全局 extension chip | `/v1/extensions` 中启用且有 global UI 或配置的扩展 | 与当前 session 无关，初始页面也显示在 `title-row` 右侧；点击打开统一扩展 Modal，并定位到该扩展。runtime 状态用于 chip tone。Extensions 设置里每个已启用扩展的 Configure 打开同一 Modal。 |
| 顶栏 status chip | `status` | 只在 `title-row` 右侧。无 `status.text` 则无 chip。tone：`info` / `active` / `success` / `warning` / `error`。按 tone（error → warning → active → success → info）再按扩展名排；最多 4 颗全展示，超过则留 3 颗加 `+N`。右侧始终有展开钮。窄屏藏单颗 status chip，只留「扩展 · N」。 |
| 统一扩展 Modal | global `ui` + session `extensionUi` + 全局 config | 点全局 chip、status chip、展开钮 **或** Extensions 设置的 Configure 打开同一 Modal。左侧导航列出全部已启用扩展（窄屏改成顶部横滑）；右侧显示当前扩展的 global/session UI 或配置。global panel 只读；选中 session 后，同名 session UI 覆盖 global UI。关 Modal **不**卸 chip，也不 `clearPanel` |
| 确认 / 选择 | `prompt` | 叠在详情 Modal 上。120s 或 abort = 取消 |
| slash | `commands[]` | 见上一节。扩展命令 `source=extension`，`argumentHint` + `completions` |
| 气泡 origin | user `origin` | `extension:<name>`、`agent:<task-id>` 与用户气泡可区分；非人类来源使用虚线框 |
| 就绪锁 | `runtime.ready` | 未就绪不能打字；避免 `/goal` 尚未注册就 404 |

不要在 tabs 下、composer 上再做第二条 status 横条。全局 chip 不依赖 session；global config、global UI 与 session 的 `status` / `panel` 在同一个扩展 Modal 中呈现；这些 UI 投影 **不进 jsonl**。Reload 后 sidecar 按自己的状态重新发布 global/session UI。

### 投影

`GET /v1/extensions` 的每个 extension item 可带 global `ui`；`GET /v1/sessions/{id}` 的 `extensionUi[]` 是 session 投影（每个扩展一条）：

```json
{
  "extension": "goal",
  "status": { "key": "goal", "text": "Goal · active", "tone": "active" },
  "panel": { },
  "prompt": null
}
```

每个打开的 tab 只保持**一条** push 流 `GET /v1/events`（见 [events.md](events.md)），它同时承载失效信号和 session sideband；旧的「每个 running session 一条轻量 SSE」因此消失，明文 HTTP/1.1 的同源 6 连接限制不再随会话数增长。tab 变可见时（`visibilitychange`）额外做一次全量刷新，兜住休眠或代理超时后连接尚未报错的情况（列表走 ETag，空闲 tab 只花一个 304）。该流推两类帧：

| event | 何时 | 客户端动作 |
|---|---|---|
| `ready` | 订阅建立（含重连） | 重取全部：列表 + workspaces + extensions + 当前会话的 runtime；流不回放，重连靠这一次全量刷新追平 |
| `invalidate` + `scope` | `sessions` / `workspaces` / `providers` / `extensions` 变了 | 重取对应 REST（`GET /v1/sessions` 带 ETag，未变为 304）；`sessions` 还会把「当前打开的会话」与列表对齐：变 running 就接上 run 流，已不存在就退出该会话。`extensions` 额外重读当前会话的 `fields=runtime`（`GET /v1/sessions/{id}` 里重新预热扩展视图，取代旧的 server 端 rewarmWatchers） |
| session sideband（带 `sessionId`） | 见下表 | 只处理 `sessionId` 等于当前会话的事件；`agent_end` / `run_aborted` 例外，用于后台完成的系统通知 |

SSE（带 `sessionId`，不进 occupy 回放）：

| event | 何时 |
|---|---|
| `extension_ui_updated` | status / panel / prompt 变了 → 客户端再 GET |
| `extension_notice` | 非错误 toast（`reason` = `info` / `warn` / `error`） |
| `extension_error` | sidecar 失败；可带 Reload |
| `runtime_ready` | 该 session 打开时的 Prepare 结束（失败也算） |
| `compaction_start` / `compaction_end` | 手动 `/compact` 的进度（同步请求，没有 run 流） |
| `queue_changed` | 队列变化 |
| `run_aborted` / `agent_end` | 中止 / run 结束；`agent_end` 不带 `messages`（run 的整份消息只回放给持有该 run SSE 的客户端） |

用户点面板：`POST /v1/sessions/{id}/extension-ui`

| `kind` | body | Host → sidecar |
|---|---|---|
| `action` | `{ extension, value }` | `ui.action` `{ id: value }` |
| `submit` | `{ extension, fields }` | `ui.submit` `{ fields }` |
| `confirm` | `{ extension, ok }` | 解开 `ui.confirm` |
| `select` | `{ extension, ok, value }` | 解开 `ui.select` |

### Sidecar → Host（写投影）

| method | params | 壳怎么用 |
|---|---|---|
| `ui.setStatus` | `{ key, text, tone }` | 顶栏 chip。`text` 空则去掉该扩展 chip |
| `ui.setPanel` | 见下表 | 详情 Modal 的内容 |
| `ui.clearPanel` | `{}` | 清 panel，chip 仍在（若还有 status） |
| `ui.confirm` | `{ title, message }` | 是/否；result `{ ok }`；**120s** |
| `ui.select` | `{ title, options[] }` | 点一项；result `{ ok, value }`；**120s** |

Host **不解析** panel 里的业务字段。扩展自己决定列哪些 action、何时 `disabled`。

### `ui.setPanel`

```json
{
  "title": "Goal",
  "sections": [
    {
      "heading": "Details",
      "items": [
        { "label": "Status", "value": "active" },
        { "label": "Turns", "value": "1 / 25" }
      ]
    }
  ],
  "fields": [
    { "id": "objective", "label": "Objective", "type": "textarea", "value": "说你好" }
  ],
  "submitLabel": "Update",
  "actions": [
    { "id": "pause", "label": "Pause" },
    { "id": "resume", "label": "Resume", "disabled": true, "title": "Nothing to resume" },
    { "id": "clear", "label": "Clear", "style": "danger" }
  ]
}
```

| 字段 | 壳 |
|---|---|
| `title` | Modal 标题 |
| `summary` | 只读摘要卡片。和 `fields` 里已有的值不要重复；目标正文放 field，空状态才用 summary 提示 |
| `sections[].heading` | 小节标题 |
| `sections[].items[]` | `{label,value}` 属性表；`label` 也认 `key` / `name` |
| `sections[].kv` | 对象展成属性表 |
| `sections[].markdown` | Markdown（无 items/kv 时） |
| `sections[].text` | 纯文本 |
| `fields[]` | 可编辑。`type`：省略=单行，`textarea`，或 `options[]`=select |
| `submitLabel` | 有 fields 时主按钮文案，缺省「提交」。点了走 `ui.submit` |
| `actions[].id` | 点了走 `ui.action`，只回这个 id |
| `actions[].label` | 按钮文字 |
| `actions[].style` | `danger` / `primary`；其余次按钮 |
| `actions[].disabled` | 显示但不可点 |
| `actions[].title` | tooltip（为何 disabled） |

渲染顺序：status chip → summary → sections → fields → 底栏 actions + submit。

扩展需要本地化 WebUI 固定文案时，可在 `title`、`summary`、`status.text`、section 的
`heading`/`label`/`value`、field label、action label/title 和 `submitLabel` 中发送原始字符串，
或发送扩展自有的 `UIText`：

```json
{ "key": "status.connected", "params": { "count": 2 }, "fallback": "Connected" }
```

壳按以下顺序查找翻译：当前语言、当前语言的 regional/base 变体、扩展
`defaultLocale`、`en`，最后使用 `fallback` 或 key。`params` 使用 `{name}` 形式插值。
普通用户输入、业务正文、field 的提交值和 `options[]` 的提交值仍直接发送原文；`options[]`
是业务值而不是 host 文案。扩展没有必要绑定某一个浏览器语言，缺失 locale/key 只会触发上述
回退。

扩展配置页的扩展描述同样使用 `manifest.description` key；Telegram、deep-web-search 等
自定义配置表单通过各自的 i18n catalog 解析字段、提示和选项文案。Host 通用的关闭、提交、
空状态等壳文案仍属于 WebUI 自己的 `i18n.tsx`，不得写入 `ext.<extension>` 或
`cfg.<extension>` host key。

### Sidecar ← Host（用户点了）

| method | params |
|---|---|
| `ui.action` | `{ id }` 对应 `actions[].id` |
| `ui.submit` | `{ fields }` 当前表单（id → 值） |

需要二次确认用 `ui.confirm`（例如 Clear），不要在 SPA 里写死文案。

slash 子命令不必和按钮一一同名；面板是控制面，slash 是输入面。goal 的做法：pause / resume / clear 做 action（不能用的 `disabled`），status 用 sections，edit/start 用 field + submit。

## 响应式与交互契约

WebUI 以动态视口为边界，不假设固定桌面尺寸。桌面保留可折叠侧栏；宽度不超过
900px 时改为带 scrim 的抽屉导航，主区常驻打开按钮，抽屉关闭时自身为 `inert`，打开时
主区为 `inert`。同一断点内顶栏只显示一颗「扩展 · N」聚合入口，扩展、Provider 和设置
内部导航改为横向可滚动列表，不能用 0 宽网格轨道隐藏仍在绘制的侧栏。

手机宽度（不超过 760px）以及不超过 540px 高的横屏使用完整移动布局：设置、扩展、
附件和目录占满可用视口；header、可滚动 body、sticky footer 各自分层，不得让
内容画到 footer 下方。平板可以保留居中弹窗，但内部必须使用与 compact 壳一致的单列或
横向导航布局。目录的 Miller 两列在手机上纵向排列并各自滚动。
侧栏的 subagent 分支在触控端把展开箭头和行菜单放大到至少 40px，缩进引导线随箭头尺寸
一起缩放，保证多级嵌套仍可点。

Provider 新建与高级模型弹层固定头尾、只滚动字段区。轨迹在触控端提供可见的缩小、复位、
放大按钮；矮横屏保持单行工具栏，为记录列表保留可点击高度，选中后再钻取详情。

页面高度使用 `dvh`，浮动菜单同时读取 `visualViewport`，四边 padding 合并
`safe-area-inset-*`。移动端可编辑的 input、textarea、select 字号至少 16px，避免 iOS
聚焦缩放。主要触控目标至少 40px，列表行和关键导航目标至少 44px；关闭的抽屉和弹层
不能被 Tab 聚焦。底部 composer 的 textarea 不显示品牌色 focus outline（不影响布局），
其他可交互控件保留键盘焦点提示。所有 dialog 均需 `aria-modal`、Escape 只关闭栈顶、Tab 焦点圈定和关闭
后焦点恢复；嵌套 dialog 打开时，下层 dialog 同时设为 `inert` 和 `aria-hidden`，关闭后精确
恢复其先前属性。命令面板的 combobox 必须显式关联 listbox，且在焦点移交给 Select 或
dialog 时立即释放键盘；侧栏操作菜单使用 menu/menuitem 语义，支持方向键、Tab、Escape，
并在关闭后把焦点还给触发按钮。抽屉触发目录或设置 dialog 时，焦点直接交给 dialog，关闭
后回到主区的抽屉按钮，不得回到已变为 inert 的侧栏节点。Settings 和扩展 Details/Config
使用完整 tablist/tab/tabpanel 关系、roving tabindex 和方向键/Home/End 导航，inactive panel
保留关联节点但必须 hidden。`prefers-reduced-motion` 下关闭抽屉和控件过渡。

响应式回归覆盖 320×568、390×844、844×390、768/820 平板、1024×768 和桌面，逐页
检查登录、Hero、Chat、Trajectory、Info、Settings、Provider、扩展配置、模型、命令菜单、
选择菜单、附件、目录和会话树；根页面不得横向溢出，header、composer、dialog/footer 必须
保持在可视区内。各 profile 会检查可见交互控件具有可访问名称；触控 profile 进一步扫描
按钮、菜单项、链接、输入、select、textarea 以及 checkbox/radio 的 label 命中区，不允许
小于 40px（Chromium 的布局盒子按 1/64px 舍入，所以断言用 39.95px 作为下限，避免 40px 控件量到 39.999999 时误报）。dialog 内的 action 按钮同样固定 40px 最小命中区，不随 label 文本宽度收缩。
长消息、分支切换、排队操作、扩展开关等低频状态也遵守同一命中区契约。

复制按钮统一走 `web/src/lib/clipboard.ts` 的 `copyText()`：浏览器只在安全上下文
（HTTPS，或 `localhost`/`127.0.0.1` 上的 HTTP）暴露 `navigator.clipboard`，而 WebUI
经常通过主机名以明文 HTTP 访问（Tailscale/LAN 地址等），此时该 API 不存在。`copyText()`
在非安全上下文回落到 `document.execCommand('copy')`，保证任何访问方式下复制都可用。

## 构建

```bash
cd web && bun install && bun run build
cd .. && go build -tags embed -o ki ./cmd/ki
```

`web/dist` 是构建产物、不进 git（见 `.gitignore`）。必须用 `-tags embed` 才能把它嵌进二进制：
不带该 tag 时 `web/embed.go` 不参与编译，改由 `web/stub.go` 提供空 FS，`ki serve` 对 `/`
返回 503 提示。改前端后必须重新 `bun run build` 再编 Go。依赖用 bun 管理
（`web/bun.lock`）。`scripts/run.sh` 用输入 hash 判断前端有没有变：覆盖 `src/`、`public/`、
`index.html`、`vite.config.ts`、`tsconfig.json`、`package.json`、`bun.lock` 和 bun 版本；
没变就跳过 `bun run build`，直接复用 `web/dist`（指纹存在
`web/node_modules/.cache/ki/web-dist-hash`，不放进 dist，因为 `//go:embed all:dist` 会把
dotfile 一起打进二进制）。`--force-web` 强制重建。跳过与否都始终带 `-tags embed`，避免把
旧前端资源嵌入新的二进制。

打包器是 **Vite 8**，它默认用 **Rolldown**（`vite` 的依赖里是 `rolldown`，不再依赖 Rollup），
config、插件和 `vite` / `vite build` 命令都照旧。同一份代码，原 Vite 6（Rollup）的
`vite build` 约 5.7s，Vite 8 约 1.4s（`@vitejs/plugin-react` 用 6.x，其 peer 要求
`vite ^8`）。构建快了以后就不必再为重型依赖（mermaid）做预打包，`DiagramBlock` 直接
`import('@streamdown/mermaid')`。升级 Vite 或换插件后都要用 `bun run test:e2e` 和
`bun run test:perf` 验证。

## Playwright

假模型打通对话和轨迹（每个并行单元各起一个隔离的 `ki serve`）：

```bash
cd web && bun install && bunx playwright install chromium
bun run test:e2e          # 并行 runner（默认）
bun run test:e2e:serial   # 单进程串行，便于定位单个失败
```

`bun run test:e2e` 由 `web/scripts/e2e-parallel.ts` 驱动：先 `playwright test --list`
自动枚举 `--project=fake` 的用例，再按文件声明的方式拆成独立进程：

- `test.describe.configure({ mode: 'parallel' })` —— **一个用例一个进程**。这是文件在声明
  其用例彼此独立；加这行前必须先逐个用例单独跑通（`-g` 精确匹配单条标题）验证。
- `mode: 'serial'` —— 整文件保持单进程，顺序叙事不得拆散。
- 其余：超过 `KI_E2E_SPLIT` 个用例、且全部挂在顶层 describe 下的大文件按 describe 拆分
  （`responsive.spec.ts` 的历史行为），否则整文件一个进程。

默认并发 `min(CPU, 32)`，每个进程独立端口。每次 invocation 使用独立的临时状态、鉴权文件、
二进制和随机 loopback 端口，因此互不干扰，可与 Go e2e 并行运行，不得复用固定 `/tmp` 状态文件。
`freePort()` 的探测 socket 会先关闭再由 `ki serve` 绑定，两者之间可能与另一个单元抢同一端口；
runner 仅在"一个用例都没跑 + `address already in use`"时换端口重试一次，因此不会掩盖真实失败。

为保证不牺牲覆盖，runner 记录 `--list` 的期望用例数，跑完按文件与实际执行数核对：任何用例被
丢弃或重复执行（例如标题重复导致 `-g` 多匹配）都会让整个 run 失败退出。可用 `KI_E2E_JOBS` 调
并发、`KI_E2E_SPLIT` 调拆分阈值、`KI_BIN` 复用已构建的二进制、`KI_SKIP_WEB_BUILD` 禁止自动
构建前端。

用例需要"先锁后放"这类瞬态时，不要靠固定 sleep 撞窗口（高并发下会偶发）：`sidecar` fixture
支持 `KI_INIT_WAIT_FILE`，测试可以先断言锁定态、再写文件放行。

`go test ./e2e -run WebUI` 复用同一个 runner，并用 `KI_BIN` 指向 Go 构建的二进制，因此每个
spec 仍然打到 Go 编出来的 SPA（需已 `bun install` 和装好 chromium）。

长会话 / 超长消息压测不进 fake 矩阵。生成 jsonl 夹具后测 slim GET 体积与延迟（含 `fields=runtime`、`before`、`entry`）、打开 Chat/Trace 的 DOM 与 JS heap，以及向上翻页 / 截断正文补全：

```bash
cd web && bun run test:perf
```

Go 侧同一套夹具：`go test ./internal/session ./internal/server -run 'SeedView|ViewPerf|SeedTranscript' -v`；微基准 `go test ./internal/session -bench . -benchmem`。

真模型（DashScope `qwen3.7-plus`，读 `DASHSCOPE_CN_API_KEY` 或 `~/.ki/ki.toml`）：

```bash
cd web && KI_LIVE=1 bun run test:e2e:live
```

或 `go test -tags live -timeout 5m ./e2e -run LiveWebUI`。
