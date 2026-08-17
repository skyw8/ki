---- MODULE broadcast_then_close ----
(*
  server.go events 等待循环的 TLA+ 模型 —— 错误顺序（对照组）。
  对应 spec/events-wait/README.md 的抽象映射。

  本模型固定 writer 收尾顺序为（与 close_then_broadcast.tla 的唯一区别）：
    1. Broadcast()          （先广播）
    2. close(done)          （后关灯）
  要检查的性质：reader 绝不因错过最后一次广播而永远等待。
  预期结果：TLC 找到反例（丢失唤醒），证明"先 close(done) 后 Broadcast()"
  是必要顺序。
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

(* writer 收尾：先 Broadcast，后 close(done) —— 错误顺序 *)
BroadcastFirst ==
  /\ phase = 0
  /\ done = FALSE
  /\ woken' = (woken \/ waiting)
  /\ phase' = 1
  /\ UNCHANGED <<buf, read, done, waiting>>

CloseAfter ==
  /\ phase = 1
  /\ done = FALSE
  /\ done' = TRUE
  /\ phase' = 2
  /\ UNCHANGED <<buf, read, waiting, woken>>

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
  \/ BroadcastFirst
  \/ CloseAfter
  \/ StartWait
  \/ Recheck
  \/ ReadEvent

Spec == Init /\ [][Next]_vars

(* 同 close_then_broadcast.tla：收尾完成后 reader 不得永远等待 *)
NoLostWakeup ==
  [] ~(waiting /\ done /\ woken = FALSE /\ phase = 2)

====
