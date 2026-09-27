# WebUI 历史翻页、滚动跟随与长会话性能优化方案

日期：2026-09-27。调研基线：`7c16f06`。状态：M1–M4 已落地；实施结果见第 10 节；compact 整轮分页与中间帧修复见第 11 节。第 2–8 节保留调研时的基线与设计理由，真机验收另行标明。

## 1. 结论与优先级

这四个问题需要一起处理：分页决定哪些内容可读，锚定决定内容出现在哪里，跟随决定谁控制视口，性能优化则不能破坏前三者。继续单独调整距离阈值或增加滚动补偿，容易让几套逻辑互相干扰。

| 优先级 | 用户问题 | 建议 |
| --- | --- | --- |
| P0 | 有时不能继续向上翻历史 | 修复加载窗口与游标不一致；统一分页调度；补齐顶部加载、失败重试及无滚动空间时的入口 |
| P0 | 上滑与自动跟随争抢 | 分离用户阅读意图与“几何上接近底部”；向上手势立即暂停跟随；统一滚动写入的归属 |
| P0 | 加载一页后跳到未知位置 | 改用提交时的稳定消息锚点；接入当前虚拟列表库已有的锚定能力；取消重复的逐帧补偿 |
| P1 | 手机长列表卡顿、普通加载流量偏高 | 索引真正按需加载；正文按可见性补全；限制解析、缓存、分页响应的工作量 |
| P2 | 长流式回复仍占大量带宽 | 在现有 loop/SSE 契约内减少累积全文重复传输，同时保证重连和回放正确 |

先完成 P0 的回归与真机验证，再收紧性能预算。保留现有虚拟化、Markdown 分段和服务端 slim view 的成果，不另换列表库。

## 2. 调研范围与已有成果

主要检查了 [App.tsx](../../web/src/App.tsx)、[Chat.tsx](../../web/src/features/chat/Chat.tsx)、[model.ts](../../web/src/lib/model.ts)、[rowHeight.ts](../../web/src/lib/rowHeight.ts)、[Markdown.tsx](../../web/src/features/markdown/Markdown.tsx)、[client.ts](../../web/src/api/client.ts)、[session/view.go](../../internal/session/view.go)、[server.go](../../internal/server/server.go)、[loop.go](../../internal/loop/loop.go)，以及现有性能、消息显示和响应式测试。

当前契约见 [WebUI 文档](../webui.md)、[session 文档](../session.md)、[事件文档](../events.md)。[此前的滚动问题复盘](../postmortem/2026-09-27-transcript-scroll-jumps-and-lost-pages.md)已经记录固定行高估算、提交前恢复位置等问题及修复；本方案处理这些修复之后仍留下的边界。

已存在的优化：

- 首次返回最近 100 条 leaf entries；正文按字段裁到 24 KiB；相同 request header 省略重复 system/tools；历史通过 `before` 翻页。单页上限 500 条，正文批量补全上限 40 个 id。
- 整树 index 与首屏分离；服务端 transcript 有增量读取缓存；JSON/文本使用 gzip，哈希静态资源 immutable，SSE 除外。
- 聊天超过 48 行/节点时虚拟化，overscan 为 4；工具默认折叠，compact 按 turn 折叠回复；轨迹和请求导航也有虚拟化。
- 行高按 item key、宽度缓存，最多 2000 项；未知行使用字符数估算。静态 Markdown 延迟到 idle 渲染，分块结果缓存最多 300 项；流式 Markdown 封存已定稿段，活动段超过 4 KiB 时先显示纯文本。
- SSE 在浏览器内按 16 ms 批处理相邻累积更新；服务端清理已过时的 partial，支持按 run/seq 续传。

这些优化分别限制了首屏、DOM、解析或回放的部分成本，尚不等于整个阅读过程有稳定的位置、内存和传输上限。

## 3. 本轮验证及其边界

### 3.1 现有测试基线

执行 `cd web && bun run test:perf`：**3/3 通过，23.6 秒**。使用测试套件自己的隔离 fake server，没有重启正常开发服务。

| 项目 | 本轮结果 |
| --- | --- |
| 400 turn、390 × 844 视口的翻页锚点 | `-19px → -19px` |
| 向上滚动采样 | 最差单帧内容位移 400px，等于测试的一步滚轮；两段采样 long task 均为 0 |
| 200 turn 历史：首次 GET | 237,447 B；本机 9 ms |
| 同一历史：index 请求 | 492,591 B；本机 48 ms |
| 同一历史：较早一页 | 234,075 B；本机 13 ms |
| 同一历史：打开 UI | 235 ms；assistant DOM 2 个；JS heap 12.1 MiB |
| 300 KiB 巨大回复 | 首次 GET 27,694 B；补全正文 307,515 B |

上表 B 来自测试里的 `response.arrayBuffer().byteLength`，是**解压后的响应体**，不是 gzip 后的实际带宽；fixture 中大量重复字符也不能代表真实压缩率。UI 打开耗时只量到对应元素可见，不是全部正文稳定可读时间。

调研时 Playwright 所有 project 都使用 Chromium。设置 `isMobile/hasTouch` 后，滚动预算用例实际仍通过 `page.mouse.wheel` 和脚本写 `scrollTop` 操作，不能覆盖 iPhone WebKit 的惯性、回弹和键盘布局变化。

