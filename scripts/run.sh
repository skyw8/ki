#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SESSION="${KI_TMUX_SESSION:-ki}"
ADDR="0.0.0.0:19800"
ATTACH=0
FAKE=0
FORCE_WEB=0
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
  echo "      --force-web rebuild web/dist even when frontend inputs are unchanged"
  echo "      --addr A    listen address (default $ADDR)"
  echo "      -h, --help  show this help"
  exit 0
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -a|--attach) ATTACH=1 ;;
    --fake) FAKE=1 ;;
    --force-web) FORCE_WEB=1 ;;
    --addr) ADDR="$2"; shift ;;
    -h|--help) usage ;;
    *) SERVE_ARGS+=("$1") ;;
  esac
  shift
done

command -v tmux >/dev/null || { echo "error: tmux is required" >&2; exit 1; }

cd "$ROOT"

WEB_HASH_VERSION=1

hash_stream() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum | cut -d' ' -f1
  else
    shasum -a 256 | cut -d' ' -f1
  fi
}

hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

web_inputs_hash() {
  (
    cd "$ROOT/web"
    printf 'version %s\n' "$WEB_HASH_VERSION"
    printf 'bun %s\n' "$(bun --version 2>/dev/null || echo unknown)"
    for f in index.html package.json bun.lock vite.config.ts tsconfig.json; do
      if [[ -f $f ]]; then printf '%s %s\n' "$(hash_file "$f")" "$f"; fi
    done
    for d in src public; do
      if [[ -d $d ]]; then
        find "$d" -type f | LC_ALL=C sort | while IFS= read -r f; do
          printf '%s %s\n' "$(hash_file "$f")" "$f"
        done
      fi
    done
  ) | hash_stream
}

echo "building web/dist ..."
web_hash="$(web_inputs_hash)"
web_hash_file="$ROOT/web/node_modules/.cache/ki/web-dist-hash"
dist_index_hash=""
[[ -f "$ROOT/web/dist/index.html" ]] && dist_index_hash="$(hash_file "$ROOT/web/dist/index.html")"
recorded_index_hash=""
if [[ -f "$web_hash_file.index" ]]; then
  recorded_index_hash="$(cat "$web_hash_file.index")"
fi
if [[ $FORCE_WEB != 1 && -f "$ROOT/web/dist/index.html" && -f "$web_hash_file" \
  && "$(cat "$web_hash_file")" == "$web_hash" \
  && "$recorded_index_hash" == "$dist_index_hash" ]]; then
  echo "web/dist unchanged, skipping frontend build (--force-web to rebuild)"
else
  (cd web && bun run build)
  mkdir -p "$(dirname "$web_hash_file")"
  printf '%s\n' "$web_hash" > "$web_hash_file"
  printf '%s\n' "$(hash_file "$ROOT/web/dist/index.html")" > "$web_hash_file.index"
fi
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

tmux set-option -w -t "$SESSION:server" remain-on-exit failed

port="${ADDR##*:}"
tmux send-keys -t "$SESSION:server" C-c 2>/dev/null || true
for _ in {1..50}; do
  if ! ss -H -ltn "sport = :$port" 2>/dev/null | grep -q .; then
    break
  fi
  sleep 0.1
done

tmux respawn-window -k -t "$SESSION:server" -c "$ROOT" "$cmd"
tmux select-window -t "$SESSION:server"

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
