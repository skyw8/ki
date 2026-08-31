# WebUI

`ki serve` 同域出页面：一个二进制，静态资源嵌在 `web/dist`，API 仍是 `/v1/*`。

浏览器打开 `http://127.0.0.1:19800/`，或经 SSH/IDE **端口转发** 打开同一端口；`scripts/run.sh` 默认监听 `0.0.0.0:19800` 时，应使用主机的 LAN IP。首次打开时在登录页输入本机 `server.json` 中的 token，WebUI 通过 `POST /v1/auth/login` 换取短期 HttpOnly session cookie；SPA HTML 和 URL 都不包含 server token。浏览器 API 写请求同时带同源 CSRF header，前端只用同域相对路径调 `/v1/*` 和 `/assets/*`，不把宿主文件路径写进 `href`，也不用系统选目录。

## 页面

- 侧栏：工作区树（创建 / 重命名 / 删除登记和会话日志、组内 `+`、pin、拖拽、每组默认 5 条「显示更多」）；常驻列表隐藏 `forkMode=tree` child，普通 `flat` fork 继续平铺。Info 页的 Tree 按钮打开固定两栏 Miller 浏览器，确认深层 child 后在其 workspace 中追加显示最终 child，紧跟可见 parent、自动滚动并聚焦；已打开的多个 child 在当前 App 生命周期内跨普通导航保留，整页重新进入或刷新后清空；tree child 菜单不提供 pin，打开后主区域切到该 session 的 chat 页面；不会在侧栏递归展开隐藏 parent 链。Miller 选目录、标题+正文搜索、未分组只给旧脏数据
- 对话：气泡、Markdown（Streamdown + remend 补全未闭合标记，`@streamdown/cjk` 处理中日韩强调；外观仍走 `.md` 设计 token，不用 Streamdown 自带的 Tailwind/shadcn 外壳）、Think、默认折叠的工具行（Read 行号 / Edit diff / Bash 终端 / IN·OUT、Inspect；Bash `description` 和路径预览可选择、可复制）、用量脚注下 copy/fork/regen、离底「回到底部」、右侧请求导航（两轮以上才出现：桌面悬停或点按右侧三条杠，浮层列出当前分支 user 请求，高亮当前视口那条，点一项滚到该气泡；不占第三栏。超长对话沿已有虚拟列表 `scrollToIndex`，很多请求时列表自己虚拟化，12 条以上可筛选）、composer（命令按钮 + 行首 `/` 打开 slash 面板，数据来自 session `commands[]`；点选只填入输入框，回车才发送；面板用不透明 `bg-layer-1`，描述单行省略。thinking 未选时显示该模型 `defaultThinking`，优先 medium 而不是列表第一项 off）。composer 下方一条会话统计：当前分支的轮/步、平均 TTFT、吞吐、缓存命中、累计输入/输出和 cost；从 `GET /v1/sessions/{id}` 的 `entries` 沿 leaf 折叠（压缩掉的 assistant 仍计入），实时 `message_end` 补上尚未入库的节点，没有新 HTTP 接口。edit 在原 user 气泡内复用 composer 原语，文本和附件一起形成当前 session 内的 sibling branch；分支用 `‹ 1 / N ›` 切换。fork 从最终 assistant entry 创建并打开新的 session 目录（沿用源 session 的 provider/model/thinking），regenerate 留在当前 session。侧栏「新会话」和工作区 `+` 把当前 composer 的模型配置发给 `POST /v1/sessions`；本浏览器 `localStorage` 记住上次选用的模型与 thinking，server 同时记住模型。冷启动没有记录时落到第一个可用模型。侧栏会话灯在本端开始 listen 时立刻变绿（不等列表刷新）
- 附件：底部和 edit composer 共用附件条与宿主机文件浏览器；选择器支持图片、纯文本/代码和 PDF.js 翻页预览，文本最多显示前 1 MiB，HTML/SVG 只作纯文本，不执行宿主内容。composer 中图片与文件使用等尺寸卡片；发送前/编辑态 composer 和已发送 user 气泡显示图片预览，缩略图可打开同一全屏查看器。浏览器文件拖到 WebUI 任意位置都会显示全屏 drop target，并进入当前 edit composer，否则进入底部 composer；剪贴板文件仍跟随获得焦点的 composer。远端预览经带鉴权的同源 `/v1/fs` Blob 响应读取，不把宿主绝对路径导航给浏览器，也不把 token 放进资源 URL。粘贴/拖入文件上传成 session 内的内容寻址副本。文件引用 host-absolute path，图片在 provider 边界读取。编辑移除只移除新消息引用，不删除工作区文件或旧分支仍引用的 blob
- 轨迹：按 turn/step 展示 SYSTEM / USER / ASSISTANT / TOOL / COMPACTED；Overview 固定为 Input / Model / Tools 三条 lane。每个 `request_header` 都保留为 request 边界和 effective prompt，但只有首次或 system/tools 发生变化时才显示 SYSTEM 记录。检查器提供 Summary / Preview / Raw，以及 system prompt、tools、context diff；跟尾在用户滚离尾部时暂停（80px 松手、16px 贴回），运行中可以翻看前面的记录；工具 description 可复制，工具耗时显示在行尾和 Summary 中
- 操作结果（slash 回执、parentId/slash 的 409、Reload/切模型/会话操作失败）走右上角 toast，portal 到 `body`，不嵌进 composer。忙时停止和发送并存：发送走 `message.busy` 默认。Enter 带内容按默认发送（queue 则入队）；Ctrl+Enter 带内容为 `delivery=steer`；Ctrl+Enter 空输入且 `queued[]` 非空则 `queueId` 提升队尾进本轮。空输入 Enter 仍 abort；Ctrl+Enter 不 abort。composer 上方列出 `queued[]`（用户）与 `extQueued[]`（扩展 FIFO，标 origin）：队尾标 Ctrl+Enter，每条有 Steer 按钮，可删。`steer_accepted` / `run_aborted` 为 live SSE（后者兼 sideband）。成功约 3.5 秒消失，错误需手动关闭。对话气泡里的模型错误、目录/附件列失败、表单 JSON 校验仍贴在原处。扩展 `enqueue` 的消息带 origin，与用户气泡可区分。
- slash：命令按钮 + 行首 `/` 打开面板，锚在整块 composer 卡片上、优先出现在输入框**上方**（不挡住 textarea）。有 session 时数据来自 session `commands[]`；尚未创建 session 时通过 workspace-scoped `/v1/commands` 预加载内置、prompt template 和 skill 命令。点选只填入输入框，回车才发送。两级：`/` 下列 `/{name}` + description（`argumentHint` 灰色写在名后，不 dump `completions`）；光标在 `/name` 或 `/name ` 且有 `completions` 时换成子命令列表。
- 扩展 UI 壳见下一节。打开会话（新会话和点开历史同一套）立刻渲染标题和气泡；`runtime.ready === false` 时锁 composer（输入、附件、`/`、发送），placeholder「正在加载扩展…」。就绪或预热失败后解锁。
- Info：本会话只读元数据、skills、extensions、slash 命令；内容右侧提供 sticky outline。每个 extension 下列出它加载的 skills、tools、slash 命令、prompt append 文件和 providers。`path` 只展示字符串，不当 `href`。有 tree parent 或 tree child 时显示 Tree 按钮；弹窗使用已有文件浏览器的 Miller 壳，左列为当前层 sibling、右列为选中 session 的直接 tree children，点击右列后推进层级但始终只有两列。Reload 清资源快照，复用全局 extension sidecar。Edit 打开设置。不在此页开关。标题按 h2 / h3 / h4 分层。
- 设置 / 选模型：各自弹窗。设置页签为「模型供应商」「Skills」「Extensions」「Message」「主题和语言」。Skills/Extensions 开关和 Message 忙碌策略写 `{KI_HOME}/toggles.json`；扩展的全局配置、goal 等 panel 和 Telegram 等表单都从统一的扩展 Modal 进入。顶栏 chip 和 Extensions 设置里每个**已启用**扩展的 Configure 打开同一页面（goal 没有 config schema 也一样）；停用的扩展没有 Configure，避免打开空的或不相关的 inspector。全局 UI 在没有 session 时也能显示；选中 session 后，session UI 覆盖同名的全局状态。无信任按钮、无原生文件选择器。选模型支持按 provider、model ID、显示名称和完整 spec 进行不区分大小写的子串 / 顺序模糊搜索。没有「设为默认」：composer 里切模型或 thinking 即记住；server 把上次选用的模型写入 `models.json`，本浏览器另存 thinking。冷启动没有记录时落到第一个可用模型。页签和主按钮与对话页同一套 tab / 主按钮样式。供应商页的外层不滚动，左侧供应商列表只显示名称，有凭据且启用的排在前面，与右侧连接、凭据、模型编辑区分别独立滚动，新增供应商和使用「编辑」打开的模型高级 JSON 都用二级弹窗。模型高级 JSON 编辑保留 `input` 和 `applyPatchToolType` 等能力字段。目录只在本机维护，不在线刷新目录。Base URL、API 协议等表单控件共享尺寸和排版；API 协议与 thinking effort 共用 ARIA combobox/listbox 组件，支持方向键、Enter、Escape，并按可用空间向上或向下展开。主题（默认浅色）和语言（中 / 英）存在本浏览器 `localStorage`
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
| 气泡 origin | user `origin` | `extension:<name>` 与用户气泡可区分 |
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

