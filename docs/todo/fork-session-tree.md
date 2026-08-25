# Fork session Tree 浏览器与侧栏临时定位

## 背景与现状

当前 fork 的父子关系实际上已经落盘：子 session 的 `events.jsonl` 首行 header 有
`parentSession`，值为直接来源 session 的 id。再次从子 session fork 时，新的
session 会把这个子 session 作为父 session，因此数据模型天然支持多级关系。

目前的问题主要在契约和展示层：

- `GET /v1/sessions` 和 `GET /v1/sessions/{id}` 通过 `sessionMap` / `infoMap`
  返回的是含义较宽的 `parent`；
- fork 的响应又额外返回 `parentSession`，造成同一个关系有两个字段名；
- `SessionInfo` 只把它建模成 `parent?: string`，WebUI 没有使用它建立树；
- workspace 的 `sessionIds` 只表达工作区内排序，不表达 fork 关系，当前侧栏因此把
  所有 session 平铺；
- session 内 edit/regenerate 产生的是 entry 的 sibling branch，不能和跨 session
  的 fork child 混为一谈。

所以不是 fork 不知道 parent，而是 parent 已保存但没有形成统一、明确的 API 契约，
也没有区分“普通 fork 的来源关系”和“需要被宿主托管的 child 关系”。后者不应污染
常驻 session 列表，而应通过当前 session Info 中的 Tree 浏览器按需访问。

## 需求修订：记录处理策略，不引入调用方来源概念

主仓库只需要知道一个 child 应该如何被处理，不需要知道它由哪个调用方创建。普通 fork
与其他调用方复用同一个 fork 接口，只传入不同的处理策略。

建议保存一个创建后不可变的枚举字段 `forkMode`：

```json
{
  "id": "child",
  "parentSessionId": "parent",
  "forkMode": "tree"
}
```

- `forkMode=flat`：保留 `parentSessionId` 作为来源追踪，在 workspace 中平铺，删除
  parent 时独立保留；
- `forkMode=tree`：隐藏于常驻 workspace 列表，在 Tree 弹窗中挂在 parent 下浏览，并
  随 parent 删除级联清理。

`forkMode` 和 `parentSessionId` 都属于 fork 创建时确定的 session lineage metadata，建议
写在 jsonl header（`Header.ParentSession`、`Header.ForkMode`），而不是放入可变的
`config.json`。workspace 整体删除是用户明确发起的批量删除，仍按现有 workspace 语义
删除组内所有 session，不被 child 的 fork mode 拆分。

普通 WebUI fork 的默认值是 `flat`。需要树状展示和托管生命周期的调用方传入 `tree`，
但主仓库不需要知道调用方的业务名称。

这里故意把树展示和级联删除绑定到同一个枚举值，因为当前产品定义中 `tree` 就代表
“由 parent 托管的 child”。不增加 `tree: true/false`，而使用 `forkMode` 是为了让字段
表达这是 fork 的处理模式，并为未来增加 `background`、`archive` 等模式保留扩展空间。

fork 请求建议是：

```json
{
  "entryId": "...",
  "forkMode": "tree"
}
```

server 只接受 `flat` / `tree`、在创建时持久化，并由 server 在删除时执行级联；前端只
消费 `forkMode` 渲染，不自行实现级联删除。`forkMode` 不应通过普通 session PATCH 修改，
避免 session 在树中途改变归属语义。

## 目标

- 常驻 workspace session 列表只显示 `forkMode=flat` 的 session；普通 fork
  继续使用现有列表体验。
- 在当前 session 的 Info 页面增加 Tree 按钮，打开一个类似文件浏览器的 Miller 弹窗，
  用列逐级浏览 `forkMode=tree` 的 child。
- 支持任意深度，不依赖侧栏缩进；弹窗始终保持两列，通过推进左列持续浏览，当前路径始终可定位。
- 点击确定后打开选中的 tree session；只把这个 child 临时注入所属 workspace 的侧栏
  列表并自动展开 workspace、滚动到该行、保持 active/focus 状态。
- 临时注入只服务当前浏览会话，不注入隐藏 parent 链，不把 child 永久变成普通列表项，
  也不通过 localStorage 持久化。
- `forkMode=flat` 的 child 删除独立处理；删除一个 session 时，只沿 `forkMode=tree` 的
  边级联，不删除 flat 分支及其独立后代。
