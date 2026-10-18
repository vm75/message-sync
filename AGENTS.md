# message-sync agent guide

## Project overview

`message-sync` is a privacy-first Go service that synchronizes configured WhatsApp, Discord, and Telegram conversations. A transport-neutral canonical router owns synchronization semantics; transport packages authenticate accounts, normalize provider events, and perform provider operations without becoming the application's identity model.

Use KISS and YAGNI. Prefer standard-library Go, explicit data flow, small packages, and simple SQLite transactions; do not restore legacy behavior unless the current product needs it.

## Repository map

- `internal/privatedm/` — opt-in anonymous private relay, outside canonical group routing.

- `cmd/message-sync/` — CLI entry point (`run`, `validate-config`, and `version`).
- `internal/app/` — process wiring and the single ordered ingress worker.
- `internal/api/` — authenticated HTTP API and embedded Web UI.
- `internal/config/` — SQLite-backed routing configuration and validation.
- `internal/router/`, `internal/delivery/`, `internal/recovery/` — canonical routing, destination lanes, retries, and checkpoints.
- `internal/transport/{whatsapp,discord,telegram}/` — provider adapters.
- `internal/store/` — PII-free routing database (`sync.db`).
- `internal/controlstore/` — sensitive control-plane database (`control.db`).
- `internal/identity/`, `internal/safelog/` — HMAC identities and safe error logging.
- `internal/integration/` — mixed-transport reliability and privacy tests.
- `ref/` — ignored upstream reference material; never treat it as current architecture.

## Context and documentation routing

Start with targeted search and read only what the task needs:

1. [`README.md`](README.md) for current scope, setup, and user-facing behavior.
2. [`ARCHITECTURE.md`](ARCHITECTURE.md) for boundaries, data flow, and invariants.
3. [`docs/MESSAGE_PRESENTATION.md`](docs/MESSAGE_PRESENTATION.md) before changing sender attribution, WhatsApp PN/LID identity handling, Discord threads/forum posts, Telegram topics, reactions, polls, replies, edits, or any rendered bridge header.
4. The package being changed and its tests.
5. [`docs/FEATURE_COMPARISON.md`](docs/FEATURE_COMPARISON.md) only for capability or architectural comparisons.

Use [`TESTING.md`](TESTING.md) for the full reliability gate, [`docs/TESTING_GUIDE.md`](docs/TESTING_GUIDE.md) for manual provider testing, and [`DOCKERHUB.md`](DOCKERHUB.md) for published-image usage.

## Working commands

```sh
make run
make fmt
make test
make vet
make build
```

For container/runtime changes also run:

```sh
podman build -f Containerfile -t message-sync:dev .
podman compose -f compose.yml config
```

## Privacy and trust boundaries

- `sync.db` and application logs must contain no PII/PHI, credentials, message content, raw provider objects, external media URLs, or arbitrary provider error text (including phone numbers, WhatsApp JIDs/LIDs, Discord/Telegram user IDs or usernames, group/channel/guild names, message bodies, or auth tokens).
- Allowed routing state is limited to safe aliases, operational endpoint targets, random canonical IDs, opaque remote message IDs, HMAC actor IDs, emoji/reaction and aggregate poll state, non-content timestamps, and recovery cursors.
- `/data/whatsapp/<connection-id>.db` is isolated sensitive protocol state owned by whatsmeow. Never query it for application features or expose it through the API; keep decrypted-event and retry plaintext persistence disabled.
- `control.db` is the explicit sensitive boundary for accounts, sessions, encrypted bot credentials, audits, and experimental membership verification. Keep it separate from routing state and mode `0600` where supported.
- Membership evidence is short-lived private control-plane data for the experimental verification workflow. Human decisions are final, terminal decisions purge local evidence, and WhatsApp fulfillment must never add participants directly.
- Download message media only long enough to forward it. Do not persist media for convenience.
- Use `log/slog` with explicit safe fields and route arbitrary errors through `internal/safelog`.
- Never log endpoint remote target IDs, bot/webhook tokens, `IDENTITY_SECRET`, or provider structs.

## Change-control invariant

- Existing observable behavior is a compatibility contract unless the task explicitly requests a product change. Do not alter message formatting, defaults, routing semantics, persistence/privacy behavior, API payloads, transport behavior, UI workflow, or lifecycle semantics as a side effect of an unrelated fix or refactor.
- Before changing established behavior, identify the explicit user request or issue acceptance criterion that authorizes it. If none exists, preserve the behavior and make the smallest implementation change that satisfies the task.
- When shared code is touched, add regression tests for behavior that must remain unchanged. Do not reinterpret ambiguous requirements as permission to redesign existing behavior.
- Documentation and refactors must describe the current contract; they must not silently redefine it. Intentional behavior changes require code, tests, and permanent documentation to change together.

