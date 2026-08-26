# WebUI

`ki serve` 同域出页面：一个二进制，静态资源嵌在 `web/dist`，API 仍是 `/v1/*`。

浏览器打开 `http://127.0.0.1:19800/`，或经 SSH/IDE **端口转发** 打开同一端口。token 写进 `index.html`，前端只用同域相对路径调 `/v1/*` 和 `/assets/*`，不把宿主文件路径写进 `href`，也不用系统选目录。

## 页面

- 侧栏：工作区树（创建 / 重命名 / 删除登记和会话日志、组内 `+`、pin、拖拽、每组默认 5 条「显示更多」）；常驻列表隐藏 `forkMode=tree` child，普通 `flat` fork 继续平铺。Info 页的 Tree 按钮打开固定两栏 Miller 浏览器，确认深层 child 后在其 workspace 中追加显示最终 child，紧跟可见 parent、自动滚动并聚焦；已打开的多个 child 在当前 App 生命周期内跨普通导航保留，整页重新进入或刷新后清空；tree child 菜单不提供 pin，打开后主区域切到该 session 的 chat 页面；不会在侧栏递归展开隐藏 parent 链。Miller 选目录、标题+正文搜索、未分组只给旧脏数据
- 对话：气泡、Markdown（Streamdown + remend 补全未闭合标记，`@streamdown/cjk` 处理中日韩强调；外观仍走 `.md` 设计 token，不用 Streamdown 自带的 Tailwind/shadcn 外壳）、Think、默认折叠的工具行（Read 行号 / Edit diff / Bash 终端 / IN·OUT、Inspect；Bash `description` 和路径预览可选择、可复制）、用量脚注下 copy/fork/regen、离底「回到底部」、composer（命令按钮 + 行首 `/` 打开 slash 面板，数据来自 session `commands[]`；点选只填入输入框，回车才发送；面板用不透明 `bg-layer-1`，描述单行省略。thinking 未选时显示该模型 `defaultThinking`，优先 medium 而不是列表第一项 off）。composer 下方一条会话统计：当前分支的轮/步、平均 TTFT、吞吐、缓存命中、累计输入/输出和 cost；从 `GET /v1/sessions/{id}` 的 `entries` 沿 leaf 折叠（压缩掉的 assistant 仍计入），实时 `message_end` 补上尚未入库的节点，没有新 HTTP 接口。edit 在原 user 气泡内复用 composer 原语，文本和附件一起形成当前 session 内的 sibling branch；分支用 `‹ 1 / N ›` 切换。fork 从最终 assistant entry 创建并打开新的 session 目录（沿用源 session 的 provider/model/thinking），regenerate 留在当前 session。侧栏「新会话」和工作区 `+` 把当前 composer 的模型配置发给 `POST /v1/sessions`；本浏览器 `localStorage` 记住上次选用的模型与 thinking，server 同时记住模型。冷启动没有记录时落到第一个可用模型。侧栏会话灯在本端开始 listen 时立刻变绿（不等列表刷新）
- 附件：底部和 edit composer 共用附件条与宿主机文件浏览器；选择器支持图片、纯文本/代码和 PDF.js 翻页预览，文本最多显示前 1 MiB，HTML/SVG 只作纯文本，不执行宿主内容。composer 中图片与文件使用等尺寸卡片；发送前/编辑态 composer 和已发送 user 气泡显示图片预览，缩略图可打开同一全屏查看器。浏览器文件拖到 WebUI 任意位置都会显示全屏 drop target，并进入当前 edit composer，否则进入底部 composer；剪贴板文件仍跟随获得焦点的 composer。远端预览经带鉴权的同源 `/v1/fs` Blob 响应读取，不把宿主绝对路径导航给浏览器，也不把 token 放进资源 URL。粘贴/拖入文件上传成 session 内的内容寻址副本。文件引用 host-absolute path，图片在 provider 边界读取。编辑移除只移除新消息引用，不删除工作区文件或旧分支仍引用的 blob
- 轨迹：SYSTEM / USER / ASSISTANT / TOOL / COMPACTED 各有色标；检查器 Summary / Preview / Raw。跟尾在用户滚离尾部时暂停（80px 松手、16px 贴回），运行中可以翻看前面的记录；工具 description 可复制
- 操作结果（slash 回执、parentId/slash 的 409、Reload/切模型/会话操作失败）走右上角 toast，portal 到 `body`，不嵌进 composer。忙时停止和发送并存：发送走 `message.busy` 默认。Enter 带内容按默认发送（queue 则入队）；Ctrl+Enter 带内容为 `delivery=steer`；Ctrl+Enter 空输入且 `queued[]` 非空则 `queueId` 提升队尾进本轮。空输入 Enter 仍 abort；Ctrl+Enter 不 abort。composer 上方列出 `queued[]`（用户）与 `extQueued[]`（扩展 FIFO，标 origin）：队尾标 Ctrl+Enter，每条有 Steer 按钮，可删。`steer_accepted` / `run_aborted` 为 live SSE（后者兼 sideband）。成功约 3.5 秒消失，错误需手动关闭。对话气泡里的模型错误、目录/附件列失败、表单 JSON 校验仍贴在原处。扩展 `enqueue` 的消息带 origin，与用户气泡可区分。
- slash：命令按钮 + 行首 `/` 打开面板，锚在整块 composer 卡片上、优先出现在输入框**上方**（不挡住 textarea）。数据来自 session `commands[]`；点选只填入输入框，回车才发送。两级：`/` 下列 `/{name}` + description（`argumentHint` 灰色写在名后，不 dump `completions`）；光标在 `/name` 或 `/name ` 且有 `completions` 时换成子命令列表。
- 扩展 UI 壳见下一节。打开会话（新会话和点开历史同一套）立刻渲染标题和气泡；`runtime.ready === false` 时锁 composer（输入、附件、`/`、发送），placeholder「正在加载扩展和 MCP…」。就绪或预热失败后解锁。
- Info：本会话只读元数据、skills、MCP（含已缓存工具）、extensions、slash 命令；内容右侧提供 sticky outline。`path` 只展示字符串，不当 `href`。有 tree parent 或 tree child 时显示 Tree 按钮；弹窗使用已有文件浏览器的 Miller 壳，左列为当前层 sibling、右列为选中 session 的直接 tree children，点击右列后推进层级但始终只有两列。Reload 清资源快照并关闭 MCP 与 extension sidecar。Edit 打开设置。不在此页开关。
- 设置 / 选模型：各自弹窗。设置页签为「模型供应商」「Skills」「MCP」「Extensions」「Message」「主题和语言」。Skills/MCP/Extensions 开关和 Message 忙碌策略写 `{KI_HOME}/toggles.json`，对应页可 Reload。无信任按钮、无原生文件选择器。选模型支持按 provider、model ID、显示名称和完整 spec 进行不区分大小写的子串 / 顺序模糊搜索。没有「设为默认」：composer 里切模型或 thinking 即记住；server 把上次选用的模型写入 `models.json`，本浏览器另存 thinking。冷启动没有记录时落到第一个可用模型。页签和主按钮与对话页同一套 tab / 主按钮样式。供应商页的外层不滚动，左侧供应商列表只显示名称，与右侧连接、凭据、模型编辑区分别独立滚动，新增供应商和使用「编辑」打开的模型高级 JSON 都用二级弹窗。模型高级 JSON 编辑保留 `input` 和 `applyPatchToolType` 等能力字段。目录只在本机维护，不在线刷新。Base URL、API 协议等表单控件共享尺寸和排版；API 协议与 thinking effort 共用 ARIA combobox/listbox 组件，支持方向键、Enter、Escape，并按可用空间向上或向下展开。主题（默认浅色）和语言（中 / 英）存在本浏览器 `localStorage`
- 扩展 OAuth：供应商页对 `auth.type=oauth` 的 provider 显示 Browser login、Device code login 和 Logout；登录进度通过同源 `/v1/providers/{id}/auth/{requestId}` 轮询，页面只显示授权 URL、设备码和脱敏错误。Browser flow 还允许粘贴 redirect URL/code，适用于端口转发；不会在页面中打开外部窗口，也不会要求用户填 access token。

