# Testing

## Private DM relay regression gate

Run `go test ./internal/privatedm ./internal/controlstore ./internal/connection
./internal/transport/... ./internal/app ./internal/api` in addition to the full
gate below. Private relay tests cover all nine provider pairings, owning
connections, three-turn native reply chains, named and anonymized DM headers,
concurrent conversations, command
precedence, invalid inputs, duplicate creates, expiry, membership changes,
endpoint reassignment, ignored lifecycle events and zero canonical group copies.
Control-store tests check ciphertext (including author message tokens),
inbox-bound lookup, restart persistence
and bounded cleanup retries. Adapter tests inspect WhatsApp per-message Delete
for Me operations, Discord bot DM payloads/intents, Telegram bootstrap and
structured replies, bold-italic attribution (including UTF-16 entity offsets), suppressed previews/pings and absence of shared-message
deletion. Real-provider cleanup behavior requires the documented manual checks;
fake tests do not claim live linked-device deletion has been verified.
The WhatsApp adapter-to-relay regression uses phone-only private events and an
empty protocol identity cache, exercising command delivery and a three-turn native
reply chain. Roster lookup checks disabled endpoints, missing pairs, unrelated
phone numbers and conflicting identities without private alias enrollment.

Alias upgrade regression checks cover private delivery to an original issued
address, ordering detection, subsequent allocation and restart persistence.

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

`internal/integration` uses in-memory fake WhatsApp, Discord, and Telegram adapters to verify multi-connection mixed-transport fan-out, destination isolation, ambiguity-safe retry, cross-connection thread/topic reply lineage, connection reassignment, restart/replay state, and lifecycle ordering. The focused package tests cover dynamic connection management, bounded queues, checkpoints, provider reconnect/history behavior, webhook repair, configuration reload, directional endpoint anonymization and alias canaries, WhatsApp lifecycle markers, and privacy-safe API/logging.

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

Use a disposable data directory for these smoke tests. Narrow idempotent initialization migrations preserve documented schema additions; upgrade tests cover removal of retired display settings, preservation of unrelated policies, and conversion of the legacy Discord anonymity flag into endpoint incoming settings. `sync.db` stores operational metadata only, and test canaries assert that message content, media, identities, credentials, and raw provider errors do not cross the persistence, API, or logging boundary.

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

`go test ./internal/identity ./internal/store ./internal/config ./internal/router ./internal/transport/... ./internal/privatedm`
covers global alias uniqueness, the exhaustive 65,536-pair bijection, frozen
vocabulary/ordering checksums, overflow and int64 boundaries, restart stability,
SQL contention across connections, rollback and permanent nonreuse after deletion. Settings-only
saves preserve assignments, counters, observations and friendly labels; changed
bindings invalidate stale endpoint state. Upgrade tests strip retired display
settings while preserving all unrelated policies.

Router tests check source-set isolation, case-insensitive assigned-alias lookup,
exact literal fallback, escaping and reference count bounds. Only accepted group
authors allocate; structured mentions and private commands reuse assignments.
Unknown mentions stay safe without inventing aliases. Quote-only mention entities
retain metadata through ingress; native poll question/option entities render
restart-safe alias labels before result presentation is retained. Allocation and identity
failures create no canonical/outbound state. WhatsApp PN-only authors consume no
ordinal until a canonical LID is verified; author/mention identities agree.

Adapter tests assert actual-destination WhatsApp LID wire tokens/metadata in text,
captions and companions; Discord guild/channel/private-thread checks and explicit
allowed users; and Telegram membership-gated, HTML-escaped inline mentions.
Restart tests reuse persisted aliases while recreating volatile native caches.
Cold-cache Discord tests cover HMAC guild-roster pagination, permissions,
nonadvancing pages and rejection of identities from other providers. Edits and
MTProto retain non-notifying literal braced references. Long Discord webhook
names retain the entire assigned alias.

Poll mention tests verify alias-projected question/options and equivalent source
and forwarded vote counts. Store tests verify the source option-hash mapping
survives restart, rejects unrecognized hashes and cascades on poll deletion.

Private tests cover nine provider pairings, preexisting assignments, global
lookup with set-local authorization, equivalent inbox deduplication, distinct
inbox rejection, exact native reply identity/TTL, restart, membership revocation,
no enrollment and unchanged provider cleanup restrictions. No native private
content or inbox identity may enter canonical routing state or admin output.

For real client rendering, follow [the manual guide](docs/TESTING_GUIDE.md):
check alias-only anonymity wherever source Outgoing or destination Incoming is enabled, and name-plus-alias attribution elsewhere,
LID-only WhatsApp chips, Telegram links, revoked permissions, cold caches and
escaped `\@{Amber Badger}` literals. Legacy username-mode/discriminator API fields
must be rejected; their inert schema columns are only upgrade compatibility data.

Scoped-policy tests cover independent settings/partial updates, policy
validation and retired-setting rejection, transport policy isolation,
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
