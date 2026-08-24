---- MODULE close_then_broadcast ----
(*
  TLA+ model of the server.go events wait loop -- correct order.
  Implements the abstraction described in spec/events-wait/README.md.

  This model fixes the writer finalization order:
    1. close(done)          (server.go: first line in defer, without a lock)
    2. Broadcast()          (broadcast afterward while holding st.mu)
  Property: a reader must never wait forever after missing the final broadcast.
  (See broadcast_then_close.tla for the control model with the incorrect order.)

  Abstraction mapping:
    buf     = len(st.evs) (number of appended events)
    read    = the reader cursor idx
    done    = st.done is closed
    waiting = the reader is in Cond.Wait()
    woken   = whether Broadcast was received during this wait
    phase   = writer finalization progress: 0 not started / 1 intermediate / 2 complete
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

(* writer finalization: close(done) first, then Broadcast *)
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
  \/ CloseFirst
  \/ BroadcastAfter
  \/ StartWait
  \/ Recheck
  \/ ReadEvent

Spec == Init /\ [][Next]_vars

(*
  Property (liveness expressed as a safety invariant):
  after writer finalization is complete (phase=2) and done is closed, the reader
  must never be in the state "waiting and never woken" -- that means it missed
  the final broadcast.
*)
NoLostWakeup ==
  [] ~(waiting /\ done /\ woken = FALSE /\ phase = 2)

====