- tree child 的父 id 因外部删除、数据损坏或不在当前列表时仍可在 Tree 弹窗中看到，
  不丢 session，并按 orphan 规则处理未能建立的树边。
- 不增加新的 children REST 路由；继续使用已有的 `GET /v1/sessions` 平铺数据，在
  前端派生 Tree 浏览数据。

非目标：在侧栏实现递归展开；修改 session 内 entry branch 的行为；允许通过拖拽改变
fork parent；让普通 fork 参与 Tree 浏览或级联删除。

## 数据契约方案

### 统一直接父字段

对外统一使用明确的 `parentSessionId`：

```json
{
  "id": "child",
  "title": "子会话",
  "workspaceId": "workspace",
  "parentSessionId": "parent",
  "forkMode": "flat",
  "timestamp": "..."
}
```

以下三个响应必须完全一致：

- `GET /v1/sessions` 的每一项；
- `GET /v1/sessions/{id}` 的 session metadata；
- `POST /v1/sessions/{id}/fork` 的返回值。

`parent` 和 fork 响应专用的 `parentSession` 不再作为 WebUI 契约。落盘的
`Header.ParentSession` 可以继续作为内部字段，由 server 统一映射为
`parentSessionId`；同时落盘并在 list/detail/fork response 暴露 `forkMode`，普通 fork
默认 `flat`，避免为了 UI 行为引入来源类型概念。

### 可选的 fork 来源信息

如果需要在后续 UI 中显示“从父 session 的哪条消息 fork”，再增加
`forkedFromEntryId`。它不是构建树所必需的，第一阶段不要让它阻塞树形导航；如果
增加，应在 ForkAt 确定实际 target（空 target 即当前 leaf）后写入，且在 list/detail/
fork response 中保持同名。

不返回 `children[]`、`rootSessionId` 或单独的 children API。children 可由已有的
平铺列表 O(n) 建立，避免两个方向的数据不一致，也符合 loop 已经有数据时不新造
REST 路由的约束。`parentSessionId` 是直接父，不是祖先链；深度和根节点由客户端
计算。

### 异常关系

- parent id 找不到：把 child 放入当前 workspace 的顶层 orphan 区，行尾显示“来源已
  删除/不可用”，仍允许打开；不要隐藏。
- parent 存在但属于另一个 workspace：不要跨 workspace 把树连起来，child 在当前
  workspace 顶层显示，并保留 orphan 状态。
- 检测到环或重复遍历：以 visited 集合断开该边，把未渲染节点作为顶层显示，避免
  递归渲染卡死。
- 删除 session 时只沿 `forkMode=tree` 的边级联；遇到 `flat` 边就停止，独立分支及其
  后代继续存活。
- orphan tree child 不因暂时找不到 parent 而被删除；保留 `parentSessionId`，下一次列表
  刷新仍按 orphan 规则展示。

## Tree 浏览器模型

### 常驻列表与 Tree 数据范围

`GET /v1/sessions` 仍返回全部 session，供 Tree 浏览器使用；“隐藏”只发生在 WebUI
常驻 workspace 列表，不在 server list 接口过滤。这样不需要新增 children 路由，也不会
因为侧栏隐藏而失去深层节点。

侧栏普通列表的可见条件是：

```text
forkMode != tree || id ∈ temporaryTreeRevealIds
```

`temporaryTreeRevealIds` 是 App 内存状态，不写 localStorage。它保存从 Tree 弹窗确认跳转过的
多个 child，并在 App 生命周期内跨普通 session 切换、搜索和新建 session 保留；整页重新进入或
刷新后自然清空，目标 session 被删除后逐个清理。常驻列表不会因此展开完整 Tree，也不会临时
显示它们的隐藏 parent。

Tree 按当前 session 建立一个局部 connected component：

1. 沿 `parentSessionId` 向上追溯 `forkMode=tree` 的边，找到最上层 root；如果 parent
   是 `flat`，它仍可作为 Tree 的 root anchor，但 flat child 本身不进入 Tree。
2. 向下只收集 `forkMode=tree` 且 `parentSessionId` 等于当前节点的 children。
3. 当前 session 是 flat 但拥有 tree children 时，以当前 session 为 root；当前 session
   是 tree child 时，显示它所在的完整 root → current 路径。
4. parent 缺失、跨 workspace 或存在环时，不丢节点，放进 “unresolved” root，并在行上
   显示 warning。

