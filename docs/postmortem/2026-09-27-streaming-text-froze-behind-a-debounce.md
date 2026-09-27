# 流式正文冻在一个 debounce 后面

日期：2026-09-27  
范围：`web/src/features/markdown/Markdown.tsx`（`useStreamingText`）  
引入：提交 `5222560`（"keep the reader's place and the frame while the transcript scrolls"），同一天修掉。

## 现象

加了滚动修复之后，流式正文 **整段冻住**：一条回复从第一个 delta 到 provider 停顿之间都不动，然后在停顿的瞬间整块跳出来（实测：气泡从 1.2 KiB 一步跳到 55 KiB）。用户侧看起来像"输出卡住了"。

## 根因

那次提交为了限制"每条 delta 都重新渲染并重新解析整段 markdown"，加了一个 150ms 的渲染节流：

```tsx
useEffect(() => {
  if (!streaming || text === shown) return
  const id = window.setTimeout(() => setShown(text), STREAM_RENDER_MS)
  return () => window.clearTimeout(id)          // ← 每次 text 变化都清掉上一个定时器
}, [text, streaming, shown])
```

**每次 delta 都重置定时器**，这不是节流（throttle），而是防抖（debounce）：只要求下一次 delta 在 150ms 之内到达，`setShown` 就永远不会执行。真实 provider 的 delta 间隔是几十毫秒，假 provider 每 25ms 一批，所以正文只在 provider 停顿（或本轮结束）时才更新一次。

## 修复

按"上一次渲染的时刻"算 deadline，而不是每条 delta 重新计时：

```tsx
const renderedAt = useRef(0)
const wait = Math.max(0, STREAM_RENDER_MS - (performance.now() - renderedAt.current))
const id = window.setTimeout(() => { renderedAt.current = performance.now(); setShown(text) }, wait)
```

这样在持续流式下大约每 80ms 更新一次，且最后一次一定落地。

## 为什么测试没抓到

当时那条 e2e（"a burst of streaming chunks keeps the main thread responsive"）**只看两件事**：本轮是否跑完、长任务总量是否达标——而正文冻住恰好让长任务更少、跑完也没问题，所以它一路绿灯。**只测终点状态的性能用例，会把"少做了工作"和"根本没做工作"混为一谈。**

## 教训

- **节流和防抖差一个前提**：节流是"两次渲染之间至少隔 T"，防抖是"变化停止 T 之后才渲染"。写节流时永远按**上一次执行的时间**算下一次，而不是按每次变化重新计时。
- **性能优化要有一条"行为没变"的断言兜底**：现在 `web/e2e/markdown-stream.spec.ts` 的 "keeps the text moving while it streams" 在流式过程中采样渲染出来的正文长度，要求出现 ≥5 个不同长度、且最后一个接近全文——正是这条断言能当场抓住上面那个 bug。
- **"少渲染"永远不该以"看不见进展"为代价**：真正省下来的应该是**每次渲染解析的文本量**（只解析未定稿的尾部，见 `features/markdown/streamText.ts`），而不是渲染次数本身。