现有翻页测试还有一个测量盲区：它在收到分页响应后才取得用于最终比较的锚点，能证明后续布局稳定，不能严格证明响应落地前后仍是同一阅读位置。应保留原覆盖并补上跨提交的测量。

### 3.2 隔离探针

探针放在 `/tmp/ki-webui-research`，没有写入产品源码或正式测试目录。

1. **加载窗口裁剪与游标断层已复现。** 直接调用现有 `loadHistory → hydrateEntries → applyTail`：构造 `e1…e900` 的完整分支 index，加载 `e401…e900` 共 500 条，游标为 `e401`；tail 刷新后只剩 `e501…e900` 共 400 条，游标仍为 `e401`。下一次 `before=e401` 只能取到更早的条目，中间 `e401…e500` 共 100 条被跳过。
2. **网络等待期间继续滚动存在漂移。** Playwright 暂扣较早一页的响应，用户到顶触发请求后向下滚动 600px，再释放响应。相同 item 的屏幕偏移 `0px → -40px`。这次没有复现整屏跳转，不能据此量化用户报告的最坏情况，但也未达到稳定阅读位置的目标。
3. **跟随问题仍需精确的时序回归。** 高频 fake 流、小步滚轮及延迟 index 探针没有稳定复现“每次都被拉到底部”；它们会受到每批正文增长量和行高量测影响。下面有关 80px/300ms 判定的结论来自源码，不能写成已在真机复现。

## 4. 问题机制

### 4.1 不能继续向上：触发入口和内容连续性都有缺口

`App.loadOlder` 只从 `onScroll` 的 `scrollTop < 96` 分支调用，并要求 `userScrolled` 已被置位。

- 已经停在 `scrollTop=0`、短列表没有滚动空间、分页失败后仍停在顶部时，新的 wheel/touch 意图不一定产生新的 scroll 事件。当前没有常驻“加载更早消息”或顶部原地重试按钮，用户只能寻找其他入口。
- 96px 对高延迟链路太晚，用户到达边界后才等待网络。成功提交或锁释放后也没有独立的“是否仍缺内容”检查。
- 右侧导航走独立的 `jumpToRequest` 循环，每页 500 条，显式关闭 follow，且不使用 `userScrolled`/`olderLock`。所以它能避开普通翻页的部分门槛；这不能证明后端没有历史，也不能替代正常滚动。
- `applyTail` 的 400 条裁剪已经复现断层。被裁掉的锚点会直接消失；index 仍列出这些请求，而导航加载循环也可能从旧游标继续往更早处找，无法补回缺口。
- `applyCursor` 无条件采用 tail 的 `hasMore`，只对 `oldestId` 做部分保护。tail/index 的窗口信息不能直接覆盖用户已经向前翻出的边界。

**结论：** 必须保证所有历史区间可达，不能只扩大顶部触发距离。

### 4.2 跟随冲突：用户意图被几何和时间窗口覆盖

当前 `onScroll`：距底小于 80px 就把 `follow` 设为 true；否则只有距离最近一次手势不足 300ms 才设为 false。随后 `[view.nodes, atBottom, layoutTick]` 变化都可能执行 `scrollTop = scrollHeight`。

- 向上移动 10–60px 仍可能命中“接近底部”，无法明确退出跟随；无需滑满 80px才算用户接管。
- touchend 后的惯性可以超过 300ms；指针按下、内部控件按键与真正向上滚动也不是同一种意图。
- 程序化补偿引发的 scroll 事件同样会命中这些分支；“刚有手势”不等于“这次位移来自手势”。
- `syncFollow` 在折叠或分页完成后再按几何计算跟随，也可能覆盖读历史的意图。
- `follow`、分页锁、待恢复锚点及补全队列缺少统一的 session/branch 世代管理。切换会话时当前代码明确重置的是 `userScrolled`，不能假设其它滚动状态都已重置。

### 4.3 翻页跳动：锚点不是稳定内容，多方同时改位置

`capturePrepend` 在请求发出时记录 `scrollHeight - scrollTop - clientHeight`，新页提交时按旧值恢复。这个量只在“尾部、视口尺寸、原窗口布局和用户位置均不变化”时可靠。

- 弱网等待期间用户可能继续滚动；尾部可能持续输出；图片、补全正文、Markdown 定稿、软键盘都可能改变高度。旧的距底值不再等于当前阅读位置。
- `ChatView` 先在 layout effect 恢复一次，随后最多 1.2s 逐帧检测行位移并写 `scrollTop`；同时虚拟化库做行高补偿，App 做跟随，折叠和导航也会写位置。
- 应用的逐帧补偿无法理解库内部的“延迟到 iOS 惯性结束再补偿”。浏览器原生 scroll anchoring 也未在该滚动容器上明确关闭，是需验证的额外参与者。
- 超过 48 节点时会从普通 DOM 切换到虚拟 DOM。现有修复避开了单纯折叠展开造成的切换，分页增加节点数仍可能跨阈值。
- 只记行顶还不能保证巨大消息内部的某段文字稳定；同一行内部上方的图片或 Markdown 布局变化，需要更细的块锚点或预留尺寸。

### 4.4 性能与带宽的剩余成本

