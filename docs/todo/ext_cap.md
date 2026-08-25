# 扩展系统：对齐 Pi 插件自由度 — 待补清单

参考：Pi `ExtensionAPI` / `ExtensionContext` / `ctx.ui` / `pi.events`（`/data/hgy/pi`）；本仓库 `docs/extension.md`、`docs/webui.md`。

**范围：** 只改 extension Host、协议、会话调度、WebUI 扩展面。  
**不做：** 具体业务扩展包（goal 等）。

**目标：** 第三方扩展在 **不改 ki core、不 curl 全量 `/v1`** 的前提下，达到与 Pi 插件同级的可编排自由度。形态保持 **NDJSON sidecar + capability**，不搬 in-process TS。

**兼容：** **不做**旧版扩展协议兼容。删除今日 `hook` / `intercept` / `intercept[]` / 旧 RPC method 名与双栈迁移；一次切到订事件 + 本文 API。仓库内测试与文档同步改掉。

**多扩展协作（已定）：** 采用与 Pi 相同的 **扩展事件总线** 架构（跨 sidecar 由 Host 转发）。Host 提供总线与投递；workflow 互斥等协作协议跑在总线上。Host **不**维护业务 workflow 注册表，**不**用 Host 锁表替代总线。

**生命周期订阅（已定，对齐 Pi `on` / Codex hooks）：**  
对外只保留 **订事件**：

- Host 固定 **event 目录**（停靠点写在主路径里）。
- Sidecar 在 `initialize`（及 Reload）申报：`subscriptions: [{ "event", "mode": "sync"|"async" }]`。
- **`sync`** = 该点 **RPC 并 await**，应用返回值（block / 改 args·messages·tools…）。
- **`async`** = persist/SSE **之后** notify，不依赖返回值改流；瘦 DTO；fail-open。
- 同一 event：先 sync 链，再 async（async 见 **最终** 结果）。
- 允许 sync 的 event 由 Host 白名单规定（如 `message_update` 只许 async）。
- `failClosed`、短超时、富载荷：**仅 sync**。
- capability 名：**`lifecycle`**。

订事件（Host→扩展）与 **`bus.*`（扩展↔扩展）** 是两套通道。

---

## 现在已经有的（将被替换的底座）

- 声明式：`prompt.append` / `skill` / `command` md / `mcp`（保留）
- 代码：`tool` / `command.invoke`（保留）；今日 `hook`+`intercept` **删除并换成订事件**
- 失败策略、`extension_error`、同 session 一 sidecar（sync 失败 skip 集语义保留并写进新规格）

---

## 必须实现

### 0. 订事件（替换 hook / intercept）

- [ ] Host **完整 event 目录表**：每点允许的 mode、sync/async 载荷、超时、链序（全局名→项目名）
- [ ] 目录至少覆盖并对齐 Pi 语义（名称以规格终稿为准）：
  - 会话：`session_start` 等价（initialize/绑定）、`session_shutdown`、**`session_before_compact`（sync：可 cancel / 定制 summary）**、`session_compact` / compact failed（async）
  - 输入：**`input`（sync：改写或吞掉用户输入）**
  - Agent：`before_agent_start`（sync；**每 occupy 一次**，steer 不重跑）、`agent_start` / `agent_end` / **`agent_settled`**（async；settled 默认不许 sync）
  - Turn / message：`turn_start`/`turn_end`；`message_start`；`message_update`（仅 async）；`message_end`（**sync 可替换最终 message，同 role**）；`request_header`
  - Tool：`tool_call` / `tool_result`（sync）；`tool_execution_start`/`update`/`end`（update 仅 async）
  - Provider：`context`、`before_provider_request`、`before_provider_headers`、`after_provider_response`、`provider_error`（fallback）
  - 其它已有 sideband：`queue_changed`、`steer_accepted`、`run_aborted`、`mcp_*`（async）
