# Telegram MTProto Support Progress Tracker

Branch: `agent/telegram-mtproto-support`
Base: `main` at `284d6ce2dd16cc8a5254cf40d1810c97c4fb51c9`

> Temporary implementation tracker. Keep this file current while the issues below are in progress. Delete it in the final cleanup commit after all tracked issues are implemented, tested, documented, and closed.

## Objective

Add phone-number/user-account Telegram integration through MTProto as an **alternative** to the existing Telegram Bot API integration.

The two methods are mutually exclusive per connection/endpoint:

- `telegram / bot` — current Bot API integration
- `telegram / mtproto` — new phone-authenticated user-session integration

An endpoint continues to reference exactly one `connection_id`. That connection determines the Telegram integration method. There is no dual-send, automatic fallback, or mixed authentication for one endpoint.

Keep the canonical transport name `telegram`; the router, sync sets, canonical message model, replies, reactions, media, edits/deletes, polls, and topic model should remain integration-method agnostic.

## Why add MTProto

The Bot API remains the simplest operational option, but it cannot provide several capabilities that are valuable for message-sync:

- complete joined-group discovery without first observing traffic
- complete forum-topic enumeration
- arbitrary historical message reads
- history-backed recovery/backfill
- freedom from Bot Privacy Mode visibility limitations

MTProto should provide those missing capabilities while retaining live feature parity with the Bot API wherever practical.

## Architecture principles

1. **Exclusive method per endpoint** — endpoint -> connection -> one integration mode.
2. **No router fork** — both Telegram integrations normalize into the existing `transport.Incoming` / `transport.Outgoing` contract.
3. **Capability-driven admin behavior** — the backend reports what a connection supports; the UI enables only supported operations.
4. **Bot API remains first-class** — no regression, removal, or forced migration.
5. **MTProto session material is highly sensitive** — persist only through the existing encrypted control-plane boundary; never plaintext files, logs, or `sync.db`.
6. **KISS/YAGNI** — do not create generalized provider/plugin abstractions beyond what is needed for the two Telegram modes.
7. **No automatic fallback** — failures in one integration mode must not cause routing through the other.
8. **No version bump** during this feature branch unless explicitly requested separately.

## Preferred MTProto implementation

Use `github.com/gotd/td` unless a concrete implementation blocker is found and documented before selecting another library. Required capabilities include:

- user phone-code authentication
- optional Telegram 2FA flow
- pluggable persistent session storage
- raw/update handling
- media upload/download
- dialog and forum-topic APIs
- history reads
- reconnect/flood-wait support

Avoid plaintext session-file storage. Adapt gotd session storage to encrypted `control.db` state.

## Capability matrix target

| Capability | Bot API | MTProto |
|---|---|---|
| Live text send/receive | Yes | Yes |
| Media | Yes | Yes |
| Native replies | Yes | Yes |
| Reactions | Yes | Yes |
| Edits/deletes | Yes | Yes |
| Telegram topics | Yes, observed/live | Yes |
| Native polls/live aggregate results | Yes | Yes after #107 |
| Group discovery | Observed chats | Full joined dialogs |
| Forum-topic discovery | Observed topics | Full enumeration |
| Bot Privacy Mode guidance | Applicable | Not applicable |
| Arbitrary history reads | No | Yes |
| Recovery from provider history | No | Yes |
| Simple token onboarding | Yes | No |
| Phone/OTP/2FA onboarding | No | Yes |

## Issue order

### Phase 1 — Connection model and lifecycle

- [x] #102 — Telegram dual integration foundation: exclusive bot vs MTProto connection modes
  - connection-level `integration_mode`
  - schema/API validation
  - adapter selection
  - capability model
  - existing Telegram rows remain Bot API

- [x] #103 — Implement secure Telegram MTProto authentication and session lifecycle
  - API ID/hash + phone
  - code flow
  - optional 2FA
  - encrypted reusable session
  - restart/reconnect/logout
  - multi-connection isolation

### Phase 2 — Live transport parity

- [x] #104 — Implement Telegram MTProto live transport parity for messages, media, replies, reactions, edits, deletes, and topics
  - new MTProto adapter behind existing transport contract
  - peer/access-hash persistence or reliable reconstruction
  - loop prevention
  - topic-aware send/reply
  - live feature parity

