#!/bin/sh
# Stub `zg` CLI for the sidecar runtime tests. It echoes deterministic output
# for the subcommands the extension uses and records each invocation.
set -eu

log="${KI_ZVEC_GREP_TEST_LOG:-}"
if [ -n "$log" ]; then
  printf '{"event":"cli","args":"%s"}\n' "$*" >>"$log"
fi

command="${1:-}"
case "$command" in
  status)
    echo "Workspace index is ready"
    echo "  roots  $2"
    ;;
  server)
    # `server status --check-ready`
    echo "daemon ready"
    exit 0
    ;;
  query)
    echo "freshness: fresh"
    echo "1  ${2:-query}"
    ;;
  index)
    if [ "${KI_ZVEC_GREP_FAKE_INDEX_FAIL:-}" = "1" ]; then
      # Mirrors zg's refusal to change the model of an existing index.
      echo 'Error: Embedding model does not match the existing index.' >&2
      echo 'Re-run with --rebuild to change the embedding model.' >&2
      exit 1
    fi
    # A small delay keeps a background job visible to the caller long enough to
    # exercise the "already running" and progress paths.
    sleep "${KI_ZVEC_GREP_FAKE_INDEX_SLEEP:-0.3}"
    echo "Scanning files..."
    echo "Indexing files: 1/2"
    echo "Indexing complete"
    printf 'files\t2 scanned, 2 added, 0 modified, 0 retried, 0 unchanged, 0 deleted, 0 failed\n'
    printf 'entities\t2\n'
    printf 'duration\t1s (1000ms)\n'
    ;;
esac
