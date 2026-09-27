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