SSE（`?notifications=1`，不进 occupy 回放）：

| event | 何时 |
|---|---|
| `extension_ui_updated` | status / panel / prompt 变了 → 客户端再 GET |
| `extension_notice` | 非错误 toast（`reason` = `info` / `warn` / `error`） |
| `extension_error` | sidecar 失败；可带 Reload |
| `runtime_ready` | 该 session 打开时的 Prepare 结束（失败也算） |

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
附件、目录和会话树占满可用视口；header、可滚动 body、sticky footer 各自分层，不得让
内容画到 footer 下方。平板可以保留居中弹窗，但内部必须使用与 compact 壳一致的单列或
横向导航布局。目录和会话树的 Miller 两列在手机上纵向排列并各自滚动。

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
小于 40px。长消息、分支切换、排队操作、扩展开关等低频状态也遵守同一命中区契约。

## 构建

```bash
cd web && npm install && npm run build
go build -o ki ./cmd/ki
```

改前端后必须重新 `npm run build`，再编 Go，嵌入的才是新资源。

## Playwright

假模型打通对话和轨迹（`KI_FAKE=1` 起 `ki serve`，同域打开页面）：

```bash
cd web && npm install && npx playwright install chromium
npm run test:e2e
```

Playwright 每次 invocation 使用独立的临时状态、鉴权文件、二进制和随机 loopback 端口，
因此可与 Go e2e 或另一轮 WebUI 测试并行运行，不得复用固定 `/tmp` 状态文件。响应式矩阵
由 `e2e/responsive.spec.ts` 随 fake project 一起执行。

`go test ./e2e -run WebUI` 会先起 server，再跑同一套 Playwright（需已 `npm install` 和装好 chromium）。

长会话 / 超长消息压测不进 fake 矩阵。生成 jsonl 夹具后测 slim GET 体积与延迟（含 `fields=runtime`、`before`、`entry`）、打开 Chat/Trace 的 DOM 与 JS heap，以及向上翻页 / 截断正文补全：

```bash
cd web && npm run test:perf
```

Go 侧同一套夹具：`go test ./internal/session ./internal/server -run 'SeedView|ViewPerf|SeedTranscript' -v`；微基准 `go test ./internal/session -bench . -benchmem`。

真模型（DashScope `qwen3.7-plus`，读 `DASHSCOPE_CN_API_KEY` 或 `~/.ki/ki.toml`）：

```bash
cd web && KI_LIVE=1 npm run test:e2e:live
```

或 `go test -tags live -timeout 5m ./e2e -run LiveWebUI`。
