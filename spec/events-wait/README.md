# spec/events-wait：SSE 等待循环"close-before-broadcast"顺序的 TLA+ 模型

形式化验证 `internal/server/server.go` 里 SSE 等待循环（`events` handler +
`runState`）的"丢失唤醒"顺序问题。教学版讲解见 `tmp/server-events-explained.md`
§5.6 / §5.0。

## 为什么放 `spec/` 而不是 `docs/`

- `docs/` 的约定是"one file per topic"的 markdown 笔记；TLA+ 模型是带自己工具链
  （TLC）的形式化规格产物（`.tla` / `.cfg`），单独成目录；
- 规格与代码的对应关系在下面"抽象映射"里，不在代码注释里重复；
- 将来更多并发点的规格（abort、compact、workspace 竞态…）按 `spec/<名称>/` 平铺。

## 为什么是两个模型而不是一个

早期版本用 `order \in {0,1}` 开关变量在同一个模型里切换两种顺序。拆成两个文件后：

- 每个模型自洽、可独立运行，不需要先读懂"开关"；
- 两个模型的**唯一区别**是 writer 收尾两步的顺序（各三行），对照即论证；
- 结果正好一正一反：一个穷举通过、一个给出反例，合起来才是完整证明。

## 抽象映射（模型 → 真实代码）

| 模型变量 | 真实代码 |
|---|---|
| `buf` | `len(st.evs)`（已追加的事件数） |
| `read` | reader 的游标 `idx` |
| `done` | `st.done` 已关闭 |
| `waiting` | reader 正在 `Cond.Wait()` 中 |
| `woken` | 本次等待期间是否收到过广播 |
| `phase` | writer 收尾进度（0/1/2） |

要检查的性质 `NoLostWakeup`：

> writer 收尾完成（phase=2）且 `done` 已关闭后，reader 绝不允许处于"还在等待且
> 从未被唤醒"的状态——那意味着它错过了最后一次广播，会永远等下去（SSE 挂死）。

## 运行

```bash
# 需要 Java。TLC 从这里下载（或任意 release）：
#   https://github.com/tlaplus/tlaplus/releases/download/v1.7.4/tla2tools.jar
cd spec/events-wait

# 正确顺序：先 close(done) 后 Broadcast() —— 应"Model checking completed. No error"
java -jar /path/to/tla2tools.jar -config close_then_broadcast.cfg close_then_broadcast.tla

# 错误顺序：先 Broadcast() 后 close(done) —— 应"Error: Invariant NoLostWakeup is violated"
java -jar /path/to/tla2tools.jar -config broadcast_then_close.cfg broadcast_then_close.tla
```

## 结果

**`close_then_broadcast.tla`（正确顺序）——穷举通过：**

```
Model checking completed. No error has been found.
58 states generated, 36 distinct states found, 0 states left on queue.
```

36 个可达状态全部满足性质（穷举，非抽样）。

**`broadcast_then_close.tla`（错误顺序）——TLC 自动找到反例：**

```
Error: Invariant NoLostWakeup is violated.

State 1: done=FALSE, waiting=FALSE, phase=0, woken=FALSE   (初始)
State 2: <BroadcastFirst> 广播先发生；reader 还没登记 → woken=FALSE
State 3: <StartWait>       reader 此时才登记等待（done 仍是 FALSE，所以能登记）
State 4: <CloseAfter>      writer 关闭 done；没有第二次广播 → phase=2
最终: waiting=TRUE, done=TRUE, woken=FALSE, phase=2  ← 丢失唤醒，违反性质
```

## 结论

- "先 `close(done)` 再 `Broadcast()`" 不是风格问题，是**形式化可证明的必要顺序**；
- 顺序反了，存在一个合法交错让 reader 错过最后广播、永远睡在 `Wait()` 里；
- 这个 bug 是**逻辑错误而不是数据竞争**——`go test -race` 抓不到它，只有人脑
  （不变式）或模型检查（TLC）能保证；
- 该模型验证的是**协议**（谁在什么顺序广播/关闭），与等待循环的实现结构
  （一个 select 还是两个 select）无关——`server.go` 的 events 循环已按
  "单 select" 重构（见 `server.go` 的 why-comment），协议不变，本模型依然有效。
