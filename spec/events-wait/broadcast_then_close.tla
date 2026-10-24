---- MODULE broadcast_then_close ----
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

Append ==
  /\ done = FALSE
  /\ buf < MaxEvents
  /\ buf' = buf + 1
  /\ UNCHANGED <<read, done, waiting, woken, phase>>

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

StartWait ==
  /\ done = FALSE
  /\ waiting = FALSE
  /\ read = buf
  /\ waiting' = TRUE
  /\ woken' = FALSE
  /\ UNCHANGED <<buf, read, done, phase>>

Recheck ==
  /\ waiting = TRUE
  /\ woken = TRUE
  /\ IF done THEN waiting' = FALSE
            ELSE waiting' = TRUE
  /\ woken' = FALSE
  /\ UNCHANGED <<buf, read, done, phase>>

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

NoLostWakeup ==
  [] ~(waiting /\ done /\ woken = FALSE /\ phase = 2)

====