| 位置 | 源码确认的行为 | 优化空间 |
| --- | --- | --- |
| `App.requestIndex` | 打开长会话 250ms 后自动取 index；当前服务端 `fields=index` 还返回 tail 和 runtime | 从首屏延后不等于按需；弱网仍会传全树，并重复传 tail |
| `ChatItem` / `requestHydrate` | 截断 assistant 一挂载就补全，包含 overscan 行；32ms 聚合之后没有完整的 in-flight 去重、取消和会话校验 | 只补读者即将阅读的内容，避免重挂、切会话、弱网下重复请求 |
| `addEntries` | 相同 id 的新对象直接覆盖旧对象 | 已补全正文可能被后到的 slim tail/index 覆盖，再次触发下载 |
| `model.rebuild` | 每次补全/分页/index 更新重新合并 index、遍历分支并建立 nodes/records | 少量内容变化也可能产生全分支计算和对象变化 |
| `ChatView` | `cacheMisses/turnStats/items` 随 nodes 重算；行 props 中还有整张 misses map 和易变化回调；导航高亮按 scroll 扫描 | 虚拟化限制 DOM，但没有消除全窗口派生计算和可见行重复 render |
| Markdown | 缓存的是分块字符串，按 300 项限额；挂载后仍需 React/remark/rehype 工作；多个 idle 回调可能一起到期 | 按字节限制缓存、统一解析调度、限制巨型静态正文工作量 |
| 数据窗口 | 向前翻页期间可不断累积 body，直到 run-end 才裁剪到 400 条 | 长时间读历史时内存无持续预算；粗暴裁剪又破坏阅读和可达性 |
| 图片 | `AttachmentImage` 挂载即 fetch blob，卸载撤销 URL；Markdown 图片没有统一的延迟加载策略 | 先记录实际缓存命中，再做可见性调度、尺寸预留和有界资源复用 |
| 服务端 slim view | 24 KiB 是字段上限；不是 entry/page 总上限；request header、多个 content block 等仍能使一页很大 | 页级字节预算；折叠工具正文的按需投影 |
| SSE | `message_update` 同时带 `message` 与 `assistantMessageEvent.partial` 两份累积消息 | 内存清理与浏览器 16ms 合并不消除已经在线上传送的重复全文 |

## 5. P0：统一阅读与滚动契约

### 5.1 单一控制器，分别记录意图和几何

建议把分散逻辑收敛到 `features/chat` 下的滚动控制 hook/适配器。App 负责会话数据，Chat 负责布局，控制器是程序化位置变化的唯一入口。不要再增加一个与已有逻辑并行写 `scrollTop` 的 effect。

意图状态与 `atBottom` 分开：

| 状态 | 进入条件 | 新内容/尺寸变化的行为 |
| --- | --- | --- |
| `following` | 首次进入会话默认；显式“回到最新”；用户主动向下滚动并实际到达尾部 | 跟随尾部；异步量测也保持尾部可见 |
| `reading` | 明确的向上 wheel、触摸位移、PageUp/Home；拖动滚动条离开尾部；阅读历史/展开历史内容 | 保持当前阅读内容；新输出只更新“有新内容”提示 |
| `seeking` | 点击某条请求导航 | 暂停跟随；加载并定位目标；新手势或新跳转取消旧任务，完成后进入 reading |

关键规则：

