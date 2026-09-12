# message-sync agent guide

## Project overview

`message-sync` is a privacy-first Go service that synchronizes configured WhatsApp, Discord, and Telegram conversations. A transport-neutral canonical router owns synchronization semantics; transport packages authenticate accounts, normalize provider events, and perform provider operations without becoming the application's identity model.

Use KISS and YAGNI. Prefer standard-library Go, explicit data flow, small packages, and simple SQLite transactions; do not restore legacy behavior unless the current product needs it.

## Repository map

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

- Preserve `Connection -> Endpoint -> ChildScope -> Sync Set`: connections authenticate, endpoints address configured parent conversations, child scopes represent Discord threads or Telegram topics, and sync sets define fan-out.
- Endpoint aliases must match `[A-Za-z0-9][A-Za-z0-9_-]{0,63}`, be safe to display, reference their owning connection, and belong to exactly one validated sync set.
- Provider message IDs are never global canonical IDs. `message_copies` owns bidirectional lookup and idempotent fan-out.
- Keep ingress deterministic with one canonical router worker. All three transports use the same router for creates, replies, reactions, polls, edits, deletes, and recovery.
- Outbound creates distinguish definite pre-acceptance failures from ambiguous provider outcomes. Never blindly retry an ambiguous create.
- Advance recovery checkpoints only when payload-dependent delivery is safe to forget; durable delivery intent alone is insufficient.
- Native replies and reactions are best effort when forbidden identity storage prevents reconstruction; use safe textual fallback.
- WhatsApp lifecycle echo suppression stays bounded and content-free, and must not suppress unmatched linked-device `FromSelf` mutations.
- Store configuration in `sync.db`; store Discord and Telegram credentials only as AES-256-GCM ciphertext in `control.db` using a domain key derived from `IDENTITY_SECRET`.
- Treat synchronized message presentation as a compatibility contract. In default `push_name` mode, ordinary attribution is `<group-alias>/<name>` and child-context attribution is `<group-alias>/<thread-or-topic-label>/<name>`. Opaque ChildScope IDs and `[contexts ...]` preambles are routing metadata and must never appear in forwarded messages. Display name wins over phone number; phone is only a fallback when no display name exists. See [`docs/MESSAGE_PRESENTATION.md`](docs/MESSAGE_PRESENTATION.md).
- Do not confuse child-context syntax with child-label persistence: `childContextDisplayMode=opaque` does not persist labels, while `friendly` may persist them for restart/replay presentation. Both modes use the same human-facing `group/child/name` syntax.

## SQLite and Go conventions

- Enable foreign keys, use committed schemas, transactions for related canonical/copy updates, uniqueness for duplicate safety, and bounded retention.
- The current schemas initialize fresh databases; there is no upgrade migration path. Do not claim compatibility with older development databases.
- Pass `context.Context` through blocking, network, and database operations.
- Prefer concrete types until an interface represents a real boundary.
- Validate inputs at configuration and transport edges. Wrap errors with operation context but never sensitive values.
- Make ownership and lifetime of large media buffers clear. Prefer table-driven tests where they improve readability.

## Release and container constraints

- The runtime image is non-root, writes persistently only to `/data`, and works with Docker, rootful Podman, and rootless Podman without privileged mode, host namespaces, or extra capabilities.
- Compose keeps the root filesystem read-only and drops all capabilities.
- `VERSION` is the release source. Development builds report `development`; release builds inject `VERSION` with linker flags.
- `.github/workflows/release-images.yml` publishes only when `VERSION` changes on `main`. Do not add other publishing triggers without explicit owner direction.

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
- `TESTING.md` / `docs/TESTING_GUIDE.md`: Automated test commands, test coverage, manual provider testing steps, and troubleshooting.
- `docs/FEATURE_COMPARISON.md`: Capability or architectural comparisons with upstream or alternative designs.