### Phase 3 — MTProto-only capability gains

- [x] #105 — Add MTProto full chat and forum-topic discovery with capability-aware admin APIs
  - full joined-group discovery
  - direct target validation
  - complete forum-topic enumeration
  - keep Bot API observed-discovery behavior

- [x] #106 — Implement MTProto historical recovery and bounded backfill through RecoverySource
  - provider-history recovery
  - bounded max-age/max-count behavior
  - deterministic ordering
  - dedupe against live events
  - topic-aware recovered messages

### Phase 4 — Poll parity

- [ ] #107 — Add Telegram MTProto poll parity with canonical live poll synchronization
  - inbound native polls
  - outbound native polls
  - aggregate poll snapshots
  - persistent provider correlation
  - no per-voter data

### Phase 5 — Product integration and hardening

- [ ] #108 — Complete Telegram integration-mode UI, end-to-end hardening, documentation, and tracker cleanup
  - mutually exclusive mode choice in UI
  - Bot API flow unchanged
  - phone/code/2FA MTProto UI
  - capability-driven controls
  - mixed bot+MTProto deployment tests
  - restart/reload/delete/security regression tests
  - final docs
  - delete this tracker

## Dependency graph

```text
#102 connection-mode foundation
  |
  +--> #103 MTProto auth/session
           |
           +--> #104 live MTProto adapter
                    |
                    +--> #105 discovery/topics
                    +--> #106 recovery/backfill
                    +--> #107 polls
                              \
#105 ---------------------------+
#106 ---------------------------+--> #108 UI + hardening + docs + tracker removal
#107 ---------------------------+
```

## Expected connection model

Conceptually:

```text
transport_connections
  id
  transport = telegram
  integration_mode = bot | mtproto
  encrypted_credential
  credential_nonce
  ...

endpoints
  alias
  transport = telegram
  connection_id -> exactly one Telegram connection
  remote_id
```

Do not add `telegram_bot` or `telegram_mtproto` as new canonical transports. Do not add integration mode to sync-set routing decisions.

For MTProto, the encrypted credential payload may contain structured state such as API credentials and reusable session bytes. Ephemeral OTP and 2FA inputs must not be persisted.

## Capability contract guidance

Do not hard-code feature differences throughout the frontend. Expose a small backend capability descriptor, sufficient for the UI/admin API to determine at least:

```text
chatDiscovery = observed | full
topicDiscovery = observed | full
historyRecovery = false | true
privacyModeStatus = false | true
polls = false | true
```

The exact representation can differ if a simpler typed design fits the codebase better.

Capabilities are presentation/admin metadata only. They must not influence canonical routing identity.

## Data/privacy rules

MTProto introduces more sensitive session state than the Bot API. Preserve the current project privacy model.

Must never be persisted in `sync.db` or logs:

- phone number
- OTP/login code
- Telegram 2FA password
- API hash
- MTProto auth/session key material
- raw Telegram update/history payloads
- message bodies beyond the existing approved encrypted poll-presentation storage behavior

The reusable MTProto session and required auth configuration must be encrypted in the control-plane store.

Peer/access-hash state required for post-restart sends must also be handled deliberately; do not rely only on an in-memory cache that requires fresh Telegram traffic after restart.

## Testing expectations for every issue

Before closing an issue:

1. Run focused unit tests for the changed area.
2. Run the full repository test suite.
3. Run formatting/static checks used by the repository.
4. Verify no unrelated version changes.
5. Add regression tests for existing Bot API behavior when shared Telegram code changes.
6. Update this tracker with the issue status and any design decision that affects later issues.
7. Update durable product/architecture docs when the completed behavior is user-visible or architectural.
8. Close the GitHub issue only after tests and docs are complete.

## End-to-end scenarios required before completion

The final implementation must cover at least:

- Bot API connection only
- MTProto connection only
- Bot API + MTProto connections in one process
- two independent MTProto connections
- Telegram endpoints in separate sync sets using different modes
- restart with an authorized MTProto session
- connection disable/re-enable
- connection deletion/logout
- live text/media in both directions
- replies across platforms
- reactions
- edits/deletes
- Telegram forum topics / Discord threads
- polls and live aggregate results
- MTProto history recovery
- recovery/live overlap without duplicate fan-out
- no cross-connection peer/session/cursor/poll leakage
- no credential/phone/session/message-content leakage into inappropriate stores or logs