- [ ] `initialize` 只接受 `subscriptions`；未知 event / 不允许的 sync → **拒载该订阅（error）**
- [ ] RPC：**统一** `lifecycle.invoke`（params 含 `event` + payload）；async 用 notification `lifecycle.event`
- [ ] sync 载荷带 **紧凑 ctx 快照**：`idle`、`model`、`aborted`（对齐 Pi handler 上的 ctx 子集）；全文 history 不默认下发
- [ ] `before_agent_start` sync 可见 system 全文；其它点可用 system 哈希/长度；`getSystemPrompt` 语义用 snapshot / 该事件载荷覆盖
- [ ] **工具批语义**：写清串行/并行下 `tool_call` 顺序、`terminate` 批结束条件（对齐今日 Terminate）
- [ ] 删除旧 method：`intercept.*`、旧 `event` 通知名若重命名则只保留新名

### 1. 双向 RPC：sidecar → Host

- 同一 stdio NDJSON JSON-RPC；`readLoop` 处理 inbound request
- 禁止在 sync 生命周期回调栈里同步等待整轮 run（enqueue 只许快速入队）
- 不另开端口；不以 `server.json` + 全量 `/v1` 为正式插件 API

### 2. 扩展事件总线（对齐 Pi `pi.events`）

- [ ] `bus.emit`：`channel` + `data`；投递本 session 其它订阅 sidecar
- [ ] **initialize 声明订阅** + **运行中 `bus.subscribe` / unsubscribe**
- [ ] capability：`bus`
- [ ] 同步 fan-out：emit 的 RPC 在订阅者处理完后返回；**深拷贝** data，result 带回最终 data（mutex 用）
- [ ] 另支持 fire-and-forget 广播（规格与同步 emit 分开）
- [ ] 总线不进 jsonl、不进 LLM context；session 关闭清空
- [ ] Host 不解析 channel 业务含义

Workflow 互斥（扩展协议，非 Host API）：

- channel：`workflow:mutex:v1`；group：`agent-workflow`
- 抢锁 payload `{ sessionId, group, busy:false }` → 持锁方置 `busy=true`
- 未订阅者不参与互斥

### 3. 投递下一轮（对齐 Pi `sendUserMessage` / `sendMessage`）

- [ ] `session.enqueue`
  - `content[]`
  - `deliverAs`：`queue`（默认）、`steer`、`nextTurn`（对照 Pi 三种投递测全）
  - `when`：`now`、`settled`
  - `idempotencyKey`、`origin=extension:<name>`
  - `kind`：`user`（默认）与 `custom`（`customType`/`display`；默认进 provider context 与 transcript）
- [ ] 与 `POST /prompt` 共享 `AcceptPrompt`
- [ ] **扩展 FIFO** 与 **用户 queue 分轨**；用户占用结束后 **先用户 queue，再扩展 FIFO**
- [ ] 未持总线 mutex 的自动工作流不得 enqueue（协议约束 + fixture 覆盖）

### 4. 稳定空闲（对齐 Pi `agent_settled` / `waitForIdle`）

- [ ] `agent_settled`：occupy 结束且 auto-compact/内部收尾完成，可接受新 occupy（不含「扩展 FIFO 已空」）
- [ ] `when=settled` 在 settled 上 flush，按扩展 FIFO 开后续 occupy
- [ ] `session.snapshot`：`idle`、`running`、queue/扩展 FIFO 长度、model、thinking、**activeTools / allTools / commands**、本扩展 status 键等（覆盖 Pi `isIdle`/`getActiveTools`/`getCommands`）

### 5. 会话扩展状态（对齐 Pi `appendEntry`）

- [ ] jsonl `type=custom`：`extension`、`customType`、`data`
- [ ] `session.appendEntry`；不进 provider context；强制只能写自己的 `extension`
- [ ] reload / fork 复制或回放；sidecar 绑定后读回自己的 entries

### 6. 运行时控制

- [ ] `session.abort`
- [ ] `session.setActiveTools`（会话级；未知名忽略 + `extension_notice` warn；不得静默清空全部）
- [ ] `tools.register`：session 内增补/更新；**下一 occupy** 的 Prepare 生效
- [ ] `session.compact`（与 HTTP compact 同路径）
- [ ] **`session.patch`**：扩展可改本 session 的 model / thinkingEffort（对齐 Pi setModel/setThinkingLevel；校验与 HTTP PATCH 相同）
- [ ] async **opt-in 加富**：usage、context 压力、tool-free fingerprint；默认仍不传正文/args/result
- [ ] sync 失败 → `extension_error` + 本 occupy skip 集（与今日 fail-open/failClosed 规则一致并文档化）

