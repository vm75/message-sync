# Testing

The automated reliability gate is:

```sh
make fmt
git diff --check
GOCACHE=/tmp/message-sync-go-cache make test
GOCACHE=/tmp/message-sync-go-cache make vet
GOCACHE=/tmp/message-sync-go-cache go test -race ./internal/integration ./internal/connection ./internal/delivery ./internal/recovery ./internal/router ./internal/api ./internal/transport/discord ./internal/transport/telegram ./internal/transport/whatsapp
git diff --exit-code VERSION
```

For ordinary feature/fix work, `VERSION` should remain unchanged; the `git diff --exit-code VERSION` check is therefore expected to be clean. A dedicated release PR is the explicit exception and should change `VERSION` together with `CHANGELOG.md`.

For release-workflow changes, also verify the release-mode binary identity locally:

```sh
version="$(tr -d '[:space:]' < VERSION)"
go build -trimpath -ldflags "-X github.com/vm75/message-sync/internal/version.Build=$version" -o /tmp/message-sync-release ./cmd/message-sync
test "$(/tmp/message-sync-release version)" = "$version"
```

GitHub Actions also runs `.github/workflows/ci.yml` for every pull request targeting `main`, and for pushes to `main` that update `VERSION` or the CI workflow itself. That workflow checks formatting and whitespace, runs the full test/vet gate plus focused race tests, builds the command, and verifies that the current `VERSION` can be embedded and reported by the release binary.

The actual publication workflow is tag-only and enforces tag == `VERSION` before registry authentication or publishing. See [RELEASING.md](RELEASING.md) for stable, pre-release, mismatch, and immutability scenarios.

`internal/integration` uses in-memory fake WhatsApp, Discord, and Telegram adapters to verify multi-connection mixed-transport fan-out, destination isolation, ambiguity-safe retry, cross-connection thread/topic reply lineage, connection reassignment, restart/replay state, and lifecycle ordering. The focused package tests cover dynamic connection management, bounded queues, checkpoints, provider reconnect/history behavior, webhook repair, configuration reload, route-aware Discord anonymization and pseudonym canaries, WhatsApp lifecycle markers, and privacy-safe API/logging.

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

`TestWhatsAppPlainTextEditPipeline` feeds protocol, wrapped, and secret-encrypted edits through the real live and HistorySync adapter paths, then the recovery coordinator and router. It checks in-place updates to Discord, WhatsApp, and Telegram copies for participant and linked-account events at the original message timestamp, with media synchronization disabled. Encrypted-edit failure tests verify safe diagnostics without provider data or content, and coordinator tests preserve ordered edit checkpoint deduplication and advancement for other providers.

The integration gate combines the three transports with deterministic fake outcomes for all-to-all duplicate handling, ambiguous create, retry/replay, partial fan-out, and lifecycle ordering. Provider-specific exact-once creation remains deliberately unasserted where the provider cannot supply it.