## Architecture invariants

- Preserve `Connection -> Endpoint -> ChildScope -> Sync Set`: connections authenticate, endpoints address configured parent conversations, child scopes represent Discord threads or Telegram topics, and sync sets define fan-out (with optional route-aware one-way Discord anonymization `anonymizeToDiscord`).
- Endpoint aliases must match `[A-Za-z0-9][A-Za-z0-9_-]{0,63}`, be safe to display, reference their owning connection, and belong to exactly one validated sync set.
- Provider message IDs are never global canonical IDs. `message_copies` owns bidirectional lookup and idempotent fan-out.
- Keep ingress deterministic with one canonical router worker. All three transports use the same router for creates, replies, reactions, polls, edits, deletes, and recovery.
- Outbound creates distinguish definite pre-acceptance failures from ambiguous provider outcomes. Never blindly retry an ambiguous create.
- Advance recovery checkpoints only when payload-dependent delivery is safe to forget; durable delivery intent alone is insufficient.
- Native replies and reactions are best effort when forbidden identity storage prevents reconstruction; use safe textual fallback.
- WhatsApp lifecycle echo suppression stays bounded and content-free, and must not suppress unmatched linked-device `FromSelf` mutations.
- Store configuration in `sync.db`; store Discord and Telegram credentials only as AES-256-GCM ciphertext in `control.db` using a domain key derived from `IDENTITY_SECRET`.
- Treat synchronized message presentation as a compatibility contract. Ordinary attribution is `<group>/<name> (<assigned-alias>)`; child attribution is `<group>/<child>/<name> (<assigned-alias>)`. Discord destinations on `anonymizeToDiscord` routes use only the assigned alias and strip phone numbers; other destinations retain ordinary names with aliases. Display name wins over phone; phone is a fallback only. There is no username-mode selector or configurable discriminator. Opaque child IDs and `[contexts ...]` never appear in forwarded text. See [`docs/MESSAGE_PRESENTATION.md`](docs/MESSAGE_PRESENTATION.md).
- Assign aliases only to accepted group-message authors, using `(sync_set_id, HMAC actor_id)` and a permanent instance-wide ordinal. Freeze the 256×256 vocabulary and bijective 16-bit slot permutation; numeric tiers begin at ordinal 65,537. Never reuse aliases/ordinals after deletion, change actor IDs, enroll from mentions/private traffic, or lose assignments during a settings save. Retain full aliases when truncating Discord names. WhatsApp assignment requires a verified canonical LID; unresolved PN-only authors fail safely before canonical/create state and checkpoint advancement.
- Sync-set settings use a shared create/edit modal. `membershipReviewEnabled` is a persisted per-set UI flag, not a browser preference or authorization boundary. Save tracks actual draft differences; preserve existing membership processing and private control-store data.
- Do not confuse child-context syntax with child-label persistence: `childContextDisplayMode=opaque` does not persist labels, while `friendly` may persist them for restart/replay presentation. Both modes use the same human-facing `group/child/name` syntax.

## SQLite and Go conventions

- Enable foreign keys, use committed schemas, transactions for related canonical/copy updates, uniqueness for duplicate safety, and bounded retention.
- The project does not have a general versioned migration framework. Schema changes that must preserve existing installations may use narrowly scoped idempotent initialization migrations with explicit upgrade tests. Do not claim compatibility with older development databases.
- Pass `context.Context` through blocking, network, and database operations.
- Prefer concrete types until an interface represents a real boundary.
- Validate inputs at configuration and transport edges. Wrap errors with operation context but never sensitive values.
- Make ownership and lifetime of large media buffers clear. Prefer table-driven tests where they improve readability.

## Release and container constraints

- The runtime image is non-root, writes persistently only to `/data`, and works with Docker, rootful Podman, and rootless Podman without privileged mode, host namespaces, or extra capabilities.
- Compose keeps the root filesystem read-only and drops all capabilities.
- `VERSION` records the repository's expected release version. Feature/fix PRs do not bump it; a dedicated release PR updates `VERSION` and `CHANGELOG.md`.
- The authoritative release event is an annotated `vX.Y.Z` (or supported pre-release) Git tag whose version exactly matches `VERSION`. Merging a `VERSION` change alone must never publish.
- `.github/workflows/release-images.yml` is tag-driven: it verifies tag == `VERSION`, runs tests/vet, publishes immutable exact-version multi-architecture images, moves `latest` only for stable releases, and creates the matching GitHub Release.
- Development builds report `development`; release builds inject the validated `VERSION` through linker flags. Follow `RELEASING.md` for the maintainer procedure.