### 7. WebUI 扩展面（Host 契约 + 内置通用壳）

扩展 **不**贡献前端代码；主 SPA 通用壳 + JSON 面板。快捷键注册不做。

| 能力 | 行为 |
|---|---|
| [ ] Top bar StatusChip | 短文本 + tone；点击开详情 |
| [ ] DetailDrawer | panel：sections / actions / fields |
| [ ] `extension_notice` toast | info/warn |
| [ ] confirm / select | 超时 **120s** = 取消 |
| [ ] origin 气泡 | 扩展 enqueue vs 用户 |
| [ ] queue UI | 用户 queue + 扩展 FIFO 可区分展示 |
| [ ] **slash 参数补全** | 扩展 `CommandSpec` 可带补全提示/枚举；WebUI command palette 消费（对齐 Pi command completions 子集） |
| [ ] 设置页扩展列表 | Scan、启停、path 字符串 |

Sidecar UI RPC：

- [ ] `ui.setStatus` / `ui.setPanel` / clear；投影 `GET session.extensionUi` + SSE `extension_ui_updated`
- [ ] status/panel **不持久化**；reload 后 sidecar 按 appendEntry 重放
- [ ] `ui.action` / `ui.submit` / `ui.confirm` / `ui.select`
- [ ] 多扩展 chip 排序：扩展链序；SSE 推送，CLI 可只展示 notice

不做：扩展 `web/` 动态加载、iframe 任意页、改 editor/footer/主题、主仓写死 goal/plan 页面。

### 8. 规格文档

- [ ] `docs/extension.md`：订事件全表、lifecycle/bus/enqueue/state/tools/ui/snapshot/patch、重入、工具批、错误与 skip
- [ ] `docs/webui.md`：extensionUi、chip/drawer、confirm/select、slash 补全、origin/queue
- [ ] 删除文档中一切旧 hook/intercept 契约叙述（以新模型为准）

---

## 明确不实现（不对齐 Pi）

| 项 | 原因 |
|---|---|
| in-process TS/Go 扩展 | 坚持 sidecar |
| `ui.custom` / 扩展打进 bundle / shortcut / flag / markdown·entry renderer | WebUI 通用壳 + 无快捷键扩展 |
| `registerProvider` | 已有 provider HTTP |
| `newSession` / `fork` / `navigateTree` / `reload` / `shutdown` / **`withSession`** | HTTP/内置已有；新 session 用新 sidecar + `KI_SESSION_ID` + enqueue |
| `project_trust` | ki 无 trust 模型 |
| `exec` 经 Host | 扩展自管 |
| `user_bash` | ki 无 `!` 用户 shell 通道 |
| Host 内置 workflow 锁表 | 总线协议 |
| 旧协议兼容 / 双栈 / manifest 映射期 | **直接删除旧面** |

---

## 已定决策（写进 `docs/extension.md` / `docs/webui.md` 时按此落笔）

下列条目不再当作开放问题；实现与规格以此为准。

### 订事件 / RPC

**L1 — sync 生命周期怎么调 sidecar**  
- **定：统一方法** `lifecycle.invoke`。  
- params 至少含：`event`（事件名）、本次 payload、以及紧凑 ctx。  
- **不定**「每个事件一个 RPC method」（如再拆 `lifecycle.tool_call`）。  
- async 旁听用 notification：`lifecycle.event`（与 invoke 成对，名称固定）。

**L3 — 非法订阅怎么处理**  
- 未知 `event`、或该 event 不允许的 `mode: sync`：  
  - **该条订阅 error，不生效**。  
- 若 manifest 声明了 capability `lifecycle`，但初始化后 **没有任何有效订阅**：  
  - **整个扩展包加载失败**（避免空 lifecycle 包静默成功）。  
- 其它 capability（tool/command/…）不受「某条订阅被拒」影响，除非整包因此失败。

