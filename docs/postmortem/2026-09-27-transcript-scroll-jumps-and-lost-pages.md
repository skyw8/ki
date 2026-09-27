# 上滑乱跳、翻历史被传送、滚动卡顿：一次 mount 的解析与一笔上千像素的账

日期：2026-09-27  
范围：`web/src/features/chat/Chat.tsx`（虚拟列表几何）、`web/src/lib/rowHeight.ts`（行高缓存）、`web/src/features/markdown/Markdown.tsx`（解析时机）、`web/src/App.tsx`（分页、跟随尾部）

## 现象

同一个长会话（400 turn，行高 100–1400px）在手机视口下：

1. **上滑乱跳**：手指往上滑，画面却经常停在原地，或者突然窜出去一段。
2. **翻历史被传送**：滑到顶、分页加载更早的消息之后，视口落在**新拉进来那页的顶部**（≈25 个 turn 之前），不是原来的位置。
3. **卡顿**：上滑 14 步（≈7000px）就有 5 个 long task，最长 451ms，主线程阻塞合计 860ms；同一段窗口来回扫三趟，long task 合计 1669 → 3223 → 5229ms。
4. **打开会话慢**：零输入打开会话，t≈879ms 就发了一次 `?before=`，白拉一页 ~100 条。

## 时间线

