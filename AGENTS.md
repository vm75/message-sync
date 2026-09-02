# AGENTS.md

## Purpose

`message-sync` is a privacy-first message synchronization service written in Go. WhatsApp groups, configured Discord channels, and configured Telegram groups/supergroups synchronize through the same canonical transport boundary; Discord and Telegram must remain transport adapters rather than becoming the application’s central identity.

Use **KISS** and **YAGNI** aggressively. Prefer standard-library Go, explicit data flow, small packages, and simple SQLite transactions. Do not port legacy functionality merely because it existed before.

## Read order

Keep context lean. Read only what the current task requires:

1. `README.md` for product scope and commands.
2. `ARCHITECTURE.md` for invariants and data flow.
3. The package(s) being changed and their tests.
4. `docs/ASPIRATIONAL_FEATURES.md` only for aspirational/deferred functionality.

The previous implementation or prototypes may be consulted as a behavioral reference for a specific feature. Do not copy legacy architecture wholesale.

## Non-negotiable privacy invariants

Application persistence (`sync.db`) and application logs must contain **no PII/PHI**.

Never persist or log:

- participant phone numbers;
- participant WhatsApp JIDs/LIDs;
- push names, contact names, profile names, or designations;
- message bodies, quoted text, captions, polls, contact cards, or location payloads;
- media bytes, thumbnails, filenames that may contain personal information, or external media URLs;
- raw WhatsApp/whatsmeow, Discord gateway/API, or Telegram Bot API update/message/user/chat objects;
- Discord user IDs, usernames/display names, guild/channel names, bot tokens, webhook tokens/URLs;
- Telegram user IDs, usernames/display names, chat titles/usernames/invite links, bot tokens, raw API errors, and file URLs;
- `IDENTITY_SECRET` or any credential/key material.

Allowed in `sync.db`:

- random canonical message IDs;
- configured endpoint aliases and their transport-specific operational remote target IDs;
- opaque remote message IDs;
- HMAC-derived actor IDs;
- emoji values needed for reaction state;
- non-content timestamps and recovery cursors.

`whatsapp.db` is a separate sensitive protocol-state exception owned by whatsmeow. Do not query it for application features. Keep whatsmeow decrypted-event and retry plaintext persistence disabled. Never expose `whatsapp.db` through an API or admin UI.

If a proposed feature cannot satisfy these rules, design it as an explicit optional PII subsystem rather than weakening the core.

## Architecture rules

- The canonical router owns cross-endpoint synchronization semantics.
- Transport adapters own platform protocol details.
- WhatsApp, Discord, and Telegram use the same canonical router for end-to-end text/media, native representable polls, aggregate-only live-result companions, and reply/reaction/edit/delete lifecycle routing. Telegram Bot API ingress/outbound is registered through the same transport adapter registry and single canonical router loop; Telegram chat discovery is a bounded in-memory observation cache exposed only through authenticated administration, and only selected opaque chat IDs may become endpoint configuration. Provider-specific poll mapping remains at transport boundaries, and no poll text or raw voter identity may be persisted. Discord discovery/UI, parent-flattened thread/forum ingress, native polls/vote events, mention fallbacks, textual poll fallback rendering, and unsupported-format handling are implemented. Dynamic thread endpoints, automatic outbound forum-post creation, directional routing, MTProto, provider-native counter injection, and exact poll-close parity remain deferred.
- Never use a Discord, Telegram, or WhatsApp message ID as the global canonical ID.
- `message_copies` must make fan-out retryable and idempotent.
- Outbound create retries must distinguish definite pre-acceptance failure from ambiguous provider outcomes; never blindly retry an ambiguous create.
- Recovery checkpoints may advance only after payload-dependent delivery is safe to forget; recording delivery intent alone is insufficient.
- Process ingress deterministically; start with one router worker.
- Download media only long enough to forward it. Do not add media persistence for convenience.
- Native replies/reactions are best effort when destination metadata cannot be reconstructed without forbidden identity storage; use a textual attribution fallback.
- WhatsApp bridge lifecycle echo suppression is bounded, content-free, and must not drop unmatched linked-device `FromSelf` mutations.
- Configuration is stored in SQLite (`sync.db`). Secrets come from environment variables or secret mounts, never database tables.
- Endpoint aliases are application-safe routing IDs: they must match `[A-Za-z0-9][A-Za-z0-9_-]{0,63}`, must not encode a JID/phone/name/group/channel subject, and every validated configured endpoint must belong to exactly one sync set. Transport remote target IDs are operational addressing only and must never be logged.

