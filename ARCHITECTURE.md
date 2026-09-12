# Architecture

## Purpose and scope

`message-sync` is one Go process that synchronizes configured WhatsApp, Discord, and Telegram conversations. This document describes the current component ownership, trust boundaries, routing flow, persistence model, and failure semantics. Provider setup belongs in the [testing guide](docs/TESTING_GUIDE.md); container usage belongs in [Docker Hub documentation](DOCKERHUB.md).

The design optimizes for:

1. no PII/PHI in routing persistence or application logs;
2. deterministic, idempotent synchronization;
3. bounded recovery without retaining message payloads;
4. transport-neutral identity and routing;
5. simple rootless self-hosting.

## System context

```text
 provider accounts/bots                         authenticated operators
          |                                               |
          v                                               v
 WhatsApp / Discord / Telegram adapters              HTTP API + Web UI
          |                                               |
          +---- normalized transient events --------------+
                                  |
                                  v
                     single canonical router worker
                                  |
                +-----------------+-----------------+
                |                 |                 |
                v                 v                 v
        per-destination     SQLite routing     connection/adaptor
        delivery lanes      and checkpoints       dispatch
                |                                     |
                +------------ provider sends --------+
```

The HTTP server, router, adapters, connection manager, recovery coordinator, and stores run in one process. Provider payloads and display metadata exist only at transport and routing boundaries while an event is being processed.

## Components and dependency direction

| Component | Responsibility |
|---|---|
| `cmd/message-sync` | CLI selection, structured logger setup, signal handling, and process start. |
| `internal/app` | Opens stores, constructs shared services, manages dynamic adapters, starts the HTTP server, and feeds one ordered ingress loop. |
| `internal/api` | Authentication/RBAC, configuration and connection administration, delivery status, experimental membership workflows, and embedded static UI. |
| `internal/connection` | Registers, replaces, stops, and dispatches to multiple independently authenticated adapters. |
| `internal/transport` | Canonical transient event and outbound operation contracts. |
| `internal/transport/*` | Provider authentication, discovery, normalization, media loading, recovery, and outbound operations. |
| `internal/router` | Canonical IDs, sync-set fan-out, copy lookup, replies/reactions/polls/edits/deletes, loop suppression, and delivery intent. |
| `internal/delivery` | Bounded FIFO lane per destination with independent retry/backoff. |
| `internal/recovery` | Ordered provider checkpoints and safe advancement. |
| `internal/store` | PII-free configuration, canonical mappings, delivery ledger, poll aggregates, and recovery cursors in `sync.db`. |
| `internal/controlstore` | Sensitive accounts, sessions, encrypted connection credentials and poll presentation text, audit data, and experimental membership state in `control.db`. |
| `internal/identity` | Stable HMAC-derived actor identifiers. |
| `internal/safelog` | Fixed safe error classification at the logging boundary. |

Dependencies point inward toward canonical contracts. Adapters may translate provider data into `transport.Incoming` and execute `transport.Outgoing`; they do not define cross-endpoint semantics or global identity. The router depends on adapter interfaces and application stores, never on a provider's raw event model. `controlstore` does not participate in message routing.

## Addressing hierarchy

The application preserves four distinct concepts:

- **Connection** authenticates one WhatsApp account, Discord bot, or Telegram bot.
- **Endpoint** gives a safe alias to one configured parent conversation owned by a connection.
- **ChildScope** carries an opaque Discord thread/forum-post or Telegram topic context for a message.
- **Sync Set** groups endpoint aliases for all-to-all fan-out.

Endpoint aliases are the only routing identifiers safe for display and logging. They match `[A-Za-z0-9][A-Za-z0-9_-]{0,63}` and must not encode a person, phone number, JID, group subject, guild, or channel name. Provider target IDs are retained only for operational addressing and are never canonical IDs.

Child scopes are not endpoint rows and do not create independent routes. Opaque scope metadata follows canonical reply/lifecycle lineage so a reply can return to the correct thread or topic. Presentation headers have no routing authority. If the scoped destination cannot be reconstructed, delivery fails safely instead of falling back to the parent conversation.

Flattened child messages use one human-facing attribution header: `<group-alias>/<thread-or-topic-label>/<username>`. Opaque child IDs never enter forwarded message text. The default `opaque` child-context mode keeps thread/topic names transient and falls back to the generic `thread`/`topic` label when a live name is unavailable. The explicit `friendly` mode may retain bounded normalized labels in the presentation-only `child_scope_labels` table so names survive restart/replay; switching back to `opaque` clears them. Labels never participate in routing, identity, or copy lookup.

