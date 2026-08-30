#!/usr/bin/env bash
# scripts/run.sh — build ki and run the real configured provider inside a dedicated tmux session.
#
# The session (default name: ki) has two windows:
#   server — runs `./ki serve`
#   cli    — a shell for operating ki (attach and type `./ki ...` there)
#
# Re-running rebuilds and respawns only the server window, so the cli
# window keeps its history. Attach with `tmux attach -t ki`.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SESSION="${KI_TMUX_SESSION:-ki}"
ADDR="0.0.0.0:19800"
BUILD_WEB=0
ATTACH=0
FAKE=0
SERVE_ARGS=()

usage() {
  echo "usage: scripts/run.sh [options] [extra serve args...]"
  echo
  echo "options:"
  echo "  -a, --attach    attach to the tmux session after starting"
  echo "      --web       rebuild web/dist before building"
  echo "      --fake      opt in to KI_FAKE=1 for canned-model tests"
  echo "      --addr A    listen address (default $ADDR)"
  echo "      -h, --help  show this help"
  exit 0
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -a|--attach) ATTACH=1 ;;
    --web) BUILD_WEB=1 ;;
    --fake) FAKE=1 ;;
    --addr) ADDR="$2"; shift ;;
    -h|--help) usage ;;
    *) SERVE_ARGS+=("$1") ;;
  esac
  shift
done

command -v tmux >/dev/null || { echo "error: tmux is required" >&2; exit 1; }

cd "$ROOT"

if [[ $BUILD_WEB == 1 ]]; then
  echo "building web/dist ..."
  (cd web && npm run build)
fi

echo "building ./ki ..."
go build -o ki ./cmd/ki

if ! tmux has-session -t "$SESSION" 2>/dev/null; then
  tmux new-session -d -s "$SESSION" -n server -c "$ROOT"
  tmux new-window -t "$SESSION" -n cli -c "$ROOT"
else
  for w in server cli; do
    if ! tmux list-windows -t "$SESSION" -F '#{window_name}' | grep -qx "$w"; then
      tmux new-window -t "$SESSION" -n "$w" -c "$ROOT"
    fi
  done
fi

cmd=""
[[ $FAKE == 1 ]] && cmd="KI_FAKE=1 "
cmd+="./ki serve --addr $ADDR"
[[ ${#SERVE_ARGS[@]} -gt 0 ]] && cmd+=" ${SERVE_ARGS[*]}"

# Respawn atomically: sending Ctrl-C first lets tmux destroy a window whose
# sole command is the server, leaving no target for the subsequent respawn.
tmux respawn-window -k -t "$SESSION:server" -c "$ROOT" "$cmd"
tmux select-window -t "$SESSION:server"

echo "ki serve starting in tmux session '$SESSION' (window server)"
if [[ "$ADDR" == 0.0.0.0:* ]]; then
  echo "  listen: http://$ADDR/ (open with the host's LAN IP)"
else
  echo "  URL: http://$ADDR/"
fi
echo "  attach: tmux attach -t '$SESSION'"

if [[ $ATTACH == 1 ]]; then
  tmux attach -t "$SESSION"
fi
