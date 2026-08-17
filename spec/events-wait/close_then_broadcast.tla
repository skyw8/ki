---- MODULE close_then_broadcast ----
(*
  server.go events 等待循环的 TLA+ 模型 —— 正确顺序。
  对应 spec/events-wait/README.md 的抽象映射。

  本模型固定 writer 收尾顺序为：
    1. close(done)          （server.go: defer 里的第一行，无锁）
    2. Broadcast()          （随后在 st.mu 内广播）
  要检查的性质：reader 绝不因错过最后一次广播而永远等待。
  （错误顺序的对照模型见 broadcast_then_close.tla。）

  抽象映射：
    buf     = len(st.evs)（已追加的事件数）
    read    = reader 的游标 idx
    done    = st.done 已关闭
    waiting = reader 正在 Cond.Wait() 中
    woken   = 本次等待期间是否收到过 Broadcast
    phase   = writer 收尾进度：0 未开始 / 1 中间步 / 2 全部完成
*)
EXTENDS Naturals

CONSTANT MaxEvents

VARIABLES buf, read, done, waiting, woken, phase

vars == <<buf, read, done, waiting, woken, phase>>

Init ==
  /\ buf = 0
  /\ read = 0
  /\ done = FALSE
  /\ waiting = FALSE
  /\ woken = FALSE
  /\ phase = 0

(* writer: emit 回调里 append 一个事件 *)
Append ==
  /\ done = FALSE
  /\ buf < MaxEvents
  /\ buf' = buf + 1
  /\ UNCHANGED <<read, done, waiting, woken, phase>>

(* writer 收尾：先 close(done)，后 Broadcast *)
CloseFirst ==
  /\ phase = 0
  /\ done = FALSE
  /\ done' = TRUE
  /\ phase' = 1
  /\ UNCHANGED <<buf, read, waiting, woken>>

BroadcastAfter ==
  /\ phase = 1
  /\ woken' = (woken \/ waiting)
  /\ phase' = 2
  /\ UNCHANGED <<buf, read, done, waiting>>

(* reader: 登记等待（进 Wait）。真实代码里必须在 done 未关闭时才会去等 *)
StartWait ==
  /\ done = FALSE
  /\ waiting = FALSE
  /\ read = buf
  /\ waiting' = TRUE
  /\ woken' = FALSE
  /\ UNCHANGED <<buf, read, done, phase>>

(* reader: 被广播叫醒后重查条件：done 关了就走，没关就重新登记再等 *)
Recheck ==
  /\ waiting = TRUE
  /\ woken = TRUE
  /\ IF done THEN waiting' = FALSE
            ELSE waiting' = TRUE
  /\ woken' = FALSE
  /\ UNCHANGED <<buf, read, done, phase>>

(* reader: 消费一个事件（排空） *)
ReadEvent ==
  /\ read < buf
  /\ read' = read + 1
  /\ UNCHANGED <<buf, done, waiting, woken, phase>>

Next ==
  \/ Append
  \/ CloseFirst
  \/ BroadcastAfter
  \/ StartWait
  \/ Recheck
  \/ ReadEvent

Spec == Init /\ [][Next]_vars

(*
  要检查的性质（safety 表述的 liveness）：
  writer 收尾完成（phase=2）且 done 已关闭后，reader 绝不允许处于
  "还在等待且从未被唤醒"的状态 —— 那意味着它错过了最后一次广播。
*)
NoLostWakeup ==
  [] ~(waiting /\ done /\ woken = FALSE /\ phase = 2)

====