Telegram basic-group to supergroup migrations are handled as addressing maintenance: the adapter recognizes the migration service message and transactionally updates only the matching endpoint's `remote_id` in `sync.db`, preserving the alias, sync set, and canonical lineage. Telegram mentions are sanitized at the adapter boundary: `text_mention` entities are reduced to HMAC actor IDs plus transient display labels, and `@username` entities are rewritten to the generic `@mention` fallback to keep raw usernames out of canonical state.

## Persistence and trust boundaries

All persistent files live below `DATA_DIR` (`/data` by default).

### `sync.db`: routing boundary

`internal/store/schema.sql` initializes the application routing database. It may contain:

- safe endpoint aliases, connection IDs, and provider target IDs needed for addressing;
- random canonical message IDs and opaque per-endpoint message-copy IDs;
- opaque child-scope IDs and optional friendly display labels;
- HMAC actor IDs, reaction emoji, poll option indexes/hashes and aggregate counts;
- content-free delivery operation state, create sub-step state, timestamps, and recovery cursors.

It must not contain message or quoted text, captions, poll questions/options, media, participant identity, provider display names, raw updates, credentials, external file URLs, or arbitrary error strings. `message_copies` maps `(endpoint, remote message ID)` to a random canonical ID and enforces one copy per canonical message and endpoint.

The store enables foreign keys and uses uniqueness constraints and transactions for duplicate safety. Retention removes old canonical state in bounded batches. The current schema is a fresh-database schema: no schema-version dispatcher or compatibility migration path exists for older development databases.

### `control.db`: sensitive application boundary

`internal/controlstore/schema.sql` initializes a separate mode-`0600` database for:

- users, roles, hashed sessions, invites and reset tokens;
- fixed-field audit records;
- transport connection records and encrypted Discord/Telegram credentials;
- experimental membership configuration, applications, verification challenges, decisions, fulfillment state, and bounded advisory assessments.

Bot credentials are encrypted with AES-256-GCM. The encryption key is domain-derived from `IDENTITY_SECRET`, and each write receives a fresh nonce. Plaintext tokens are accepted only at write boundaries, remain transient, and are never returned by read APIs. Losing or changing `IDENTITY_SECRET` makes stored bot credentials unreadable and changes actor HMACs.

Membership verification (experimental): Membership definitions and answers stay in this control-plane boundary and are shared per sync set through endpoint resolution. Decisions remain human-authorized; automated checks and optional image analysis are advisory only. WhatsApp fulfillment uses join approval and an invite flow, never direct participant addition.

Evidence files use random names under `/data/membership-evidence`, with a private directory and mode-`0600` regular files where supported. Final approve/reject decisions purge local evidence immediately. Startup and daily bounded cleanup remove expired control artifacts, terminal requests after retention, queued evidence, and old unreferenced files.

### WhatsApp protocol stores

Each WhatsApp connection owns `/data/whatsapp/<connection-id>.db`. These files contain whatsmeow credentials, sessions, cryptographic state, and provider identity metadata, so they are sensitive protocol-state exceptions rather than application databases.

The parent directory is mode `0700` and database files are mode `0600` where supported. Application features must never query them, copy their identity data into `sync.db`, or expose them through the API. Whatsmeow decrypted-event buffering and retry-message plaintext storage remain disabled, and its protocol/database logging is suppressed.

Deleting a WhatsApp connection removes only its validated database path and SQLite sidecars. QR pairing is globally serialized because whatsmeow companion properties are process-global; already-connected accounts continue running during another account's pairing.

### Logs and transient data

Application logs use explicit safe fields such as endpoint aliases, canonical IDs, counts, and fixed event/failure classes. Raw provider structs, remote target IDs, participant data, tokens, payloads, and arbitrary provider error strings never cross the logging boundary. `internal/safelog` converts unexpected errors into fixed safe classes.

Message media is loaded into memory only after an event is accepted for routing, bounded by configuration/provider limits, and released after forwarding. Membership evidence is the only deliberate on-disk content exception (for the experimental membership verification workflow) and belongs to the control plane described above.

## Message and lifecycle flow

1. A connection-specific adapter accepts a live or recovered provider event.
2. The adapter validates its configured target and normalizes provider objects into a transient `transport.Incoming`. Participant identity becomes an HMAC actor ID at this edge.
3. The connection manager merges adapter event streams; `internal/app` passes them through one ordered recovery/router worker.
4. The router resolves the source alias and sync set, detects duplicates or bridge echoes, and assigns or looks up a random canonical ID.
5. Canonical metadata and destination delivery intent are recorded without persisting payload content.
6. Each destination operation enters its own bounded FIFO lane. Slow or failing destinations do not block healthy lanes.
7. The registry dispatches the operation through the destination endpoint's owning connection and adapter.
8. A successful provider result records the opaque remote copy ID. Replies and later lifecycle mutations resolve through the same canonical/copy mapping.
9. Recovery advances a provider checkpoint only when the router reports that payload-dependent delivery is safe to forget.