**L4 — 占用开始前改 system / 注入上下文的事件名与频率**  
- **事件名：`before_agent_start`**（对齐 Pi，不用 `before_run`）。  
- **触发频率：每个 occupy 恰好一次**（空闲 `POST prompt` / 扩展 enqueue 开跑导致的新 occupy）。  
- 该次 sync 可见 **system 全文**，可改 system / 消息（具体字段见 event 目录表）。

**L8 — steer 与 `before_agent_start` / 每 turn 改消息**  
- **steer 不重跑** `before_agent_start`（与今日 beforeRun「每 occupy 一次」一致）。  
- 同一 occupy 内每一轮要改发给模型的 messages：订 **`context`（sync）**。  
- 不要用「每次 steer 再跑一遍 before_agent_start」模拟 Pi 的每 turn 行为。

### 队列 / settled / UI 投影

**E2 — 用户排队 vs 扩展自动 enqueue 谁先跑**  
- 两套队列：**用户 `queue.json`（及现有用户路径）** 与 **扩展 FIFO**（仅 `origin=extension:*`）。  
- 某一 occupy **release / settled 之后** 若两边都有等待：  
  1. 先排空（或先取）**用户**队列开下一 occupy；  
  2. 用户侧无等待时，再取 **扩展 FIFO** 头元素。  
- 避免 goal 等自动续跑插在用户已排队消息前面。

**E3 — `agent_settled` 是否表示「扩展 FIFO 也空了」**  
- **否。**  
- `agent_settled` = 当前 occupy 结束，且 auto-compact 等 **Host 内部收尾**完成，此时 **可以** 再 accept 新 occupy。  
- 不要求扩展 FIFO 为空。  
- `when=settled` 的 enqueue：在收到 settled 后由 Host **flush** 进扩展 FIFO / 启动逻辑；flush 触发的新 occupy 是 settled **之后** 的事。

**E5 — 顶栏 status / 详情 panel 是否写入 jsonl**  
- **不持久化。** 只活在 Host 内存投影里，经 session GET + SSE 给 WebUI。  
- Reload / 新 sidecar：扩展根据 **`appendEntry` 等持久状态** 再调 `ui.setStatus` / `ui.setPanel` 恢复展示。  
- 与「会话业务状态用 custom entry」分工明确：UI 是视图，entry 是源。

### 总线 / 对话框

**B2 — `bus.emit` 的 data 如何在多 sidecar 间传递**  
- Host 对 payload **深拷贝** 后再 fan-out。  
- 各订阅者 sync 处理可返回对 data 的修改；Host 按链序合并，**emit 的 RPC result 带回最终 data**。  
- 避免共享同一可变 JSON 导致未定义乱序写（mutex 的 `busy` 标志依赖可预测合并）。

**B4 — `ui.confirm` / `ui.select` 等多久**  
- **超时 120 秒。**  
- 超时视为用户 **取消**（与点取消同一结果路径回 sidecar）。  
- session abort / 扩展卸载：同样走取消，不挂死 invoke。

---

## 落地顺序

1. 订事件全表规格（含上文已定决策）+ 删除旧 hook/intercept 实现与文档  
2. inbound RPC 框架（enqueue/bus/ui/… 共用 readLoop）  
3. `session.enqueue` + 用户/扩展分轨队列 + `agent_settled` + `AcceptPrompt`  
4. `bus.*`（深拷贝 emit）+ mutex fixture  
5. `appendEntry`  
6. UI 投影（不持久化）+ WebUI 通用壳（chip/drawer/notice/origin/queue）  
7. abort、snapshot（含 tools/commands）、setActiveTools、tools.register、compact、session.patch、async 加富  
8. `input` / `message_end` / `session_before_compact` sync 行为  
9. WebUI confirm/select（120s）+ action/submit + slash 补全  

验收：**fixture 扩展**覆盖 sync block、async settled、enqueue、bus mutex、panel/action、input 改写、compact cancel；不交付 goal/plan 产品包。

---

## 一句话

一次性切到 **订事件（sync/async）+ enqueue + bus + 状态 + 运行时控制 + WebUI 通用壳**；Pi 向齐项全部列入实现；**不做旧版兼容**。队列优先级、settled 语义、lifecycle RPC 形态等关键决策见上文「已定决策」。
