---- MODULE broadcast_then_close ----
(*
  TLA+ model of the server.go events wait loop -- incorrect order (control case).
  Implements the abstraction described in spec/events-wait/README.md.

  This model fixes the writer finalization order (the only difference from
  close_then_broadcast.tla):
    1. Broadcast()          (broadcast first)
    2. close(done)          (close afterward)
  Property: a reader must never wait forever after missing the final broadcast.
  Expected result: TLC finds a counterexample (a lost wakeup), showing that
  "close(done) before Broadcast()" is the necessary order.
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

(* writer: append an event from the emit callback *)
Append ==
  /\ done = FALSE
  /\ buf < MaxEvents
  /\ buf' = buf + 1
  /\ UNCHANGED <<read, done, waiting, woken, phase>>

(* writer finalization: Broadcast first, then close(done) -- incorrect order *)
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

(* reader: register to wait (enter Wait). Real code waits only while done is open. *)
StartWait ==
  /\ done = FALSE
  /\ waiting = FALSE
  /\ read = buf
  /\ waiting' = TRUE
  /\ woken' = FALSE
  /\ UNCHANGED <<buf, read, done, phase>>

(* reader: recheck after broadcast; leave if done is closed, otherwise register and wait again *)
Recheck ==
  /\ waiting = TRUE
  /\ woken = TRUE
  /\ IF done THEN waiting' = FALSE
            ELSE waiting' = TRUE
  /\ woken' = FALSE
  /\ UNCHANGED <<buf, read, done, phase>>

(* reader: consume one event (drain the buffer) *)
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

(* As in close_then_broadcast.tla: the reader must not wait forever after finalization. *)
NoLostWakeup ==
  [] ~(waiting /\ done /\ woken = FALSE /\ phase = 2)

====