Local-prefix suppression occurs before canonicalization and media download using configured prefix strings (e.g. `!local`, `#local`, `//`, `[local]`). Its restart-safe marker contains only endpoint and opaque message ID; edits, reactions, and deletes for the suppressed message remain local.

## Semantics owned by the router

### Creates and copies

Provider message IDs never become canonical IDs. Uniqueness constraints make replayed ingress harmless, while `message_copies` lets partial fan-out resume only missing destinations. A destination create has explicit compatibility and primary sub-steps so a successful sub-step is not repeated merely because copy persistence was interrupted.

Providers do not all support deterministic client-assigned create IDs. A definite pre-acceptance transient failure may retry with bounded backoff; an ambiguous outcome is marked `awaiting_replay` and is never blindly resent.

### Replies and child scopes

The router maps a source reply target to its canonical message, then resolves the destination copy and any destination-specific child scope. When native reply metadata cannot be reconstructed without forbidden content or identity storage, it emits a deterministic safe textual fallback. On Discord, incoming messages use a gateway bot while outbound messages use bridge-managed webhooks for transient sender attribution; because Discord's webhook API does not support `message_reference`, mapped replies render as attributed webhook messages with clickable markdown links to the quoted copy rather than native reply bubbles.

### Reactions, edits, and deletes

Reaction state keys use canonical message, source endpoint, and HMAC actor identity. Adds, changes, and removals propagate to known copies with loop suppression. Edits and deletes use canonical copy lookup and preserve lane ordering. Deletes tombstone canonical state to prevent replay resurrection.

WhatsApp bridge lifecycle echoes use bounded, expiring, one-shot in-memory markers. An unmatched linked-device `FromSelf` mutation continues through normal routing.

### Polls

Poll questions and option labels are retained only in encrypted control-plane storage. The routing store retains canonical option positions, opaque hashes or provider references where required, aggregate counts, and bridge-owned result-companion IDs—never voters or labels or poll presentation text.

Representable polls use native WhatsApp, Discord, and Telegram structures. Cross-endpoint Discord and Telegram polls include the source label in the visible native poll presentation; Discord's platform author remains the bot, and Telegram polls are created as non-anonymous polls whose platform author remains the bot. Unsupported option counts, lengths, answer modes, durations, or media combinations use deterministic text. Each endpoint receives one editable aggregate-only live-results companion. Poll questions and option labels are encrypted in `control.db` using the identity-derived credential key, while `sync.db` retains only option indexes/hashes and aggregate state, so live results can be reconstructed after restart without weakening the routing database's privacy boundary. Replying with any configured aggregation trigger phrase (default `aggregate-response`, supporting multiple space-separated triggers) to any poll copy produces an immediate on-demand aggregate summary and suppresses the trigger message; those triggered result messages are also edited when the poll aggregate changes. Telegram Bot API can provide absolute snapshots for bot-created polls but not complete ongoing results for arbitrary human-created source polls; those contributions remain explicitly partial rather than introducing MTProto identity storage. Telegram MTProto records the server-returned poll ID and answer keys for outbound correlation, refreshes minimal aggregate updates through `messages.getPollResults`, and normalizes per-voter updates for non-anonymous bridge-created polls directly to canonical option indexes and an HMAC actor identity. MTProto bridge text/captions/edits use native Telegram entities rather than raw transport-neutral markup.

## Failure, retry, and recovery

Destination lanes are independent, bounded, and ordered. Failures are classified into fixed safe categories; transient and rate-limited failures receive bounded retry/backoff, while permanent failures remain inspectable in the content-free delivery ledger.

Because payloads are not durable, a process restart cannot manufacture a retry body from delivery intent. Active create work becomes replay-dependent: the source provider's bounded recovery must emit the original event again, which reuses existing canonical/copy state. Recording intent is therefore not enough to advance the source checkpoint.

Recovery is provider-limited:

- WhatsApp replays bounded HistorySync data oldest-first and cannot guarantee offline reaction/delete reconstruction.
- Discord reads a bounded channel-history window after its accepted cursor and cannot reconstruct every offline lifecycle transition.
- Telegram resumes retained Bot API updates from a connection-scoped cursor; Telegram controls the retention window.

Recovery streams are ordered and single-flight. A failed or pending position blocks later checkpoint advancement so a gap cannot be skipped.

## Configuration and administration flow

The embedded UI calls the authenticated HTTP API. The first setup creates an administrator; later sessions and role checks are server-side. Configuration changes are validated, committed to SQLite, then applied to the router, registry, and active adapters without restarting the process.

Connections and endpoints remain separate. A connection may own several endpoints, endpoint reassignment is limited to a compatible transport, and a referenced connection cannot be deleted. Discord and Telegram token replacement stops the old adapter before registering the replacement. Discovery data is connection-scoped and transient unless an operator deliberately creates a safe endpoint alias.

