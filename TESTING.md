# Testing

The automated reliability gate is:

```sh
make fmt
git diff --check
GOCACHE=/tmp/message-sync-go-cache make test
GOCACHE=/tmp/message-sync-go-cache make vet
GOCACHE=/tmp/message-sync-go-cache go test -race ./internal/integration ./internal/delivery ./internal/recovery ./internal/router ./internal/api ./internal/transport/discord ./internal/transport/telegram ./internal/transport/whatsapp
git diff --exit-code VERSION
```

`internal/integration` uses in-memory fake WhatsApp, Discord, and Telegram adapters to verify cross-transport fan-out, destination isolation, retry, and lifecycle ordering. The focused package tests cover bounded queues, restart/replay state, checkpoints, provider reconnect/history behavior, webhook repair, configuration reload, and privacy-safe API/logging.

For a runtime/container smoke test, use a fresh data directory and no provider credentials:

```sh
tmpdir=$(mktemp -d)
DATA_DIR="$tmpdir" IDENTITY_SECRET=0123456789abcdef0123456789abcdef API_ADDR=127.0.0.1:0 ./bin/message-sync run &
pid=$!
trap 'kill "$pid" 2>/dev/null || true; rm -rf "$tmpdir"' EXIT
sleep 1
kill "$pid"
wait "$pid" || test "$?" -eq 0
podman build -f Containerfile -t message-sync:dev .
timeout 4s podman run --rm --read-only --tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m -v "$tmpdir:/data:Z" -e DATA_DIR=/data -e API_ADDR=127.0.0.1:0 -e IDENTITY_SECRET=0123456789abcdef0123456789abcdef message-sync:dev run
```

The data directory is disposable because the current SQLite schema is a fresh-database schema; no migration or compatibility path is supported before the first release. `sync.db` stores operational metadata only, and test canaries assert that message content, media, identities, credentials, and raw provider errors do not cross the persistence, API, or logging boundary.
