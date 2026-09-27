# 展开折叠的消息把读者甩回窗口顶部：两次误判成「锚定不够」

日期：2026-09-27  
范围：`web/src/features/chat/Chat.tsx`（`foldReplies` 渲染列表、`virtualize` 判定、`toggleFold`）、`web/src/App.tsx`（`syncFollow`）、`web/src/lib/messageView.ts`

## 现象

compact 消息显示里点开一个折叠行（手机上折了 164 条消息），视口**跳到前面某条不认识的消息**，读者失去位置。「展开和折叠不都应该固定在本来的位置吗」——两次回复「修好了」，两次都不算修好。

## 时间线

1. **第一轮：只按桌面 + 小折叠复现**。用折叠 1~2 个节点的场景测量，发现折叠行本身其实没动（`rowTop` 150 → 150），于是判断真凶是跟随尾部：展开把尾部推到视口外却没有 scroll 事件，`atBottom` 仍是 `true`，下一个 delta 触发的 follow-tail 把视口拽回底部。修法：`toggleFold` 记下行的视口偏移并在同一个 layout effect 里还原（`foldAnchor`），`syncFollow()` 把「是否在尾部」改成按几何重算。加的回归用例（fake 服务 + `e2e-delay` 保持 run 进行中）确实覆盖了这条路径——但它**只在折叠很小的时候成立**。
2. **第二轮：仍然跳**。按用户规模重建场景才看清：手机视口（390×844）、一个 turn 里塞 90 个节点、折叠行上方还有内容（`scrollTop = 300`）。点击前 `rowTop 624`；点击后 `scrollTop` 变成 **0**，行被推到 `860`。
3. **定位**：展开前那 90 条消息全在折叠行里，渲染列表只有 ~6 行，走的是**非虚拟**分支；一点展开，渲染行数涨到 96，跨过 `VIRTUALIZE_AFTER = 48`，切到**虚拟**分支（绝对定位 item + 定量高度容器）。两条分支的 DOM 结构不同，切换等于重挂整棵列表，`scrollTop` 被夹回 0 —— 在手机上就是「跳到前面」。
4. 桌面 + 小折叠为什么看不到：折叠太小，`items.length` 根本没跨阈值，从未发生分支切换。

## 根因

两层，第二层被第一层的修法掩盖了：

1. **跟随尾部的状态是陈旧的**：展开折叠行会改变布局却不产生 scroll 事件，`atBottom` 停留在 `true`，随后任何一个 `view.nodes` 变化（streaming delta、后台 refetch）都会执行 `scrollTop = scrollHeight`，把正在看旧消息的读者拽到最底部。
2. **渲染列表的虚拟化判定依赖折叠状态**：`virtualize = items.length > VIRTUALIZE_AFTER` 让「一行的折叠/展开」可以决定整棵列表的 DOM 形态。虚拟/非虚拟两条分支的 DOM 不同（普通子节点 vs. 绝对定位 item 装进定量高度的容器），切换会重挂列表并把 `scrollTop` 夹回 0。折叠行藏得越多，越容易跨过阈值 —— 也就是**折叠功能本身最容易触发它**。

底层教训：`scrollTop` 的重置不是「锚定没做好」，而是**结构性重挂**。当时只盯着「被点的那个元素在哪」，没有问「列表的 DOM 形态会不会变」。

## 修复

- **判定与折叠状态解耦**：`virtualize = Math.max(渲染行数, 节点数) > VIRTUALIZE_AFTER`（`Chat.tsx`）。折叠行只增加渲染行、不减少节点数，因此展开/收起不再改变判定；阈值只在窗口本身变化时被跨越。注释写明了原因（两条分支 DOM 不同、切换会夹 `scrollTop`）。
- **行的视口偏移还原**（第一轮，保留）：`toggleFold` 记录行相对滚动容器的偏移与 turn id，layout effect 在 paint 前把它放回去；同一处调用 `onLayoutChanged`。
- **跟随尾部按几何重算**（第一轮，保留）：`App.tsx` 的 `syncFollow()` 同时用于 `onScroll` 与折叠切换回调，展开后不再假装还在尾部（「回到底部」按钮随之出现）。

## 验证

- 新回归用例 `web/e2e/message-view.spec.ts › big fold on a phone › opening a fold that hides a whole long turn keeps the reader in place`：把一份「一个 turn 90 个节点」的合成会话写进 Playwright fixture home（与 `webui.spec.ts` 放 skill/extension fixture 同一套做法，fake 模型造不出这种 turn），在手机视口里点击折叠行，断言 `rowTop` 与 `scrollTop` 都不变、展开内容确实出现。
- 反向验证：把判定改回 `items.length > VIRTUALIZE_AFTER` 再跑，用例失败（`rowTop 213 → 610`），说明它抓得住这个 bug。
- 顺手修掉旧用例里的 race：它靠轮询恰好命中「turn 1 已完成、turn 2 还未渲染」的瞬间（`toHaveCount(2)`）而假通过，现在先等 `2 轮` 稳定再按 turn 结构断言。
- `bun run test:e2e` 128/128、`tsc`、`go vet`、`go test ./internal/...` 全绿。

## 教训

- **按用户的规模复现**：同一个 turn 上百个节点、手机视口、折叠行上方有内容。小折叠 + 桌面「测过没问题」并不构成证据；这次两个 bug 的触发条件都恰好被小折叠绕开。
- **断言要看几何，不只看有没有渲染**：`rowTop`/`scrollTop` 不变才是「固定在本来的位置」；「折叠里的节点出现了」这种断言对跳转完全无感。
- **布局分支切换是重置滚动的高危操作**：任何会让页面在「虚拟/非虚拟」或其它 DOM 形态之间来回切换的判定，都会连带上一次重挂；判定输入里不能混进用户交互状态。
- 依然存在的老行为（本轮未动）：窗口本身首次越过阈值时（例如流式输出把 `nodes` 从 47 推到 49）仍会切换一次分支并夹一次 `scrollTop`；正在翻旧消息的读者会看到一次性小跳。根治要让两条分支共用同一套 DOM 结构（或始终走虚拟列表）。