数据来自 session jsonl 和本次 run 的 SSE。conversation 和 trajectory 根据 `leafId` 沿 `parentId` 只渲染 active path，全部 entries 保留用于 sibling 索引。`message_end` SSE 带持久化后的 `entryId`，所以刚完成的消息可以立即 edit/fork。工作区见 [workspace.md](workspace.md)。Sidecar 协议见 [extension.md](extension.md)。

## 扩展 UI 壳

WebUI **不加载扩展 JS**，不 `window.open`，不按扩展名写死控件。每个扩展只投一份投影，壳按同一套布局渲染。goal 和以后别的包用同一组接口。

### 面上有什么

| 面 | 数据 | 行为 |
|---|---|---|
| 顶栏 chip | `status` | 只在 `title-row` 右侧。无 `status.text` 则无 chip。tone：`info` / `active` / `success` / `warning` / `error`。按 tone（error → warning → active → success → info）再按扩展名排；最多 4 颗全展示，超过则留 3 颗加 `+N`。右侧始终有展开钮。窄屏藏单颗 chip，只留「扩展 · N」。 |
| 详情 Modal | `panel` | 点 chip **或**展开钮打开同一 Modal。左侧导航列出全部 chip（窄屏改成顶部横滑），右侧是当前扩展的 panel。关 Modal **不**卸 chip，也不 `clearPanel` |
| 确认 / 选择 | `prompt` | 叠在详情 Modal 上。120s 或 abort = 取消 |
| slash | `commands[]` | 见上一节。扩展命令 `source=extension`，`argumentHint` + `completions` |
| 气泡 origin | user `origin` | `extension:<name>` 与用户气泡可区分 |
| 就绪锁 | `runtime.ready` | 未就绪不能打字；避免 `/goal` 尚未注册就 404 |

不要在 tabs 下、composer 上再做第二条 status 横条。`status` / `panel` **不进 jsonl**。Reload 后 sidecar 按自己的 `appendEntry` 等状态再 `setStatus` / `setPanel`。

### 投影

`GET /v1/sessions/{id}` 的 `extensionUi[]`（每个扩展一条）：

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

### Sidecar ← Host（用户点了）

| method | params |
|---|---|
| `ui.action` | `{ id }` 对应 `actions[].id` |
| `ui.submit` | `{ fields }` 当前表单（id → 值） |

需要二次确认用 `ui.confirm`（例如 Clear），不要在 SPA 里写死文案。

slash 子命令不必和按钮一一同名；面板是控制面，slash 是输入面。goal 的做法：pause / resume / clear 做 action（不能用的 `disabled`），status 用 sections，edit/start 用 field + submit。

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

`go test ./e2e -run WebUI` 会先起 server，再跑同一套 Playwright（需已 `npm install` 和装好 chromium）。

真模型（DashScope `qwen3.7-plus`，读 `DASHSCOPE_CN_API_KEY` 或 `~/.ki/ki.toml`）：

```bash
cd web && KI_LIVE=1 npm run test:e2e:live
```

或 `go test -tags live -timeout 5m ./e2e -run LiveWebUI`。