## Definition of done

- Add or update tests for changed invariants and failure paths.
- Run `make fmt`, `make test`, and `make vet`; run container checks only when container/runtime files changed.
- Review the diff for unrelated edits and privacy-boundary regressions.
- Keep all documentation strictly in sync with implementation changes across the repository.

## Documentation maintenance

AI agents MUST keep all repository documentation in sync with the implementation for any changes to the project. When introducing, modifying, or deprecating features, settings, API endpoints, schema fields, UI capabilities, or container behaviors, update the corresponding documentation files as part of the same change set. Never leave documentation stale or out of sync.

- `README.md`: Setup, configuration (environment variables and runtime settings), core capabilities, key features, and user-facing workflows.
- `DOCKERHUB.md`: Public container overview, key features, image names, tags, environment variables, volume layouts, compose usage, and security hardening.
- `ARCHITECTURE.md`: Components, dependency direction, data flow, schema definitions, privacy boundaries, lifecycle flow, failure/retry semantics, and REST API surface.
- `AGENTS.md`: Agent workflow, repository map, definition of done, and mandatory constraints.
- `docs/MESSAGE_PRESENTATION.md`: Exact sender attribution and child-context presentation syntax; update it whenever rendered bridge headers or identity precedence change.
- `CHANGELOG.md`: Release notes, notable changes, and version history.
- `RELEASING.md`: Maintainer-only release preparation, tagging, publication, immutability, and verification procedure.
- `TESTING.md` / `docs/TESTING_GUIDE.md`: Automated test commands, test coverage, manual provider testing steps, and troubleshooting.
- `docs/FEATURE_COMPARISON.md`: Capability or architectural comparisons with upstream or alternative designs.


## Cross-group reference invariants

Private DM relays reuse this resolver and require both the global and sync-set
administrator switches (default off). Native reply mappings authorize only the
recorded addressee in the exact owning connection/private chat, expire after
24 hours and recheck current membership on every hop. No private content or
native inbox identity enters `sync.db`, logs, metrics or admin JSON. Persist
only encrypted content-free routing/inbox/cleanup tokens in `control.db`.
Private edits/deletes/revokes are ignored. WhatsApp cleanup may use only a
per-message Delete for Me AppState operation on feature-owned private copies.
Never substitute group deletion, REVOKE, whole-chat cleanup, Discord deletion
or Telegram Bot API deleteMessage. MTProto personal inboxes remain unmanaged.

Cross-group participant syntax is only `@{Amber Badger}` (with optional numeric
tier), separate from provider-native `@user`. Aliases are globally unique but
identify actors within one sync set. Users learn addresses from forwarded author
attribution; read tracking, directories and mention-only enrollment are out of
scope. Case-insensitive lookup must still enforce source-set ownership. Public
`Pn`, real-name, discriminator and endpoint-qualified lookup are removed.
Use `sync_set_actors` for assigned HMAC actor/alias/ordinal state and
`participant_addresses` for endpoint observations. Never persist names, phones,
raw user identities or content. Each actual destination verifies current
membership before native rendering. Preserve the exact authored token whenever
native rendering is unavailable, including on anonymized routes. Destination
adapters own native identity lookup without cross-provider guessing.
WhatsApp requires matching verified LID text and metadata and must not expose a
PN. Discord and Telegram Bot API retain native user bindings only in memory.
Discord verifies channel access/private-thread membership and uses an explicit
user allowlist. Verified explicit references may tag Discord members on
anonymized routes; author and ordinary mention anonymization remain active.
Live ingress and accepted history restore Discord observations. A cold-cache
lookup may compare the authorized HMAC actor with paginated current guild
identities under a bounded timeout, never by display name. It requires optional
Server Members Intent and stays literal when unavailable; do not add a privileged
gateway intent. Telegram MTProto and edits stay literal and non-notifying.
Private lookup selects the alias's owning set globally, then requires current
sender membership and existing author assignments for both sides. Deduplicate
only equivalent routes to the same verified inbox; fail on distinct inboxes.
Native reply mapping, current membership and exact account/inbox bindings remain
mandatory on every hop. No private content or inbox identity enters `sync.db`.