## Explicit non-goals

- Discord integration changes
- private Telegram DM synchronization
- one endpoint using Bot API and MTProto simultaneously
- fallback from one Telegram integration method to the other
- automatic conversion of an existing Bot API connection into MTProto
- generalized provider/plugin framework
- account export / unlimited history scraping

## Implementation log

### #102 — complete

- Added connection-level `integration_mode` metadata with restart-safe migration of existing Telegram rows to `bot`.
- Added explicit Bot API vs MTProto capability metadata without changing canonical `telegram` routing identity.
- Generic connection APIs expose mode/capabilities while credentials remain encrypted and omitted from DTOs.
- Startup/reload adapter selection is connection-metadata driven; the MTProto runtime hook is intentionally completed by #103.
- Existing Bot API connections remain the default and continue to use the existing adapter/token path.
- Verification: focused schema/API/capability/selection tests plus `go test ./...` and `go vet ./...` in the tested-change workflow.

### #103 — complete

- Added `gotd/td` MTProto client authentication with phone code and optional 2FA using `PasswordWith`/`srpguard` so the 2FA value is consumed from wipeable memory.
- Added an encrypted control-store-backed gotd session storage adapter; API credentials, phone, and reusable session stay inside the encrypted connection blob. OTP, code hash, and 2FA are runtime-only.
- Pending MTProto connections now start as control-plane adapters, expose sanitized auth states, reconnect from authorized persisted sessions, and support explicit logout/session clearing.
- Added mode-specific authenticated admin routes for setup, code request/verification, and 2FA; generic Bot API token handling remains unchanged.
- MTProto session writes are excluded from credential-fingerprint reload logic so normal session persistence cannot trigger spurious adapter restarts.
- Verification: focused encrypted-state/auth/API tests plus `go test ./...` and `go vet ./...` in the tested-change workflow.

### #104 — complete

- Added MTProto live message transport for configured Telegram groups/supergroups/channels while preserving canonical transport identity `telegram`.
- Added restart-safe encrypted per-connection peer/access-hash persistence with on-demand dialog refresh, so ordinary outbound sends do not depend on fresh post-restart traffic.
- Added live text/media, native reply/topic routing, reactions, edits and deletes through gotd while preserving media limits and privacy boundaries.
- Self-originated bridge sends/mutations are suppressed while genuine linked-account user events remain routable; peer/session state remains isolated per Telegram connection.
- Verification: focused MTProto live-adapter tests plus `go test ./...` and `go vet ./...` in the tested-change workflow.

### #105 — complete

- MTProto discovery now enumerates all joined supported Telegram basic groups and supergroups/forums without waiting for an observed message; private dialogs and broadcast-only channels remain excluded by product policy.
- Discovery refreshes only encrypted operational peer/access-hash state; group/topic labels remain transient presentation metadata.
- MTProto target validation resolves directly against current joined groups, while Bot API observed discovery/validation behavior is unchanged.
- Added complete MTProto forum-topic enumeration with explicit General-topic ID `1`; no English label is fabricated when Telegram does not supply one.
- Added a capability-specific topic-discovery API available only to MTProto connections.
- Verification: focused transport/API discovery tests plus `go test ./...` and `go vet ./...` in the tested-change workflow.

### #106 — complete

- MTProto adapters now implement `transport.RecoverySource` with connection+endpoint-scoped stream keys and matching checkpoints on live ordinary-message ingress.
- Historical reads use bounded `messages.getHistory`, normalize through the same Telegram privacy/routing semantics, preserve replies/topics/media loaders, and emit deterministic chronological events through the existing recovery coordinator.
- Recovery honors cursor, max-event, max-age, media and cancellation bounds; private dialogs remain excluded and no raw history payloads are persisted/logged.
- Added an explicitly bounded MTProto-only manual backfill API that routes through `Coordinator.RecoverStream`; Bot API returns an unsupported-capability response.
- Verification: focused ordering/bounds/topic/media/cancellation/isolation/API tests plus `go test ./...` and `go vet ./...` in the tested-change workflow.

## Completion rule

This feature is complete only when #102 through #108 are all closed, the full regression suite passes, durable documentation reflects the final capability differences, and this temporary tracker is deleted from the branch.