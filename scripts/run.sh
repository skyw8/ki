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
ATTACH=0
FAKE=0
SERVE_ARGS=()
PROXY_ENV_KEYS=(
  HTTP_PROXY HTTPS_PROXY ALL_PROXY NO_PROXY FTP_PROXY
  http_proxy https_proxy all_proxy no_proxy ftp_proxy
)

usage() {
  echo "usage: scripts/run.sh [options] [extra serve args...]"
  echo
  echo "options:"
  echo "  -a, --attach    attach to the tmux session after starting"
  echo "      --fake      opt in to KI_FAKE=1 for canned-model tests"
  echo "      --addr A    listen address (default $ADDR)"
  echo "      -h, --help  show this help"
  exit 0
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -a|--attach) ATTACH=1 ;;
    --fake) FAKE=1 ;;
    --addr) ADDR="$2"; shift ;;
    -h|--help) usage ;;
    *) SERVE_ARGS+=("$1") ;;
  esac
  shift
done

command -v tmux >/dev/null || { echo "error: tmux is required" >&2; exit 1; }

cd "$ROOT"

# web/dist is untracked build output, so always rebuild it before compiling the
# embedded binary. Otherwise frontend source changes can silently remain stale.
echo "building web/dist ..."
(cd web && bun run build)
if [[ ! -f web/dist/index.html ]]; then
  echo "error: web/dist/index.html is missing (run 'cd web && bun install' first)" >&2
  exit 1
fi

echo "building ./ki ..."
go build -tags embed -o ki ./cmd/ki

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

# tmux servers keep their own environment snapshot. Refresh proxy variables
# before respawning Ki so a long-lived tmux server does not run with stale data.
for key in "${PROXY_ENV_KEYS[@]}"; do
  if [[ -v $key ]]; then
    tmux set-environment -t "$SESSION" "$key" "${!key}"
  else
    tmux set-environment -t "$SESSION" -u "$key" 2>/dev/null || true
  fi
done

cmd=""
[[ $FAKE == 1 ]] && cmd="KI_FAKE=1 "
cmd+="./ki serve --addr $ADDR"
[[ ${#SERVE_ARGS[@]} -gt 0 ]] && cmd+=" ${SERVE_ARGS[*]}"

# Keep a failed server pane around so its stderr remains available for
# diagnosis instead of disappearing when the command exits.
tmux set-option -w -t "$SESSION:server" remain-on-exit failed

# Stop the previous server and wait for its listener to disappear before
# starting the replacement. Without this wait, a fast restart can race the
# old process's shutdown and make the new server fail with "address in use".
port="${ADDR##*:}"
tmux send-keys -t "$SESSION:server" C-c 2>/dev/null || true
for _ in {1..50}; do
  if ! ss -H -ltn "sport = :$port" 2>/dev/null | grep -q .; then
    break
  fi
  sleep 0.1
done

# Rebuild first, then replace the stopped server with the new binary.
tmux respawn-window -k -t "$SESSION:server" -c "$ROOT" "$cmd"
tmux select-window -t "$SESSION:server"

# tmux returns as soon as it has launched the command. Wait for the HTTP
# listener so a bind/startup failure is reported by this script rather than
# being hidden in the detached server window.
ready=0
for _ in {1..30}; do
  if curl --silent --show-error --fail --max-time 1 "http://127.0.0.1:$port/" >/dev/null 2>&1; then
    ready=1
    break
  fi
  if ! tmux list-windows -t "$SESSION" -F '#{window_name}' | grep -qx 'server'; then
    break
  fi
  sleep 0.2
done
if [[ $ready != 1 ]]; then
  echo "error: ki serve failed to become ready on $ADDR" >&2
  tmux capture-pane -pt "$SESSION:server" -S -40 >&2 2>/dev/null || true
  exit 1
fi

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