建议把上述逻辑抽成 `web/src/session-tree.ts` 的纯函数，输入 `SessionInfo[]`、
workspace 顺序和 current id，输出 root、path、children、orphan 状态。排序仍复用
workspace `sessionIds` 的顺序；树浏览器不修改顺序，也不支持 re-parent。

### Info 入口

在 `SessionConfig` 的 session metadata 操作区增加 `Tree` 按钮，与 Reload / Edit 同级：

- 当前 session 没有 tree parent，也没有 tree children 时不显示按钮；
- 当前 session 是 tree child，或它拥有 tree child 时显示按钮；
- 按钮只打开弹窗，不改变当前 session；
- 弹窗打开前使用 App 当前的全量 session snapshot。若 snapshot 正在刷新，等刷新完成
  后再构建树，避免刚创建的 child 不见。

`SessionConfig` 不应直接自己维护全局 session 列表；由 App 传入 `sessions`、workspace
顺序和 `onTreeOpen` / `onTreeSelect`，保持 session 列表、当前 session 和 Tree 弹窗
使用同一份状态。

### Miller 弹窗

新增 `SessionTreeBrowser`，复用 `DirectoryBrowser` 的视觉和交互骨架，但不调用文件
系统 API：

- 复用 `modal-mask`、`dir-browser`、`dir-head`、`dir-crumb-bar`、`dir-body`、
  `dir-miller`、`dir-col`、`dir-row`、`dir-foot` 的布局和响应式样式；
- 不把 session 伪装成 `FsEntry`，也不复用 `listFS`；可以后续抽出通用 Miller shell，
  让目录浏览器和 Tree 浏览器共享壳而不是共享数据模型；
- Miller 区域固定只有两列，复用文件浏览器现有的 `parent` / `child` 状态模型，不随
  深度增加列数；
- 左列显示当前层级可选的 session sibling，右列显示左列选中 session 的直接 tree
  children；root session 只出现在顶部路径条，不单独占一列；
- 如果当前 session 已经在深层，打开弹窗时左列显示它的 sibling，当前 session 被选中，
  右列显示它的 children；如果当前 session 是 root，则左列显示它的 children，等待
  用户选择；
- 点击右列的 child 后，把原右列推进成新的左列，再加载该 child 的 children 到右列，
  因此任意深度都可以通过两列持续浏览；
- 顶部路径条显示完整的 root → selected 路径（必要时省略），点击路径段回到对应层级；
- session 行显示 title、model、running 状态和 `Tree` / `unresolved` 弱标记，不显示
  宿主绝对路径；
- 单击只改变候选 selection 和后续列，不立即切换主页面；底部的“确定”才执行跳转；
- “取消”关闭弹窗并完全恢复打开前的 current session；“确定”无候选时禁用；
- Escape 取消，Enter 确认，所有行使用真实 button 和 `aria-current`，不依赖颜色表达
  选中状态。

推荐的交互形态（root 只在路径条中出现）：

```text
┌ Session Tree ───────────────────────────────────────────────┐
│ Root session        ›  child A        ›  child A-1            │
├──────────────────┬──────────────────────────────────────────┤
│ child A       ✓  │ child A-1            ✓                    │
│ child B          │ child A-1-1                              │
├──────────────────┴──────────────────────────────────────────┤
│                                                   取消  确定  │
└──────────────────────────────────────────────────────────────┘
```

### 确认跳转与侧栏临时显示

确认 Tree child 后按以下顺序处理：

1. 把候选 id 追加到 `temporaryTreeRevealIds`（已存在则不重复）；
2. 调用现有 `openSession(id)`，复用 GET detail、停止旧 SSE、加载新历史和设置
   `selectedWs` 的逻辑；
3. 打开所属 workspace（现有 `openSession` 已会展开 workspace），并切换到该 child 的
   conversation/chat 页面；
4. 重新计算侧栏列表：常规 session 仍按 workspace 原顺序显示；把每个 child 紧跟在
   它的直接 parent session 下方插入，不插到 workspace 顶部或 root 末尾。例：

   ```text
   parent session
     temporary child A-1
   next ordinary session
   ```

   如果直接 parent 本身也是隐藏 tree session，则不临时注入 parent；把 child 放在该
   workspace 中最近的可见 root/parent 位置，并用来源路径副标题说明真实 parent。
5. 每个临时行增加 `tree-focus` 样式、Tree 标记和路径副标题，例如
   `Tree · parent / child A-1`；只使用一层临时缩进，不把它渲染成完整的嵌套树；