1. 向上意图在输入事件阶段立即使跟随失效，不能等滚出 80px 或等下一次 scroll；用 ref 让同帧的异步量测看到新状态。
2. wheel 看 `deltaY`，touch 看实际移动方向与活动触点；区分代码块等内部可滚动元素，忽略文本输入和控件消费的按键。`pointerdown` 本身不表示要读历史。
3. 几何变化仅更新 `atBottom` 和导航 UI，不自动把 reading 改回 following。重新跟随需要明确动作，或用户向下且确实到尾部；可用独立的 8–16px 到底容差，不能复用“显示回到底部按钮”的宽阈值。
4. 使用 `scrollend`（能力检测）或无触点且滚动静默的回退判断惯性结束；不能用“距最后 touchmove 300ms”代替用户状态。[MDN scrollend](https://developer.mozilla.org/en-US/docs/Web/API/Element/scrollend_event)说明滚动完成还要求手势结束；如果根本没发生位移，则不应等待一个不存在的 scrollend。
5. 所有异步工作绑定 `{sessionId, branchGeneration, requestId}`。打开会话、切分支、导航取消时清理锁、timer、补全队列、旧锚点；通过 AbortSignal 和结果世代检查共同防止旧响应进入新视图。
6. 发送新请求、首次打开、回到最新、展开/收起、键盘改变高度，都定义明确的控制器动作。reading 状态不因 run-end、index 到达或 runtime 刷新重新跟随。

### 5.2 优先使用已安装库的内容锚定

锁文件当前为 `@tanstack/react-virtual@3.14.12` / `@tanstack/virtual-core@3.17.10`。已核对本地源码，存在 `anchorTo: 'end'`、`followOnAppend`、`scrollEndThreshold`、`scrollToEnd`，并有触摸/惯性期间延迟调整的 iOS 分支。这次不需要以升级依赖作为前提。

`anchorTo: 'end'` 会在数据变化前找出当前 item key 及 item 内偏移，在更新后的测量表里找到同一 key；它适合 prepend，并非简单保存距底距离。官方也要求稳定 key，避免手写 prepend 的高度差补偿。[TanStack Chat 指南](https://tanstack.com/virtual/latest/docs/chat)

接入必须同时处理这些细节：

- 先用回归探针验证“单独使用库锚定”的结果，再删除 `capturePrepend.tail`、App 无条件尾随 effect 和 Chat 的 1.2s repair 循环；不能先叠加新旧逻辑再调参。
- **仅设置 `followOnAppend=false` 不够。** 当前 core 的 `resizeItem` 在 `anchorTo=end` 且距底小于 `scrollEndThreshold` 时还会独立跟随尺寸增长。reading 必须同时关闭这一路。当前源码把距底值 clamp 到非负数，可在版本限定的内部适配器中验证 reading 使用负阈值关闭自动贴底；它不是上游承诺的布尔开关，必须用本版本回归锁住，不能散落为业务魔数。若验证不能覆盖全部路径，改为适配器显式控制 pinned 判定。
- 保留“视口上方变化应补偿”的目标，但重新验证现有 `shouldAdjustScrollPositionOnItemSizeChange` 覆写与原生锚定的组合；遵循库的 iOS 延迟写入机制，不能自己绕过它。
- 在库接管几何的容器上明确设置 `overflow-anchor: none`，避免浏览器和应用双重补偿；用 Chromium/Firefox 与 WebKit 回归验证。[MDN scroll anchoring](https://developer.mozilla.org/en-US/docs/Web/CSS/Guides/Scroll_anchoring/Overview)
- 聊天优先使用统一的虚拟化实例和稳定 DOM 外壳，小列表也沿同一套几何路径，去掉跨 48 节点时整棵列表换形的风险；同时检查短会话的首屏和键盘可访问性。
- 阅读期间不使用 smooth scroll 做持续修正；导航可以立即定位，动效服从 reduced-motion。软键盘、横竖屏与字体变化也走同一锚定流程。

### 5.3 恢复的是提交前正在看的内容

锚点语义：`{sessionId, branchGeneration, itemKey, offsetWithinItem}`。数据改变时，以最新滚动偏移和上一版虚拟测量表取得锚点；网络开始时只记录请求身份，不能冻结几百毫秒之前的位置。

执行顺序：校验响应世代和游标 → 合并页面 → 在同一次布局提交中恢复同一 key/内部偏移 → 后续 ResizeObserver 量测由同一几何适配器补偿。用户在等待时继续滚动，合并使用其最新位置；用户跳转或切会话，旧恢复任务直接取消。

不要重新引入“从尚未追上 scrollTop 的 DOM 行读取锚点”的老问题。虚拟列表以测量表确定 key，DOM 只在范围与当前 offset 一致后验证屏幕位置。锚点确实因分支切换或折叠消失时，回退到操作对应的折叠行/最近存活邻居，并提供可理解的落点。

巨大消息内部可增加稳定 Markdown block key 和块内偏移。图片预留宽高比例、图表预留占位尺寸，减少“item 没变但里面文字位移”的情况。Safari 的 scrollTop 可能因回弹为负，计算逻辑需容差及范围归一化，不能把回弹误判为新导航。[MDN scrollTop](https://developer.mozilla.org/en-US/docs/Web/API/Element/scrollTop)

### 5.4 分页调度与顶部 UX

- 普通上翻、按钮重试和请求导航共用一个分页协调器。以 session/branch/cursor 去重，同一游标只有一个在途请求；游标推进和数据合并是一个状态更新，提交前不开放重复请求。
- 第一版在明确读历史后，距顶约一屏开始取下一页；根据实测 RTT/滚动速度把预取距离限制在约半屏至两屏。最多预取一页，禁止打开会话就自动追溯全部历史。
- 可用稳定的顶部 sentinel（root 为聊天滚动容器）加范围检查触发；同时在向上手势、请求结束和窗口尺寸变化时复查。sentinel 必须处于真实列表边界，不能跟随某个虚拟 item 卸载。
- 顶部提供固定几何的状态入口：“加载更早消息”/“正在加载…”/“加载失败，重试”/“已到最早消息”。点击目标至少 44px，使用合适的 aria 状态提示；移动端可直接操作，不依赖右侧请求导航。
- 短列表没撑满、compact 后新增页仍没有足够可见行时，用户主动加载后允许有限续取；首版每次意图最多连续两页，仍不足则保留按钮，避免无限拉页。
- 请求失败保持现有内容和锚点，不移动位置；原地可重试。返回空页但 `hasMore=true`、游标不前进时停止自动循环并展示重试状态。
- 导航继续沿既有 `GET /v1/sessions/{id}?before=…` 实现，带加载进度和取消；每次响应使用局部推进的 cursor，不能假定 `setView` 后 ref 已立即更新。最远历史跳转的数据量另行记录，不与首屏混为一个预算。

### 5.5 内容窗口与分页游标不可分割

先修正确性：reading/seeking 时禁止 run-end 裁掉当前阅读窗口；following 下如需淘汰旧内容，必须同时修正可继续读取的边界，不能保留指向已丢弃区域之前的 cursor。

页面状态记录实际覆盖区间及服务端 cursor。`withTurnOpeningUser` 附送的首个 user entry 可能早于真实页边界，不能简单拿最小 entry id 当下一页游标。tail/index 到达只更新它们负责的数据，不回退已经推进的历史边界或恢复过期的 `hasMore`。

后续做内存预算时分离“已知的 id/顺序/页范围”和“完整正文”：淘汰远端 body 可以保留摘要/占位和再次补全能力；淘汰整页须留下可加载的缺口描述。当前锚点及相邻可见内容、正在运行的尾部不淘汰。任何裁剪都必须证明再往上/下读不会跳过一段消息。

## 6. P1：减少渲染、常规传输和内存

### 6.1 index 与正文按需要获取

1. 去掉打开长会话后固定 250ms 的整树请求。首屏和普通连续翻页只依赖窗口；打开全历史请求导航、轨迹、分支功能时才请求相应 index。index 未加载前保留窗口内导航，绝对轮次可以暂缓显示。
2. 在现有 GET 上把 `fields=index` 收敛成只返回 index 所需数据，不附带重复 tail/runtime；明确字段组合语义，同步改客户端和文档。可按 transcript/branch 版本做内存缓存与条件请求，不能用一直变化的 runtime 字段令 index 缓存失效。
3. 正文补全区分可见、即将进入视口、仅 overscan。大 assistant 默认先用 slim 内容，进入阅读区域稳定后或用户明确展开再补；巨型正文提供“加载完整内容”和加载状态，不能悄悄丢正文。
4. 补全队列以 session/id/body revision 去重；维护 queued/in-flight/loaded，支持取消和失败重试。每批不超过服务端 40 个 id，限制并发；小视口首版并发 1，观测后再调整。
5. 已取得完整正文不得被同一 revision 的 slim 响应降级覆盖。pagination、hydrate、tail 和 index 共用合并规则；切会话/分支后丢弃旧结果。

### 6.2 限制每次更新的计算与解析

- 隔离 transcript 与 App 的其它状态，让滚动与布局变更不触发整页组件重算。稳定行回调，向行传自身的 cache miss/branch/edit 数据，而非每次变化的整张映射。
- 分开 branch index、已加载窗口、运行中尾部的派生结果；body hydrate 只更新受影响 entry/node/turn，保留其它对象身份。SSE 同一消息更新只重算该消息和相关统计，避免反复扫描完整历史。
- 视口活动请求通过已缓存的用户节点位置和虚拟 range 查找；每帧最多更新一次，仅 activeId 改变时通知父组件。
- Markdown 使用共享任务队列，优先可见内容，惯性期间压低 overscan 解析优先级。idle callback 只决定何时调度，不能保证随后 React render 的解析工作就在空闲预算内；已结束的超大消息也需按安全语义块分批处理。
- 块缓存同时按条数和近似字节计费；先记录命中率与开销，再评估缓存可复用语法树或 worker 分词。不能直接切字符串破坏围栏、列表、引用、HTML 块和表格语义；复用既有流式边界测试。
- `normalizeMarkdown/settleBoundaries` 仍会看到增长中的整段 source，若 profile 显示其成为热点，再记录已扫描位置做增量处理。不能把“定稿段不再交给 Streamdown 重解析”误当作所有预处理都已增量化。
- 行高缓存按实际文本列宽、布局/字体版本、正文 revision 和折叠状态校验；估算按用户气泡、工具折叠行、纯文本、代码/表格等类型区分。保留实测尺寸，不把占位当真值。
- `directDomUpdates` 可作为当前 React Virtual 适配器的后续 A/B 实验，用于减少仅位置变化的 React 更新；它要求容器与 item 定位归库管理，不能与现有 transform 写法并行启用。只有 profile 和回归证明收益后采用。[React Virtual API](https://tanstack.com/virtual/latest/docs/framework/react/react-virtual)

### 6.3 页级预算和有界缓存

- 为 slim 页增加总序列化字节预算，而非只按 100/500 条计数。按预算缩页仍必须推进真实 cursor，且至少让一条历史可达；单条过大时返回有明确定义的摘要/截断标志，通过现有 entry/entries 取全文。
- 折叠工具默认只需类型、状态、摘要、统计，完整结果可按需加载；request header 完整 system/tools 留给 Inspect。压缩前先减少无用数据，并保持 usage、turn 起点、分支链和 prompt 变化判定正确。
- 给 body cache、Markdown cache 和图片 blob cache 分别设置字节预算；初始值通过固定手机 profile 测量后确定。页数上限不能代替字节上限；也不能通过抹掉当前可见正文“达标”。
- 图片优先补尺寸元数据、延迟加载与取消；需要缓存时优先复用既有 HTTP 缓存，避免无上限保存 blob。所有资源继续使用同源相对路径。
- 第一阶段不引入持久化整树/全文缓存，不默认离线预下载。保留 gzip、哈希 assets immutable 与 SSE 及时 flush 的现有契约。

## 7. P2：流式带宽专项

当前 `loop.streamWithRetry` 给每个 update 填入完整 `Message` 和带完整 `Partial` 的 `AssistantDelta`，SSE 直接序列化整个 event。若 N 个等长增量都实际发给客户端，累积正文成本约随 N² 增长，还可能出现两份相同 partial。服务端 trimming 能减少滞后回放，不能撤回已传出去的数据。

先用 SSE 实际字节计数验证瓶颈，再分两步做：

1. 清理 event 中重复的累积正文表示，统一 CLI、WebUI、extension 订阅者和服务端缓存的消费方式；服务端可合并尚未发出的同消息临时状态，不能丢 message_end、工具结束、usage、steer 或生命周期事件。
2. 在现有 `loop.Event` / SSE 中明确增量与快照：正常流只传某个 message/block 的变化；首次 attach、重连、base revision 不匹配时提供最新 snapshot；message_end 和 jsonl 保持权威完整内容。每次更新携带消息身份和 base/new revision，不能把可能跳号的全局 seq 直接当文本补丁连续性。

必须覆盖 text、thinking、tool arguments、多 content block、替换/重置、错误和中止，不能假设所有 provider 都只 append 字符串。snapshot 和之后 delta 的交接必须原子、有明确 cursor，继续遵守现有事件持久化和关闭顺序，必要时扩展现有形式化/事件测试。这里不另建 REST 路由，不通过压缩 SSE 牺牲实时性。

验收同时检查首次 attach、正常流、慢客户端、重连四种路径的传输量与最终正文一致性。第一步减掉一份 partial 只能改善常数，不能声称已经消除平方级重复。

## 8. 验证矩阵与目标预算

以下保留原始实施目标；已完成的自动化测量及仍未验证的范围见第 10 节。性能数字绑定相同 fixture、浏览器、CPU/网络配置，不用开发机本地耗时推算弱网真机结果。

### 8.1 必须补齐的行为回归

| 场景 | 核心断言 |
| --- | --- |
| 已在顶部继续上滑；短窗口/compact 不满一屏 | 更早历史可达；无 scroll 事件时仍可通过意图/按钮加载 |
| 请求失败后仍在顶部；快速连续触顶 | 原地重试可用；相同 cursor 无并发/重复提交；无自动重试风暴 |
| 分页人为延迟 800ms/2s，等待时继续滚动 | 以提交前正在阅读的同一 item/block 为基准，无退回请求时位置 |
| 分页期间尾部 streaming、run-end、index 到达 | 视口与历史 cursor 均不被这些更新抢占 |
| 距底 10/30/60px 的向上动作，同时低频/高频 streaming | 第一个明确向上意图即暂停跟随，后续更新不拉回；主动回到底部恢复 |
| touchend 后惯性超过 300ms，手指持续拖动、回弹 | 应用不在惯性中反复写位置；新手势可立即取消导航 |
| loaded entries 从 500 被限额，或已翻到起点后 tail 刷新 | 锚点存活；相邻历史没有洞；cursor/hasMore 不回退或跳段 |
| 打开/切换 A→B、切分支，同时 A 的分页/补全/index 返回 | B 不混入 A 的内容、锚点、loading 状态或游标 |
| 47/48/49 节点、折叠展开、补全、图片迟到、巨型消息 | 稳定 DOM 与锚点；无阈值切换导致的重挂跳顶 |
| 横竖屏、软键盘开关、浏览器地址栏收缩 | following 保持尾部；reading 保持可识别内容；按钮可触达 |
| 浏览全历史再来回阅读；点击很早的请求后立即点击另一个 | 所有请求可达；旧导航取消；正文/页请求去重；内存可回落 |

### 8.2 如何测量

- 在响应释放/数据提交**之前**记录当前 item 或 block 的稳定 key 与屏幕偏移，之后一直跟踪同一内容，避免先跳错再以错误位置为基准。
- 分开记录用户滚动、内容布局变化和程序补偿；`scrollTop` 改变本身不是失败。静止阅读时看同一内容的屏幕偏移；有输入时用受控手势轨迹及逐帧标记评估额外位移。
- 若锚点被合法滚出虚拟窗口，明确重新建立测量段；如果因错误裁剪消失则直接失败，不能忽略该帧当作通过。
- 跟随回归用可控制节奏和高度增量的流，不能只用每批增长数千像素的 fake burst；现有 burst 测试继续保留。
- 同时统计 API 请求次数、in-flight、decodedBodySize、encodedBodySize、transferSize 和 SSE 字节。encodedBodySize 表示压缩后的 body，transferSize 还涉及响应头及缓存语义。[MDN encodedBodySize](https://developer.mozilla.org/en-US/docs/Web/API/PerformanceResourceTiming/encodedBodySize)
- 渲染记录可见/overscan 行数、React commit、Markdown 解析、long task、帧间隔及 heap；HUD 增加意图状态、当前锚点、分页 cursor 和调整原因。诊断只记录标识/计数，不收集正文。

### 8.3 建议验收门槛

| 指标 | 第一阶段目标 |
| --- | --- |
| 静止阅读时 prepend/补全后的同内容偏移 | 稳定尺寸场景 ≤ 2 CSS px；异步 Markdown/图片场景 ≤ 8px；不得整行/整屏跳转 |
| 用户向上后继续输出 | 无自动恢复 following；内容自然增长不得导致尾部跳转 |
| 翻页正确性 | 连续读到最早消息，无缺口；同 session/branch/cursor 同时最多 1 个请求 |
| 默认打开长会话 | 0 个 before 请求；未打开需要全树的功能时 0 个全量 index 请求；纯 overscan 大正文不自动补全 |
| 页面传输预算 | 先试行 slim 页 decoded ≤ 512 KiB；在固定、非高度重复的文本 fixture 上 encoded ≤ 128 KiB；全文下载单独计量，超大单条走明确的补全流程 |
| 弱网 profile | 1.6 Mbps 下行、750 Kbps 上行、800ms RTT；另加 2s 延迟及断连恢复；Chromium CPU 4× 作为可重复实验配置 |
| 稳定滚动渲染 | 固定 profile 下 p95 帧间隔争取 ≤ 32ms；应用解析不产生 > 100ms 单个 long task；未达标需归因和调整，不能通过跳过用例放宽 |
| 内存 | 400/2000/5000 turn 反复阅读后 body/解析缓存按配置字节预算淘汰，卸载冷内容后趋于平台；可见区和直播尾部允许的例外单独计量 |
| SSE | 同等正文长度下显著减少重复传输；增量方案上线后，随输出长度/事件数近似线性增长；attach/reconnect snapshot 单独统计 |

原有 `test:e2e`、`responsive.spec.ts` 和 `test:perf` 全部保留。增加 WebKit 与 Firefox 的对应测试，Chromium 的 CPU/网络仿真不能替代它们。发布前至少在 iPhone Safari、Android Chrome、桌面触控板各做一次真实 provider + 端口转发链路验证；真机通过 `scripts/run.sh` 使用真实 provider，fake 只用于隔离自动化测试。

## 9. 实施拆分

- [x] **M1：正确性回归与窗口修复。** 把 400 条裁剪断层、顶部无位移/重试、迟到响应和提交前锚点场景纳入正式测试；修复覆盖区间、cursor/hasMore 与世代管理。涉及 `model.ts`、`App.tsx`、API 请求取消。
- [x] **M2：统一滚动与分页 UX。** 接入并验证库的内容锚定，落实 following/reading/seeking；删除竞争的滚动写入；稳定短/长列表 DOM；增加顶部入口、提前加载及导航取消。自动化覆盖跨浏览器与响应式行为；真实移动惯性、回弹和软键盘验证仍待真机。
- [x] **M3：按需数据与渲染预算。** index 按需和精简响应；正文去重、取消、完整度合并；局部派生与稳定行 props；Markdown 调度、图片尺寸、字节缓存和页级预算。每项记录变更前后的同场景数据。
- [x] **M4：SSE 传输。** 先计量并去掉重复 partial，再实现有快照恢复的增量语义；同步覆盖 CLI/WebUI/extension 消费者以及重连、回放、中止和事件顺序。

各阶段实施时同步更新已有的 `docs/webui.md`、涉及的 `docs/session.md`/`docs/events.md` 以及所属 Go 包 `doc.go`；修复点添加解释原因的英文注释。滚动问题已复发，落地后补充既有复盘或新增后续复盘，明确旧修复未覆盖的时序及防回归方式。方案中的候选参数最终以测量和真机结果收敛。


## 10. 实施结果（2026-09-27）

### 已落地的行为

- `useTranscriptScroll` 统一阅读意图与分页触发，向上手势立即停跟随；顶部提供 44px 加载/重试入口，不依赖 scroll 事件或请求导航。每次向上意图最多连续取两页，失败停下等待重试。
- `useTranscriptRequests` 统一普通翻页、导航和正文请求；同游标去重，按会话/分支取消旧请求，提交响应时推进本地 cursor。取消 run-end 的 400 条窗口裁剪；index 不改 leaf/正文/窗口，完整正文不被 slim 降级。
- 短长列表使用同一虚拟化外壳。由现有库按提交时的内容 key 锚定，删除业务层逐帧 scrollTop 修复；固定 `getItemKey` 回调和每行初始估算，避免量测学习隐式改变其它行位置。顶部留白中的锚点也按原 key 补偿，富文本翻页用例收紧到 8px，普通延迟页用例保持 2px。
- 整树索引按需获取，纯 `fields=index` 只返回索引并支持 ETag；正文仅实际可见或主动展开后补全，批量去重、并发 1。后端 slim entries 页预算 512KiB，工具预览 1KiB，大 entry 返回摘要并通过既有正文接口取全文。
- 不变的 node/record 保留对象身份，行回调稳定，活动请求高亮改为位置二分查找。Markdown 共享队列每帧放行一个任务、静态大正文安全分段；正文与 Markdown 缓存各使用约 8MiB 预算，冷正文退回可再次补全的摘要。图片接近视口才请求、卸载取消；全屏预览独立于虚拟行存活，避免回收行时关闭图片。
- SSE 在每条连接上先发消息快照，之后选用更小的 append/set/remove/resize patch；baseSeq 指向前一个消息帧，允许全局 seq 间隔。重连从新快照恢复，CLI/WebUI 解码后得到完整消息；jsonl、扩展和服务端 canonical 事件继续保留完整消息，不压缩 SSE。

### 自动化结果

| 检查 | 结果 |
| --- | --- |
| 全仓 Go 测试（含 CLI / WebUI fake 集成） | 通过；WebUI fake 矩阵 152 项，含原响应式覆盖 |
| TypeScript / Vite build / go vet | 通过 |
| WebKit / Firefox 滚动专项 | 各 8 项，共 16/16；没有跳过失败用例或放宽 2px 锚点断言 |
| 原性能套件 | 3/3；400 turn、390×844 的富文本 prepend 锚点 60px → 60px，两个滚动采样段 long task 均为 0 |
| 弱网 + CPU 4× 的同一 GET/UI 用例 | 1/1；下行 1.6Mbps / 上行 750Kbps / 延迟 800ms，未放宽原预算：tail GET 828ms，index 926ms，长会话 UI 1,368ms，300KiB 回复 UI 1,783ms |
| SSE 增量计量 | 100/200 个等长 chunk：26,678 / 53,478 B；输出翻倍时线上体积约翻倍，最终解码正文一致 |

固定 200 turn 历史夹具的响应体比较（同一 fixture，B 为字节）：

| 请求 | 调研基线 decoded | 实施后 decoded | 实施后 encoded / transfer |
| --- | ---: | ---: | ---: |
| 默认 tail GET | 237,447 | 58,457 | 4,066 / 4,366 |
| index | 492,591 | 255,185 | 21,556 / 21,856 |
| 较早一页 before | 234,075 | 57,434 | 3,556 / 3,856 |

前后 decoded 分别减少约 75%、48%、75%；基线没有 encoded 记录，不能把这些比例称为实际带宽降幅。正文里有重复内容，压缩率也不能直接外推到任意真实会话。默认打开长会话已不请求全树索引。300KiB 单回复仍可通过原正文接口获取完整内容，普通 profile 下 UI 打开约 142ms、heap 18.1MiB；此时间只量到目标可见。

### 采用边界与后续测量

- 8MiB 正文预算不包含最小摘要和树元数据；可见/邻近行和最新 turn 允许超出。没有丢弃历史 id 来压低内存，也未宣称 5000 turn 真机 heap 已达平台。
- `rebuild` 仍遍历分支并复用不变对象，尚未改成整套 O(1) 增量投影；Markdown normalize/boundaries 仍处理全文。worker、语法树缓存、directDomUpdates 都是方案中需 profile 后再决定的候选，本轮未引入。
- 图片复用浏览器 HTTP 缓存；未额外保留全局 Blob 缓存，只有挂载行与当前打开的预览持有资源。
- Playwright WebKit / Firefox 是真实浏览器引擎自动化，并非 iPhone / Android 真机。真机触摸惯性、回弹、软键盘和横竖屏，以及真实 provider + 端口转发链路尚未执行；自动化结果不能替代这些验收。
- 已同步现有 WebUI/session/events 文档、相关包 doc.go 和既有滚动复盘。测试均使用隔离 fake 服务，未重启正常开发服务。


## 11. Compact 整轮分页与中间帧修复（2026-09-27）

用户补充明确：整轮分页用于 compact；detailed 仍按定量 entry 加载。前一版两种模式共用 entry cursor，长 turn 的隐藏区会被逐页下载，折叠计数也随之增加，属于分页契约错误。

本轮落地：

- compact 首次最多 4 个完整 turn，每次上翻 1 个完整 turn：input + compact 摘要 + 最后 N 个详细节点。`oldestId` 指向该轮输入，隐藏正文不进入普通分页；正在等待该页时重复 wheel/touch 不再排队追加一页。
- 摘要携带完整折叠数量、整轮统计、累计步骤与最新步骤用量。可见工具保留 call/result 配对，共享 entry 中隐藏的 assistant 正文仍省略。keep=0、分支、20 个大回复及 512KiB 页预算均有后端回归。
- 点击展开通过现有 GET 的 `turn` 参数分页补该轮，局部游标不推进历史 cursor，收齐后一次发布。调整 keep 重取投影；切换 detailed 补缺失正文，已完整加载或展开的 turn 不因转换丢失。
- 运行中会话的 SSE attach 携带已读快照 leaf，服务端过滤摘要已覆盖的消息/已完成工具；保留并发未完成工具及新消息，生命周期边界不再重复夹带整轮正文。
- iPhone UA 的 WebKit 复现到约 500px、持续约 150ms 的中间帧闪跳。根因是程序补偿的 scroll 被当作触摸惯性，Markdown 的 transform 先更新、scrollTop 后更新。分页等待真实手势静止再提交，offset observer 区分真实输入与程序补偿，仍由虚拟列表库独占几何补偿。

新增场景用每轮 260 次工具往返、520 个隐藏节点的 9 轮会话。普通分页只收到输入与最终回复两条 entry；显式展开才请求该轮 522 条 entry。连续三次翻页均在释放响应前锁定内容 key，再逐动画帧量测，最大偏移必须 ≤2px；另覆盖按住触点时响应到达，不能只比较最终恢复后的位置。

WebKit iPhone 仿真使用 DOM 触摸事件和受控位移，验证库的 iOS 路径，不代表已完成真机惯性、回弹或弱网端口转发验收。所有自动化使用隔离测试服务，正常开发服务未重启。


本轮验证：

| 检查 | 结果 |
| --- | --- |
| Chromium 全量（含完整 responsive matrix） | `bun run test:e2e`，155/155，29.0s |
| WebKit / Firefox 滚动与 compact、iPhone WebKit compact | 25/25；补强连续手势后 compact 三项目再跑 9/9 |
| 性能预算 | `bun run test:perf`，3/3，24.5s；400 turn 分页锚点 `60px → 60px`，上滑/重访 long task 都为 0 |
| 弱网仿真 | `KI_PERF_WEAK=1` 指定性能用例 1/1，34.1s；1.6Mbps 下行、800ms 延迟、CPU 4×，未放宽预算；历史 UI 1341ms、300KiB 回复 UI 1774ms（量到目标可见） |
| Go | `go test ./...`、`go vet ./...` 通过；包含 compact 预算、展开游标、分支、SSE 快照/并发工具回归 |
| 构建与类型 | Vite build、embed 构建、`bunx tsc --noEmit` 通过 |

性能套件保留 detailed 定量分页预算：200 turn 首次返回 100 条 entries，decoded 58,457B、encoded 4,078B；普通较早页 decoded 57,434B、encoded 3,559B。该 fixture 带重复内容，不能把压缩比例外推到真实会话。compact 的新增用例验证每次跨过一个完整 522-entry turn，只传 input 和 final 两条正文，且响应与普通分页的后续请求均不含隐藏正文。