## SQLite rules

Use two databases:

- `/data/whatsapp.db`: whatsmeow protocol/session store.
- `/data/sync.db`: application routing state.

For `sync.db`:

- enable foreign keys;
- use committed migrations;
- use transactions for canonical message + copy updates;
- use uniqueness constraints to make duplicate delivery harmless;
- enforce retention in batches;
- do not store raw transport identity to simplify rare features.

## Go style

- Keep packages small and responsibility-oriented.
- Prefer interfaces only at real boundaries.
- Pass `context.Context` through blocking/network/database operations.
- Wrap errors with useful operation context but never sensitive values.
- Use `log/slog` with explicit safe fields; never log complete protocol structs.
- Route arbitrary errors through the safe logging helper so raw error text cannot enter application logs.
- Make ownership/lifetime of large media buffers obvious.
- Validate inputs at config and transport boundaries.
- Prefer table-driven tests when they improve clarity.

## Testing

Every feature must add tests for its invariants. As implementation lands, cover at least:

- SQLite config validation and persistence;
- HMAC stability and non-disclosure;
- SQLite migrations and uniqueness constraints;
- canonical lookup in both directions;
- duplicate/loop prevention;
- partial fan-out followed by restart/retry;
- reply mapping and fallback behavior;
- reaction add/remove and actor separation;
- edit/delete propagation;
- media size limits and absence of persistent media;
- offline recovery bounds;
- PII-safe logging where feasible.

Run before completing changes:

```sh
make fmt
make test
make vet
```

For container/runtime changes also run:

```sh
podman build -f Containerfile -t message-sync:dev .
podman compose config
```

## Rootless/container rules

The runtime container must:

- run as a non-root user;
- work with Docker, rootful Podman, and rootless Podman;
- use `/data` as its only persistent writable location;
- use a read-only root filesystem in Compose;
- require no privileged mode, host networking, host PID namespace, or extra Linux capabilities;
- use `Containerfile`, `.containerignore`, and `compose.yml`;
- avoid runtime-specific behavior unless isolated and documented.

## Version/release rules

`.github/workflows/release-images.yml` must trigger only when `VERSION` changes on `main`. Normal development builds identify as `development`. Release builds inject `VERSION` with linker flags.

Do not add image-publishing triggers for ordinary pushes, pull requests, tags, schedules, or manual dispatch unless the project owner explicitly changes this policy.

## Scope discipline

MVP scope is defined in `README.md`. Aspirational and deferred features are tracked in `docs/ASPIRATIONAL_FEATURES.md`.

For a future feature:

1. state whether it changes the privacy model;
2. keep optional integrations behind narrow interfaces;
3. avoid adding cloud/web dependencies merely because the previous implementation used them;
4. extend the canonical transport model instead of special-casing a platform.

## Documentation maintenance

At the end of every feature add/delete/modify:

- update `README.md` for user-visible behavior/configuration/deployment changes;
- update `DOCKERHUB.md` when deployment examples, container features, or configuration options change;
- update `ARCHITECTURE.md` for data flow/schema/privacy/component changes;
- update `AGENTS.md` when contributor guidance or invariants change;
- update `docs/ASPIRATIONAL_FEATURES.md` when scope changes.

Keep context and docs lean; avoid duplicating large authoritative sections.