6. 等 DOM 行出现后再 `scrollIntoView({ block: "nearest" })`，并将视觉 active、
   `aria-current` 和必要的 DOM focus 放到该行。

临时行的生命周期：

- 关闭 Tree 弹窗、从侧栏打开其他 session、点击“新会话”或切换到搜索模式时都保留；
- 从 Tree 弹窗选择另一个 child 时追加，不覆盖已有 child；
- 目标 session 被删除、列表刷新后不存在或 workspace 被删除时逐个清除；
- 浏览器刷新后清除，因为它是导航反馈而不是用户偏好；
- 侧栏可同时显示多个已选择 child，但仍不展开完整隐藏 parent 链。

Tree 弹窗本身仍支持多级浏览和选择深层 child；确认后侧栏追加最终选中的 child，不展开
它的隐藏 parent 链；成功打开后主区域切到该 child 的 conversation/chat 页面。搜索模式默认不展示隐藏 tree session，Tree 弹窗是
它们的主要入口。

### 删除、排序和异常边界

- Tree 浏览器只读，不提供删除、pin、拖拽或改变 parent 的操作；这些仍通过 session
  侧栏菜单或其他既有入口完成。
- 删除 `flat` session 不影响其 child；删除 `tree` parent 由 server 递归执行，
  前端收到成功后刷新全量列表并清除失效的 temporary reveal。
- workspace 删除仍是显式批量删除，直接删除组内 session，不在 Tree 弹窗中重复确认。
- parent 缺失的 child 在 root 列表中显示 unresolved，不因为无法连接到路径而隐藏。
- Tree 弹窗使用内存快照；如果 fork/delete 在弹窗打开期间发生，确定时先校验目标仍
  存在，失败则保留弹窗并 toast，要求重新刷新，不静默跳到其他 session。

## 实施拆分

1. 后端统一 `parentSessionId` 映射，增加并校验 `forkMode`，移除 fork 响应的字段分叉，
   补充 list/detail/fork 一致性测试和 flat/tree 删除测试；如采用 `forkedFromEntryId`，
   同时补充 session header 和 ForkAt 测试。
2. 更新 `web/src/types.ts`，把字段命名改成 `parentSessionId` / `forkMode`；新增纯
   Tree component 构建测试，覆盖三级、多根、orphan、跨 workspace、环保护和排序。
3. 新增 `SessionTreeBrowser.tsx`，复用 `DirectoryBrowser` 的 Miller modal shell，
   实现列式浏览、路径恢复、候选选择、确定/取消和键盘操作。
4. 在 `SessionConfig` 增加 Tree 入口；在 App 增加 `temporaryTreeRevealIds`、隐藏
   tree session 的列表过滤、临时行插入、workspace 展开和滚动/焦点定位。
5. 增加 Tree、unresolved、临时 focus 行以及中英文 i18n 文案；补充 Info 弹窗、跳转、
   刷新、删除和窄侧栏行为测试。
6. 更新 `docs/session.md` 的 API/header 说明和 `docs/webui.md` 的侧栏/Info 说明，重建
   `web/dist` 后再 `go build`。

## 验收清单

- 新建 A，使用 `forkMode=flat` fork 得到 B；B 出现在常驻 workspace 列表中，删除 A
  后 B 仍保留。
- 使用 `forkMode=tree` 创建 B，再从 B 创建 C；B/C 不出现在常驻列表，A 的 Info 中
  打开 Tree 后可以按列浏览 A → B → C。
- 在 Tree 弹窗中选择 C 并确认后，主界面跳转到 C；C 被临时注入 A 所属 workspace
  的列表，自动展开 workspace、滚动到 C 并高亮。
- 关闭 Tree 弹窗不改变当前 session；切换普通 session、新建 session、刷新页面后，
  临时 Tree 行按规则清除。
- 删除 A 时，`forkMode=tree` 的 B/C 被级联删除；从 B 创建的 `forkMode=flat` session
  保留。
- 因外部缺失 parent 的 tree child 显示 orphan 提示且可打开，不因渲染失败而丢失。
- 弹窗能处理多个 root、深层路径、空 child、跨 workspace、环和 unresolved 节点；
  搜索不会把隐藏 tree session 混入常驻结果。
- `/v1/sessions`、`GET /v1/sessions/{id}`、fork response 都只依赖同一个明确的
  `parentSessionId` / `forkMode` 语义，前端不再判断 `parent` 与 `parentSession` 两套
  字段，也不从 UI 临时状态推导删除策略。
