# Testing

## Private DM relay regression gate

Run `go test ./internal/privatedm ./internal/controlstore ./internal/connection
./internal/transport/... ./internal/app ./internal/api` in addition to the full
gate below. Private relay tests cover all nine provider pairings, owning
connections, native reply correlation, concurrent conversations, command
precedence, invalid inputs, duplicate creates, expiry, membership changes,
endpoint reassignment, ignored lifecycle events and zero canonical group copies.
Control-store tests check ciphertext, inbox-bound lookup, restart persistence
and bounded cleanup retries. Adapter tests inspect WhatsApp per-message Delete
for Me operations, Discord bot DM payloads/intents, Telegram bootstrap and
structured replies, suppressed previews/pings and absence of shared-message
deletion. Real-provider cleanup behavior requires the documented manual checks;
fake tests do not claim live linked-device deletion has been verified.

The automated reliability gate is:

```sh
make fmt
git diff --check
GOCACHE=/tmp/message-sync-go-cache make test
GOCACHE=/tmp/message-sync-go-cache make vet
GOCACHE=/tmp/message-sync-go-cache make build
GOCACHE=/tmp/message-sync-go-cache go test -race ./internal/privatedm ./internal/controlstore ./internal/integration ./internal/connection ./internal/delivery ./internal/recovery ./internal/router ./internal/api ./internal/transport/discord ./internal/transport/telegram ./internal/transport/whatsapp
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

Use a disposable data directory for these smoke tests. Narrow idempotent initialization migrations preserve documented schema additions; upgrade tests cover the discriminator-display setting and Discord anonymization flag. `sync.db` stores operational metadata only, and test canaries assert that message content, media, identities, credentials, and raw provider errors do not cross the persistence, API, or logging boundary.

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


## Cross-group references regression gate

`go test ./internal/store ./internal/router ./internal/transport/...`
includes participant handle stability/isolation, name and pseudonym resolution,
post-restart pseudonym lookup, alias ambiguity rejection, and exact literal
fallback. Positive adapter tests assert WhatsApp LID token/mention metadata in
text and media captions, reference propagation to audio/sticker text companions,
Discord mention text and explicit user allowlisting, and Telegram HTML inline
mentions for members shared across groups. Rejection tests cover absent or
unknown members, failed lookups, PN-only WhatsApp identities, revoked Discord
channel access/private-thread membership, and departed/restricted Telegram
members. Discord tests cover native mention payloads on anonymized routes,
including pseudonym input, and IDs restored by channel/thread history recovery.
Full-path restart tests observe a Discord `dg1/vm` or WhatsApp sender, verify
the forwarded attribution, recreate the router/adapter, then address the name
from that older message. They check real names, full pseudonyms, short aliases
and `Pn` with anonymization both on and off, final native payloads, and literal
text in nonmember destinations. Provider-lookup tests reject ambiguous names
and actors outside the referenced group's registry.
Cold-cache tests cover HMAC-only guild-roster matching, pagination, missing
roster permission, nonadvancing pages, channel-access rejection and rejection
of identities from another provider.
HTML-escaped names, duplicate tokens and escaped literals are covered.

For client rendering and notifications, perform real-provider checks:
WhatsApp LID-only mention chips without phone exposure; Discord channel and
private-thread membership with explicit allowed-user allowlisting; Telegram
Bot API clickable inline mentions. References must stay exactly as posted in
groups without verified membership. Test edit/delete propagation, reconnection,
restart (Discord/Telegram native-ID caches are transient), and escaped
`\@{group/P1}` literals. MTProto and edits keep literal references. Explicit
Discord references may tag verified local members on anonymized routes while
sender and ordinary provider-mention anonymization remain unchanged.

Discriminator regressions cover hidden defaults, enabled display across sender,
child, hash, poll and reaction attribution, live display reload, and immutable
legacy aliases. Upgrade tests open the previous schema, check the false default
and preservation of existing settings, enable the setting and reopen the DB.
API tests preserve explicit false and omitted partial-update fields. Router
collision fixtures reject duplicate five-character codes and name/handle
conflicts, retaining exact authored tokens. Full-path WhatsApp/Discord restart
tests exercise discriminator-only labels with anonymization and display on/off;
Telegram Bot API tests verify cold router lookup plus native and unavailable
native-cache payloads. Long Discord webhook names retain their full suffix.

Scoped-policy tests cover independent settings/partial updates, code-length
validation and historical-length addressing, transport username isolation,
WhatsApp media isolation, scoped friendly-label preservation, live retention
reload, original retention ownership and orphan suppression cleanup. Upgrade
fixtures remove the new sync-set settings/retention and connection-name columns,
import nondefault policies, and reopen to prove idempotence and independence.
API tests reject retired global settings, protect the DM safety gate, and show failure when
saved settings cannot reload into the runtime. Browser smoke checks exercise
creation/editing modals, dirty-only Save enablement (including reverted fields),
saving/error states, chip removal/paste, independent set values, section
spacing at desktop/mobile sizes, and existing WhatsApp connection names.
Membership list tests assert sync-set filtering before the request limit and
independent persisted review flags. Browser checks cover enabling membership
review per set, conditional fields, scoped navigation and list selection.
