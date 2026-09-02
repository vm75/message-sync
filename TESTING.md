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

`internal/integration` uses in-memory fake WhatsApp, Discord, and Telegram adapters to verify cross-transport fan-out, destination isolation, ambiguity-safe retry, restart/replay state, and lifecycle ordering. The focused package tests cover bounded queues, checkpoints, provider reconnect/history behavior, webhook repair, configuration reload, WhatsApp lifecycle markers, and privacy-safe API/logging.

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

The control-plane retention test verifies bounded deletion of expired auth
artifacts and terminal membership requests while returning only opaque evidence
references for private-file cleanup. Container smoke tests use a data volume
writable by UID 1000 because the image deliberately runs as the non-root
`message-sync` user.

Create delivery tests also cover explicit pre-acceptance retries, ambiguous no-blind-retry behavior, restart-safe create-step completion, and audio/sticker compatibility companions. These tests intentionally do not claim exactly-once remote creation for providers without deterministic client-assigned operation IDs.

Recovery tests assert that queued, retrying, and awaiting-replay delivery keeps the source checkpoint replayable, that pending positions block later positions, and that replay reuses the existing canonical/message-copy mapping.

WhatsApp transport tests cover one-shot lifecycle-marker consumption, stale-marker expiry, and the bounded in-memory marker set. Matching bridge echoes are suppressed while unmatched linked-device `FromSelf` mutations continue through normal routing.

The integration gate combines the three transports with deterministic fake outcomes for all-to-all duplicate handling, ambiguous create, retry/replay, partial fan-out, and lifecycle ordering. Provider-specific exact-once creation remains deliberately unasserted where the provider cannot supply it.
