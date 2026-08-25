# 扩展：会话打开即加载、slash 子命令、顶栏壳

对照：Pi（创建 AgentSession 即加载）；Grok（session actor 起来就 spawn MCP，握手后台）。  
截图：`tmp/ScreenShot_2026-08-25_162753_032.png`。

**范围：** Host 生命周期 + WebUI 壳。不改 `/goal` 业务语义。  
**不做：** 全局跨 session 连接池；扩展 JS 进 SPA；`window.open`。

---

## 1. 打开会话才加载；没就绪不能输入

### 现状

`Prepare` / `startRPC` 只在 `POST prompt`。新会话和点开历史会话都一样：GET 没有 `/goal`、没有 chip，第一句才冷启动。

`create` 预热盖不住历史会话（不再 `POST /v1/sessions`）。serve 重启后内存连接全没。列表里可能几十条，不能 boot 全 spawn。

### 方案

两套 Manager 不变（sidecar vs MCP）。**预热边界 = 打开这个 session**，不是创建、也不是 List。

| 入口 | 行为 |
|---|---|
| `POST /v1/sessions` | 返回后后台 Prepare；新会话马上会被打开，等于打开预热 |
| `GET /v1/sessions/{id}` | 该 id 尚未 Prepare → 后台 Prepare（历史会话、刷新、重启后点开） |
| `GET /v1/sessions` 列表 | **不** Prepare |
| serve 启动 | **不** 扫全部 jsonl |
| `POST prompt` | 已有 `ensure` 则 no-op |

GET **不 await** 握手（避免 npm/uvx 把打开会话卡死）。增加就绪位：

- session GET：`runtime.ready`（bool）。扩展+MCP 该次 Prepare 都结束（失败也算结束）为 true。
- SSE：`runtime_ready`（sideband，可带 `extensions` / `mcp` 错误摘要）。
- `commands[]` / `extensionUi` 随就绪变完整；未就绪也可先出 transcript。

**WebUI（打开历史和新会话同一套）：**

1. 点侧栏 / 新会话：立刻渲染标题和已有气泡。
2. `runtime.ready === false`：**锁 composer**（textarea、附件、`/` 按钮、发送都 disabled）。placeholder：「正在加载扩展和 MCP…」。不要静默可打字再 404 unknown command。
3. 收到 `runtime_ready` 或轮询 GET `ready: true`：解锁。slash 此时已有 `/goal`。
4. 预热失败：仍解锁输入（内置工具可用），toast `extension_error`；chip 可以没有。
5. 切走会话不关 sidecar；删会话 / Reload 才 `CloseSession`。

超时：沿用 initialize 10s 等；UI 锁到 `ready` 或失败，不要无限转圈（上限与 Prepare 超时对齐，超时当失败解锁）。

---

## 2. Slash：截图里那一条垃圾提示

截图 composer 上方一条白胶囊：

`[pause|resume|clear|edit|status] <object Run a goal to completion: /goal <objj…`

原因：

- `argumentHint` 是 `[pause|resume|clear|edit|status] <objective>`，`completions` 又是同一组词，`description` 再拼在后面。
- Palette 只按 **命令名** 滤，不展开子命令。
- `insertCommand` 用 `prefix + argumentHint`（hint 当正文），点选变成 `/goal[pause|…]` 这种串。
- 一条 item 横着溢出，看起来像输入框里的灰 hint，不是菜单。

### 方案

**Host / 插件约定（goal 先改 spec，Host 不解析业务）：**

- `argumentHint`：只描述**自由参数**，如 `<objective>`。不要把子命令写进 hint。
- `completions`：子命令枚举 `pause` / `resume` / `clear` / `edit` / `status`。
- `description`：一行，菜单第二列。

**Palette 两级（所有带 `completions` 的 extension 命令，不写死 goal）：**

1. `/` 或 `/go`：一行一项 `/{name}` + description。hint 若有，灰色写在名后，**不要**再 dump `completions.join(' ')`。
2. 光标在 `/goal` 或 `/goal `：换成子命令列表（`completions` 滤后缀）。选 `pause` → `/goal pause`；`edit` → `/goal edit `（尾空格）；`status` 同 pause。无 completions 则保持今日：点选 `/name` + 可选空格。
3. 点选只填 composer，回车才发送。
4. 菜单用现有 portal 列表（名 / 描述两列、单行省略），不要再做成 composer 上那条单行胶囊。

未 `ready` 时 `/` 按钮和 palette 都不开（见 §1）。

---

## 3. 顶栏：截图里的第二条横条

截图结构：

- **已有 top bar**：`conv-header` 标题行右侧 chip `Goal · complete`（对）。
- **多出来的**：tabs 下面整宽 `ext-drawer`（Goal / 说你好 / status·id·turns / Clear），把对话顶下去。这就是不要的横条。

Chip 已经在标题行，不要再在 drawer、composer、tabs 下做第二条 status。

### 方案

1. Chip **只** 在 `title-row` 右侧（现有 `ext-chips`）。无 `setStatus` 则无 chip。
2. 点击 chip：**Modal**（与 confirm/select 同壳），里面才是 panel（title、summary、sections、actions、fields）。关 Modal 不卸 chip。
3. **删掉** header 与对话之间的 `ext-drawer` 横条（`App.tsx` 里 `extPanel ? <div className="ext-drawer">`）。
4. `ui.confirm` / `ui.select` 仍用 `extensionUi[].prompt`，Clear 确认走现有 `ext-confirm-ok`，叠在 panel Modal 上即可。
5. 无 `window.open`、无扩展 HTML。

截图里标题被 kickoff 全文占满（`Goal mode is active…`）是 user 消息当 session title，**不在本方案**；需要的话另开会话标题规则。

---

## 验证

- 新会话、点开历史、重启后再打开：transcript 先出，composer 锁定直到 `runtime.ready`；之后 `/goal` 在 palette 里。
- List 侧栏不触发 spawn；只打开过的 session 有 sidecar。
- `/` 只有 `/goal` + 描述；`/goal ` 列出 pause/resume/clear/edit/status，不再出现截图那种 hint+desc 胶囊。
- 点 chip 出 Modal；tabs 下没有 Goal 横条。
- Playwright：历史会话打开锁输入 → ready 解锁；slash 两级；chip → Modal → Clear。