1. 先量：`estimateSize` 是固定 96px，实测行高 p50 118px / max 1391px —— 低估 14 倍。逐帧采样（手机视口，滚轮 −400px/步）看到 `scrollTop` 出现 +1151px 的补偿帧与 +755~+4972px 的反向跳。
2. 逐帧确认「乱跳」的真身：单帧里**行的屏幕位置移动 1316px 而 `scrollTop` 完全没动**（`sent 370→1686, top 28268→28268`）。即：视口上方某行从 96px 估算变成真实高度，整列下移，而滚动补偿没有跟上。
3. 读库实现：`@tanstack/virtual-core@3.17.10` 的 `defaultShouldAdjust` 对**重新量测**要求 `itemStart + itemSize <= scrollOffset **且 `scrollDirection !== "backward"`** —— 上滑时直接跳过补偿。行高未知 + 上滑不补偿 = 每次「第一次进入某行」都欠一笔账。
4. 再查分页：`loadOlder` 用 `requestAnimationFrame` 做 `scrollTop = prevTop + (scrollHeight - prev)`。抓 rAF 与提交的先后：`?before=` 在 t=4153 返回，紧接着应用自己的 rAF 回调里 `scrollHeight` 仍是 30230（旧值），之后才长到 45334 —— 补偿算成 0。React 18 对这个 100 条的大更新会让出主线程分片渲染，commit 落在帧回调之后。
5. 顺手发现分页补偿即使生效也偏：新行此刻还是 96px 估算（估算增量 +9600 vs 真实 +15104）。
6. 卡顿归因：CPU profile 榜首是 `visitNode`/`visitChild`（remark/rehype 遍历），不是 layout、不是 React diff。`Markdown`/`Streamdown` 的 memo 只在同一实例更新时生效，虚拟列表一卸载一挂载就全部重跑；`overscan: 10` 让视口外 ±7000px 的内容也解析。
7. 修完上面几条之后出现一个**被掩盖的老 bug**：打开会话停在 `scrollTop 0`。第一次 follow 效果跑在虚拟列表还没有高度的那一帧（落点 0），此后不再触发；过去是因为每次打开都会白拉一页，`view.nodes` 变化顺带把 follow 效果又叫了一次 —— 假补偿掩盖了真顺序错误。

## 根因

- **行高必须在第一次 mount 后才测得出**，而估算与真实差一个数量级；每次修正都要移动整列，上滑时修正落在读者正要看的行上。
- **虚拟化库在上滑时不补偿重新量测**（`scrollDirection !== "backward"`）。
- **分页补偿跑在 React 提交之前**（rAF vs 并发渲染的让出），读到旧高度；且用高度算术而不是「读者与内容的相对位置」。
- **markdown 解析绑定在 mount 上**，没有缓存，且 overscan 把视口外的行也算进解析预算。
- **分页触发条件只看 `scrollTop < 96`**，把首帧/量测产生的程序化滚动当成读者意图。
- **打开时不触发 follow**：follow 效果依赖 `[view.nodes, atBottom]`，而列表高度在下一帧才出现。

## 修复

- `lib/rowHeight.ts`：按 (item key, 列表宽度) 记住实测高度（LRU 2000，宽度变了作废），没测过的行用 px/char 滑动平均估算；`estimateSize` 先查缓存。`measureElement` 覆写为「量到就记」，但**跳过占位高度**（`[data-md-pending]`）。
- `Chat.tsx`：覆写 `shouldAdjustScrollPositionOnItemSizeChange`（该 build 从实例读取，不是 options）为「变化的行完全在视口之上就补偿」，方向无关；`overscan` 10 → 4；`data-item-key` 暴露给量测与测试。
- `Markdown.tsx`：先渲染占位（按估算高度撑住），在 `requestIdleCallback`(timeout 200ms) 里渲染正文，流式消息立即解析；已定型文本的分块结果按文本 LRU 300 缓存。
- `App.tsx`：`loadOlder` 加「用户真的滚动过」门（window capture 监听 + 目标在滚动容器内）；分页锚点改成「与底部的距离」（`capturePrepend` 只记 `scrollHeight - scrollTop - clientHeight`），在渲染新列表那次 commit 的 layout effect 里、paint 之前还原；随后用「顶部那行不得相对滚动移动」的逐帧校正收掉量测漂移（≤1.2s）。
- `App.tsx`：`follow` ref —— 只有「手势之后的滚动」或「有意改变布局」才释放，内容自增长不释放；`onContentResized`（虚拟列表总高度）重跑 follow 效果；请求导航跳转显式释放。
- 真机自检浮层 `features/chat/PerfHud.tsx`（`localStorage['ki-perf-hud'] = '1'`）。

## 验证

- 新 `web/e2e/scroll-budget.perf.spec.ts`（手机视口、合成 400 turn 会话）：打开必须落在底部（`top + client ≥ height − 2`）；打开不得出现 `before=` 请求；分页后读者所在那行位移 ≤ 64px（修复前是一整页 ~60,000px），单帧内容位移 < 600px；上滑每一格滚轮内容位移正好 400px（首次 ±150，重访 ±80）；上滑期间 long task 数上限与单帧上限。
- 修好后实测：打开 `top 60490 / height 61099`（贴底）；分页 `-71 → -71`（0px）、最差一次 48px；上滑 `steps=[400,400,400,…]`、`worstLurch 400`（就是滚轮本身）、`longTasks 0`。
- 夹带修掉的老 bug 用同一条断言锁住：打开会话必须贴底。
- `bun run test:e2e` 128/128、`bun run test:perf` 3/3（重复跑 6/6）、`tsc`、`go vet`、`go test ./internal/...`。

## 教训

- **量测要量对东西**：`scrollTop` 的跳变不等于画面跳变（补偿会让 `scrollTop` 动而内容不动），「行在屏幕上的位移 + 滚动位移」才是。这一条差点把「上滑乱跳」误判成「手势被吞」。
- **别在 DOM 可能过期时读几何**：一次性跳转后一帧内，虚拟列表挂载的行仍按旧滚动位置排布，读出来的偏移属于另一帧（实测 56094px）。要么等窗口追上（行真的跨过视口顶），要么用与 DOM 无关的量（与底部的距离）。
- **不要用估算总和当不变量**：`scrollHeight` 含未挂载行的估算值，量测改变 px/char 时它会整体变化，而读者看到的内容没动 —— 用它断言会误报。
- **测试的触发方式要离散**：滚轮一步会分几帧应用，分页请求可能落在中间，读者的剩余位移会让「请求时刻的位置」无法比较。
- **修掉一个 bug 会把被它掩盖的另一个露出来**：白拉的那一页顺带掩盖了 follow 效果跑在「列表还没高度」那一帧的问题。

## 后续修复：读取意图和内容锚点

同日复查发现原修复仍有空白：已经到顶的 wheel/touch 不产生 scroll，失败后无法重试；80px 的几何贴底规则抢回小幅上滑；请求开始时记录距尾距离，在慢响应和尾部流式增长时已过期；run-end 裁掉 400 条外的内容但不修 cursor，让一段历史不可达。原性能测试在响应以后才捕获锚点，会把已经跳错的落点当成正确基准。

后续实现以 `useTranscriptScroll` 统一读取意图，以 `useTranscriptRequests` 统一分页/正文/index 去重和取消。所有长度的列表共用虚拟化 DOM，内容 key 的提交时锚点及量测补偿由 virtual-core 执行，包括其 iOS 延迟写入。reading 同时禁用 append follow 和 resize pinning。顶部入口提供加载与失败重试，短窗口也能通过上滑意图触发。正文内存淘汰保留所有 entry id 和边界；完整正文不能被 slim 响应覆盖。

加强测试后又发现顶部留白的边界：原窗口 scrollTop=0、首行位于 60px；prepend 后这 60px 变成上一行的尾部。单纯 `item.end <= scrollTop` 会漏掉这个逻辑上位于内容锚点之前的行，导致估算/解析时少补一笔。prepend 保留内容 key，量测按它的顺序判断上方；新用户意图立即解除该锚点。普通量测区分首次估算和重测，避免重新引入尾部 streaming 拉动读者。

新增回归在响应释放前记录 key/offset，且等待期间主动移动阅读位置；另测 10/30/60px 上滑、触顶无位移、失败重试、A→B 时分页/补全/index 同时迟到、500 条窗口连续性和正文退化。旧性能用例也改成先锁住网络、记录锚点再释放，保留原有全部滚动/渲染预算。共享 Markdown 队列替代每行独立 idle 回调；页/正文/解析缓存按字节计费；SSE 用连接内快照与字段补丁减少累积全文重复。

仍有 24px 漂移时，逐条记录 resize delta，发现 `getItemKey` 的新函数身份令库反复重建测量表，而 px/char 学习又改变了尚未量测行的估算高度。那些位置变化没有对应的 `resizeItem` delta，自然没有补偿。修复为稳定 key 回调、每行/正文长度/列宽固定一份初始估算，实际高度仍由库更新。400 turn 富文本场景变为 `60 → 60`，原 64px 断言收紧为 8px；上滑和重访的 long task 均为 0。


## Compact 整轮分页与 iOS 中间帧修复

前一轮仍按 entry 数分页，然后才在前端折叠。一个几百步的 turn 会跨越多个 page：附带 input 无法把窗口变成完整 turn，继续上翻只增加同一折叠行的计数，还把不可见正文传到手机。compact 改为服务端按整轮投影，每次上翻直接越过一轮，只带输入、折叠摘要和最近 N 条详细回复；展开才走独立 turn 请求。detailed 保留计数/字节分页。

运行中的会话还有第二条入口：SSE 的 message_end 和工具重放保留完整正文，客户端以“是否已有 node”去重，而 compact 正好不持有隐藏节点。修复让连接携带已读快照 leaf，在服务端过滤其已持久化消息和已完成工具，生命周期帧不再重复携带整轮正文。并发但未完成的工具仍需重放 start/args；回归同时验证其事件和快照之后的新消息不丢失。

此前只比较分页前后最终位置，未覆盖 WebKit 的中间帧。新增 iPhone UA + 触摸事件用例后复现：初次 prepend 补偿使 scrollTop 从 0 到 966，库把这个程序 scroll 当作 iOS 惯性；随后新 Markdown 量测把行位置缩短约 500px，但对应的 scrollTop 修正延迟约 150ms，读者先看到整轮跳走，然后才回到原处。最终锚点断言会放过它。

修复分两部分：有活动触点/惯性时仅完成网络读取，待手势和库状态静止后再提交分页；offset observer 仅将真实输入驱动的滚动标为 scrolling，程序补偿后的量测仍同步提交。保持库负责几何，没有恢复业务层逐帧写 scrollTop。回归按动画帧连续跟踪同一 key，不能用“最后回到原位置”代替无闪跳；520 条隐藏回复的大 turn 连续分页，以及按住手指时页到达均纳入测试。WebKit 仿真验证的是 iOS 代码路径，仍不能替代真实 Safari 的物理惯性和回弹验收。