Experimental membership pipeline mutations resolve the selected endpoint and sync set but do not copy provider addressing into `control.db`. Public intake exposes only the bounded form needed by the applicant; review and final decisions require authenticated human roles.

### HTTP API surface

The daemon hosts both the embedded SPA and the authenticated HTTP REST API on port 8080:

| Route | Methods | Access | Purpose |
|---|---|---|---|
| `/health` | `GET` | Public | Process health probe (`{"status":"ok"}`). |
| `/api/auth/status` | `GET` | Public | Reports whether initial admin setup is required. |
| `/api/auth/setup` | `POST` | Public (first run) | Creates first admin account with bcrypt password in `control.db`. |
| `/api/auth/login`, `/logout` | `POST` | Public / Auth | Establishes or terminates server-side session cookie / bearer token. |
| `/api/auth/change-password` | `POST` | Authenticated | Updates current user password and revokes other sessions. |
| `/api/users`, `/api/users/invites` | `GET`, `POST` | Admin | Manages users, roles (`admin`, `operator`), invites, and active status. |
| `/api/audit` | `GET` | Admin | Reads fixed-field audit records from `control.db`. |
| `/api/connections` | `GET`, `POST` | Authenticated / Admin | Lists connections or creates them with AES-256-GCM encrypted bot tokens. |
| `/api/connections/{id}` | `GET`, `PUT`, `DELETE`| Authenticated / Admin | Updates label/state or deletes unreferenced connection. |
| `/api/connections/{id}/status` | `GET` | Authenticated | Reports safe transport readiness and connection health. |
| `/api/connections/{id}/discovery`| `GET` | Authenticated | Returns transient discovered targets (channels, chats, groups). |
| `/api/connections/{id}/pair`, `/logout` | `POST` | Admin | Serialized WhatsApp QR pairing lifecycle and session logout. |
| `/api/endpoints` | `GET`, `POST` | Authenticated | Lists or creates safe endpoint aliases mapping to connection targets. |
| `/api/endpoints/{alias}` | `GET`, `PUT`, `DELETE`| Authenticated | Updates alias, sync set, or deletes endpoint. |
| `/api/sync-sets` | `GET`, `POST` | Authenticated | Lists or creates sync sets for fan-out between endpoint aliases. |
| `/api/sync-sets/{id}` | `GET`, `PUT`, `DELETE`| Authenticated | Updates sync set endpoints or deletes sync set. |
| `/api/config` | `GET`, `PUT` | Authenticated | Reads or modifies `global_config` in `sync.db` with live router reload. |
| `/api/delivery/status` | `GET` | Authenticated | Real-time content-free delivery health, lane depth, and failure classes. |
| `/api/verification/pipelines` | `GET`, `POST` | Admin | Manages experimental membership intake pipelines bound to endpoint sync sets. |
| `/api/sync-sets/{id}/membership` | `GET`, `PUT` | Admin | Configures experimental sync-set membership instructions and custom fields. |
| `/api/verification/requests` | `GET` | Authenticated | Reviews pending email-verified membership applications (experimental). |
| `/api/verification/requests/{id}/decision` | `POST` | Authenticated | Human-authorized approve/reject/needs-review decision (experimental). |
| `/api/verification/join/{token}` | `GET`, `POST` | Public | Public membership application intake form submission (experimental). |
| `/api/verification/email/verify` | `GET`, `POST` | Public | Validates email challenge token (experimental). |

## Container and release model

The multi-stage `Containerfile` produces a static Go binary in an Alpine runtime. The runtime user is UID/GID 1000, `/data` is the only persistent writable location, and the supplied Compose service uses a read-only root filesystem, a bounded `/tmp` tmpfs, no new privileges, and no Linux capabilities.

Development builds report `development`. `VERSION` is the release source injected through linker flags. The release workflow runs only when `VERSION` changes on `main` and publishes `linux/amd64` and `linux/arm64` images to Docker Hub and GHCR with the version and `latest` tags.

## Extension rules

- Add provider behavior at an adapter edge and express shared behavior through canonical transport types and the router.
- Do not make Discord, Telegram, or WhatsApp identifiers the application identity model.
- Keep optional PII features behind an explicit control-plane interface; never weaken `sync.db` or logging rules.
- Do not persist media or provider payloads to simplify retries or rare reply/reaction reconstruction.
- Prefer one-worker deterministic ingress and SQLite constraints before adding distributed coordination.

## Verification

Package and integration tests cover configuration validation, HMAC stability, schema constraints, canonical lookup, duplicate/loop prevention, partial fan-out and replay, lifecycle propagation, media bounds, recovery gaps, dynamic connections, and privacy canaries. Run the commands in [TESTING.md](TESTING.md) before release-relevant changes.
