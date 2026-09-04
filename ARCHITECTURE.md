# Architecture

## 1. Goals

`message-sync` is a small privacy-first message synchronization daemon. MVP priorities are:

1. no PII/PHI in application persistence or application logs;
2. deterministic, restart-safe synchronization;
3. simple rootless self-hosting;
4. transport adapters for WhatsApp (`tulir/whatsmeow`), Discord (gateway/webhooks), and Telegram (Bot API long polling);
5. transport-neutral canonical IDs so transport adapters do not become canonical identity.

## 2. Trust and persistence boundaries

```text
                             IDENTITY_SECRET
                           environment/secret
                                    |
                                    v
                              +----------+
                              | Identity |
                              +----+-----+
                                   |
                                   v
                           +------------------+       /data/sync.db
  REST API / Web UI <----->|  internal/api    |<----> config &
  (port 8080)              +--------+---------+       PII-free app state
                                    |
                                    v
                           +------------------+
  events ----------------->| Canonical Router |
                           +--------+---------+
                                    |
                         +----------+----------+----------+
                         |                     |          |
                         v                     v          v
                +------------------+   +------------------+   +------------------+
                | WhatsApp Adapter |   | Discord Adapter  |   | Telegram Adapter |
                | whatsmeow        |   | gateway/webhook  |   | Bot API polling  |
                +--------+---------+   +------------------+   +------------------+
                         |
                         +--------------------> /data/whatsapp/<connection-id>.db
                                               sensitive protocol state
```

### `/data/whatsapp/<connection-id>.db`

Owned by whatsmeow. Each configured WhatsApp connection maintains an isolated protocol SQLite database file located at `/data/whatsapp/<connection-id>.db`. The parent directory is restricted to mode `0700` and each database file is restricted to mode `0600`. It stores linked-device credentials, Signal/session keys, app-state/protocol data and may contain WhatsApp identifiers/contact metadata. It is sensitive and is the only protocol-state exception to the application’s zero-PII persistence rule.

Requirements:

- `EnableDecryptedEventBuffer = false`;
- `UseRetryMessageStore = false`;
- never expose/query it for product features;
- never copy contact/LID data into `sync.db`;
- protect it with restrictive filesystem permissions (`0700` directory, `0600` file) and encrypted storage where appropriate;
- connection deletion removes only its own isolated protocol database file and SQLite WAL/SHM sidecars.

The daemon opens each connection database with the CGO-free SQLite driver, enables SQLite foreign keys, wraps the connection with whatsmeow `sqlstore`, and restricts the database file to mode `0600`. Both the whatsmeow client logger and sqlstore logger are no-op so protocol structs, identifiers, payloads, and arbitrary protocol errors cannot bypass the application safe-log boundary.

Active QR pairing across WhatsApp connections is globally serialized because companion device properties are configured package-globally in whatsmeow. Already-connected WhatsApp accounts continue operating while another connection pairs; simultaneous pairing attempts receive an explicit busy conflict response.

### `sync.db`

Owned by message-sync and designed to remain PII/PHI-free. Initial schema is in `internal/store/schema.sql`.

The application initializes this schema only for a fresh database. The product is pre-release, so the store has no schema-version table, historical migrations, upgrade dispatcher, or legacy-database compatibility path; incompatible development changes use a fresh `sync.db`.

It may store canonical IDs, configured aliases, opaque remote message IDs, HMAC actor IDs, emoji reaction state, canonical poll option indexes, WhatsApp option hashes used only for boundary resolution, aggregate counts, narrowly required opaque provider poll references, timestamps and generic recovery cursors. A cursor contains only a safe stream key, ordered numeric position, event timestamp, and update timestamp. It must not store message content, poll question/option labels, or raw participant identity.

## 3. Configuration

Configuration is stored in SQLite (`sync.db`) and managed programmatically via Go packages and the REST API.

### `control.db`

The application-owned control plane opens `/data/control.db` independently of
the routing store. Its fresh schema contains the account/session/invite/audit
foundation, connection records (`transport_connections`), and the planned
verification pipeline/request, email challenge, and assessment records. This is
the only application database permitted to hold the minimum PII needed for
multi-user administration and membership verification, as well as encrypted
transport credentials. It uses foreign keys, explicit role/status checks, opaque IDs,
hashed bearer tokens, parameterized access APIs, and mode `0600` where the
platform permits. It has no routing tables and is never queried by the
canonical message router. The daemon closes it independently during shutdown;
the existing `sync.db` and per-connection `whatsapp-<connection-id>.db` boundaries remain unchanged.

#### Connections and Encrypted Credentials

`control.db` persists first-class transport connection records (`transport_connections`):
- `id`: safe identifier matching `[A-Za-z0-9][A-Za-z0-9_-]{0,63}`;
- `transport`: `whatsapp`, `discord`, or `telegram`;
- `label`: human-readable admin label;
- `enabled`: boolean toggle;
- `encrypted_credential`, `credential_nonce`, `credential_key_version`: encrypted bot token payload.

Credential encryption uses a domain-separated symmetric key derived from `IDENTITY_SECRET` using HMAC-SHA256 with the domain separator `"message-sync-credential-encryption-v1"`. Credential payloads (such as Discord and Telegram bot tokens) are encrypted using AES-256-GCM with unique 12-byte cryptographically secure random nonces per write. Plaintext credentials never touch SQLite storage or logs. Changing or losing `IDENTITY_SECRET` renders stored bot credentials permanently unreadable. WhatsApp connections enforce that no credential blob or nonce is present; WhatsApp session credentials remain strictly isolated in per-connection `whatsapp-<connection-id>.db`.

Verification pipelines and membership requests remain in this control-plane
boundary. Pipeline administration resolves the configured endpoint alias
against `sync.db` at mutation time but does not copy its remote target into
`control.db`. Public pipeline responses expose only a label and the fields
needed to render intake. Applicant/reviewer instructions, evidence requirements,
and bounded custom-field definitions are keyed by the endpoint's sync-set ID
in `control.db`; they are not copied into `sync.db`. Evidence is written with a random filename below the
dedicated private `/data/membership-evidence` path, mode `0600`, after bounded
MIME/size checks; request deletion removes the file and control record.
Work-email challenges are stored only as hashes with expiry and bounded
attempts. Optional Resend-compatible delivery reads deployment-only mail
credentials; a valid challenge advances `pending_email` to `pending`. Local
free-domain, LinkedIn-host, and evidence checks are advisory and never imply
employment or automatically approve a request.
When `OPENROUTER_API_KEY` is configured with the explicit
`OPENROUTER_ALLOW_TRAINING=false` privacy setting, evidence analysis runs in a
bounded asynchronous job. Only structured bounded results are stored in
`control.db`; raw prompts/responses are discarded. PDFs remain human-review
material, provider failures are unavailable/failed states, and no AI result
can advance or decide membership. The detected bounded evidence MIME is passed
to the existing analyzer boundary without storing the bytes in `control.db`.
Final human approve/reject decisions unlink local evidence immediately;
`needs_review` retains it for the reviewer.

Each membership request stores an immutable JSON snapshot of the sync-set form
definition and the validated answers in `control.db`. Subsequent edits to the
sync-set configuration therefore cannot reinterpret an existing application;
unknown fields, missing required fields, unsupported types, and overlong
answers are rejected at the public intake boundary.
Control-plane retention runs at startup and on the daily maintenance tick in
bounded batches. Expired or revoked sessions, consumed/expired invites and
reset tokens, expired email challenges, and terminal membership requests older
than 30 days are removed. Cleanup returns only opaque evidence references so
the application can unlink corresponding files under the dedicated evidence
directory; routing state in `sync.db` is never touched.

Approved membership fulfillment is implemented outside canonical routing. It
resolves endpoint aliases at action time, checks WhatsApp membership before
issuing an invite, assigns Discord roles through the existing bot session, and
records only safe fulfillment state/classes in `control.db`. WhatsApp never
uses server-side participant addition. The target group must require approval
for invite-link joins; otherwise fulfillment remains `action_pending` with an
administrator-action class. Pending join requests are read through the
WhatsApp administration boundary, matched only against the applicant's
submitted phone when the provider exposes a phone JID, and approved through
the provider request API. LID-only requests are not guessed or matched.
The authenticated connection-scoped readiness endpoint exposes only whether
the join-approval prerequisite is met and fixed failure classes.

The verification state machine is `pending_email` → `pending_admin` after a
valid work-email challenge, then `approved`, `rejected`, or `pending_admin`
(`needs_review`). Approved requests separately track fulfillment as
`not_started`, `succeeded`, `action_pending`, or `failed`; this control-plane
state never enters canonical routing.

### SQLite Configuration Tables

- `global_config`: Single-row table (`id = 1`) storing global behavior settings:
- `username_mode`: typed enum (`push_name` or `hash`, default `push_name`);
- `media_enabled`: boolean (default `1`);
- `media_max_size_mb`: integer (default `100`);
- `recovery_enabled`: boolean (default `1`);
- `recovery_max_age_hours`: integer (default `24`);
- `recovery_max_messages_per_group`: integer (default `200`);
- `storage_message_retention_days`: integer (default `90`);
- `poll_aggregation_trigger`: text (default `aggregate-response`);
- `whatsapp_chat_cleanup_enabled`: boolean (default `0`);
- `whatsapp_chat_retention_days`: integer (default `30`);
- `local_message_prefix`: bounded text prefix (empty disables source-local messages; never logged);
- `whatsapp_device_name`: bounded text identifier (default `message-sync`; customized device name shown in WhatsApp Linked Devices).
- `sync_sets`: Table of sync sets (`id TEXT PRIMARY KEY`).
- `endpoints`: Transport-aware endpoint configuration (`alias`, `transport`, `connection_id`, opaque `remote_id`, and `sync_set_id`). Supported transport values are `whatsapp`, `discord`, and `telegram`; all configured transports route through the shared adapter registry and canonical router. Every endpoint requires a valid `connection_id` referencing a connection whose transport matches the endpoint's transport. Global uniqueness on `(transport, remote_id)` is retained.

The alias is the safe endpoint ID. `remote_id` is a narrow operational addressing exception: for WhatsApp it is the configured group JID, for Discord it is the channel ID, and for Telegram it is the negative group/supergroup chat ID. Human-readable guild/channel/group/chat metadata, participant identifiers, credentials, and message content are never stored in this table or application logs.

Each validated configured endpoint must belong to exactly one sync set.

#### Connection → Endpoint → ChildScope Separation

The system maintains a strict 3-tier hierarchy:
1. **Connection (`control.db`)**: Owns transport authentication and client session lifecycle (e.g. Discord bot gateway, Telegram Bot API polling, WhatsApp protocol client). Stores encrypted credentials where applicable.
2. **Endpoint (`sync.db`)**: Belongs to an owning connection (`connection_id`) and maps a safe local alias to an operational top-level destination (`remote_id`, such as a Discord channel, WhatsApp group, or Telegram supergroup).
3. **ChildScope (`sync.db`)**: Endpoint-scoped sub-routing context (`canonical_scopes`), such as Discord threads or Telegram forum topics. Keyed strictly by `(canonical_id, endpoint_id)` without any connection identity.

`canonical_scopes` stores `(canonical_id, endpoint_id, scope_kind,
remote_scope_id, created_at)` with one row per canonical/endpoint pair.
`scope_kind` is a fixed `discord_thread` or `telegram_topic` enum; the remote
scope ID is opaque operational addressing. Optional friendly presentation labels
are stored separately in `child_scope_labels`, keyed by endpoint, scope kind, and
remote scope ID. The default opaque mode never writes labels; friendly labels are
normalized presentation metadata only and never affect routing or canonical
identity. The router prefers a live source label, then this catalog, then the
generic `thread`/`topic` label; it never parses the rendered header.

### REST API, Web UI & Authentication

The daemon provides an embedded Web UI console alongside the local HTTP REST server on port 8080 (configurable via `API_ADDR`):

- `GET /`: Serves the Single Page Application (SPA) administration console built with vanilla HTML/CSS/JS (embedded directly into the binary via `go:embed` without CDN or runtime filesystem dependencies).
- `GET /health`: Returns `{"status":"ok"}` with `200 OK` (public).
- `GET /api/auth/status`: Returns `{"isSetup": bool}` indicating whether the admin password has been initialized.
- `POST /api/auth/setup`: Accepts `{"username": "...", "password": "..."}` on first run, creates the first active `admin` in `control.db`, stores only its bcrypt hash, and issues a random server-side session token in an `HttpOnly` cookie. Fails if an account already exists.
- `POST /api/auth/login`: Verifies a username and password against an active `control.db` account and creates an independent hashed-token session. Authenticated requests resolve an opaque user ID, role, and safe username from the live session record; logout revokes only that session, and password changes revoke the user's other sessions.
- `POST /api/auth/logout` and `POST /api/auth/change-password`: Revoke the current session or change the authenticated user's password. Expired, revoked, unknown, or deactivated sessions receive `401` without waiting for token expiry.
- Account administration and connection mutations are centralized behind the `admin` role policy: `GET /api/users`, invite creation, activation changes, reset-token creation, `GET /api/audit`, `POST /api/connections`, `PUT/PATCH /api/connections/{id}`, `DELETE /api/connections/{id}`, and WhatsApp pairing/logout actions are admin-only. Operators may view safe connection status, run target discovery, and manage endpoints/sync sets, but cannot mutate credentials or accounts. Invite and reset tokens are one-time random bearer values stored only as hashes; audit rows contain fixed actions and opaque target IDs.
- `GET /api/connections`, `POST /api/connections`, `GET /api/connections/{id}`, `PUT/PATCH /api/connections/{id}`, `DELETE /api/connections/{id}`: Manage transport connection identities across WhatsApp, Discord, and Telegram. Connection DTOs contain opaque ID, transport, label, enabled state, and timestamps; bot tokens are accepted only on write, encrypted immediately with a domain-separated AES-256-GCM key derived from `IDENTITY_SECRET`, and never returned by read APIs or logs. Deleting a connection is rejected with `409 Conflict` while referenced by any endpoint. Fixed-field audit events track `connection_created`, `connection_enabled`, `connection_disabled`, `connection_credential_replaced`, and `connection_deleted`.
- `GET /api/connections/{id}/status`: Safe connection-scoped runtime health reporting readiness enums, connection state, and visibility guidance without exposing tokens or protocol secrets.
- `GET /api/connections/{id}/discovery`: Safe connection-scoped live target discovery (WhatsApp joined groups, Discord guild channels, Telegram observed chats). Target titles/names exist only transiently during discovery and disappear on process restart.
- `POST /api/connections/{id}/pair`, `DELETE /api/connections/{id}/pair`, `POST /api/connections/{id}/logout`: Scoped WhatsApp pairing lifecycle operations with global pairing serialization and fixed audit events (`whatsapp_pair_started`, `whatsapp_pair_cancelled`, `whatsapp_logged_out`).
- `GET /api/endpoints`, `POST /api/endpoints`, `GET /api/endpoints/{alias}`, `PUT /api/endpoints/{alias}`, `DELETE /api/endpoints/{alias}`: Manage transport-neutral endpoint configuration. Endpoint DTOs require `connectionId` matching an active connection of compatible transport. Reassignment of an endpoint to a new connection preserves alias, sync-set membership, canonical message copies, reactions, and native child scopes without rewriting canonical scopes.
- `GET /api/delivery/status`: Authenticated, read-only delivery health. It joins the router's in-memory lane snapshot with content-free SQLite ledger summaries and returns only endpoint aliases, transport labels/readiness enums, bounded queue capacity/depth, state counts, coarse oldest-active age, and safe failure classes. Terminal failed operations remain history and do not make an otherwise idle lane failed; the dashboard treats a ready transport with no immediate queue/retry work as healthy even when restart/replay history remains. It does not expose remote IDs, checkpoint values, message timestamps, content, identities, credentials, or raw errors. The public `/health` endpoint remains `{"status":"ok"}`.
- `GET /api/sync-sets`, `POST /api/sync-sets`, `GET /api/sync-sets/{id}`, `PUT /api/sync-sets/{id}`, `DELETE /api/sync-sets/{id}`: Manage sync set collections across all configured transports. The JSON member field is `endpoints`; every value is an endpoint alias and may identify a WhatsApp, Discord, or Telegram endpoint. Generic `/api/groups` CRUD routes do not exist.
- `GET /api/config`, `PUT /api/config`: Read and modify global configuration options with immediate reload notifications to the router.

Auth middleware protects all other `/api/*` endpoints, including both endpoint-management API shapes, returning `401 Unauthorized` if a valid Bearer token or session cookie is missing or invalid. Endpoint request bodies are never logged, and validation/error responses never echo a transport remote target. Non-API client paths (such as `/setup`, `/login`, `/dashboard`) fall back cleanly to `index.html` for client-side routing. Session tokens and plaintext passwords are never written to application logs.

The management and runtime routing models are transport-aware. Discord and Telegram endpoints can be configured and placed in mixed sync sets. The Discord gateway adapter and Telegram long-poll adapter start when endpoints for their respective transport exist or deployment-time token sources are configured, enabling discovery before the first endpoint is created. Telegram per-user reaction ingress requires the bot to be an administrator in the group or supergroup, as required by the Bot API, and the group must have anonymous reactions disabled; anonymous reaction-count updates are not mapped into the per-actor canonical reaction model. The application dispatches each destination alias through the adapter registered for that endpoint's configured transport. Outbound text/media plus reply/reaction/edit/delete lifecycle operations are implemented behind those adapter boundaries; the canonical router still addresses only endpoint aliases and remote message copies.

## 4. Canonical message model

Every source message receives an opaque application canonical ID unrelated to transport IDs.

```text
canonical c_...
   +-- c1g1 -> WhatsApp msg A (source copy)
   +-- c1g2 -> WhatsApp msg B
   +-- c1g3 -> WhatsApp msg C
```

`message_copies` maps each endpoint’s opaque remote message ID back to the canonical ID. This is the basis for replies, reactions, edits, deletes, deduplication and restart recovery.

Child conversation context is stored separately in `canonical_scopes`, keyed by
canonical message and configured endpoint alias. Each row contains only the
opaque provider child ID needed for native reply/lifecycle targeting; child
names and provider objects remain transient. Reply ingress copies all scopes
from the replied-to canonical message, with a live native scope on the source
endpoint taking precedence. This preserves independent Discord-thread and
Telegram-topic lineage without creating dynamic endpoints or sync-set members.

Uniqueness rules prevent two copies of the same canonical message in one endpoint and prevent one remote message from mapping to multiple canonicals.

## 5. Identity and attribution

Participant identity exists only transiently at ingress.

```text
raw participant JID --HMAC-SHA256(IDENTITY_SECRET)--> u_xxxxxxxxxx
```

Plain SHA is not used because phone/JID namespaces are brute-forceable. The HMAC secret remains outside JSON and must stay stable across restarts.

Attribution modes:

- `push_name`: `<alias>/<transient push name>` with HMAC fallback;
- `hash`: always `<alias>/u_xxxxxxxxxx`.

Push names are never persisted.

## 6. Ingress and router event flow

### Dynamic Connection Runtime, Shared Ingress, and Adapter Dispatch

The application runtime uses a dynamic `ConnectionManager` (`internal/connection`) that owns active connection adapter instances by opaque connection ID. Outbound operations from the canonical router remain strictly endpoint-addressed; `AdapterRegistry` resolves each configured endpoint alias to its owning connection's adapter:

```text
endpoint alias -> connection id -> adapter instance
```

The canonical router remains completely unaware of connection IDs. If an endpoint's connection is stopped or unavailable, outbound calls safely return a classified transient failure without leaking provider details.

All runtime and API transport operations use this explicit connection boundary. There is no singleton transport, implicit `conn-wa-1` ownership, empty connection-ID fallback, primary-transport selection, or unscoped transport recovery namespace. Endpoints with invalid or missing ownership are rejected by configuration validation and are never silently attached to another connection.

When an encrypted Discord or Telegram credential changes, the application compares an in-memory fingerprint of the ciphertext and nonce, opens the replacement adapter with the decrypted credential, and atomically restarts only that connection in `ConnectionManager`. Plaintext credentials are not retained for change detection; unrelated connections continue running.

Each active connection adapter emits normalized `transport.Incoming` events on its `Events()` channel. `ConnectionManager` forwards each connection's events into a unified shared ingress channel feeding the recovery coordinator and single ordered router worker. Event forwarding for each connection is isolated:
- when an adapter stream closes or a connection is stopped, its forwarder terminates without interrupting other connections or closing the shared ingress stream;
- connections support a safe register, start, stop, and atomic replace/restart lifecycle;
- recovery-capable connections dynamically register with `recovery.Coordinator` upon startup, run bounded recovery, and monitor reconnect signals until stopped;
- `transport.ChildScope` (such as Discord threads or Telegram topics) passes through outbound dispatch and shared ingress without modification.

### WhatsApp ingress boundary

The WhatsApp ingress boundary normalizes incoming events before fan-out:

```text
whatsmeow callback
   -> reject DM or unconfigured group
   -> map configured group JID to safe alias
   -> HMAC participant JID transiently
   -> normalize message into transport.Incoming
   -> application receives alias + event kind
```

The normalized event may temporarily carry message text/caption and push-name data because the router needs them for immediate forwarding, but those fields are explicitly transient and must never be persisted or logged. DMs and unconfigured groups are discarded before an internal event is produced.

Live messages and protocol HistorySync snapshots carry the same per-endpoint checkpoint stream, using the provider message timestamp as the ordered position. HistorySync snapshots are parsed in memory, filtered by the configured age/count bounds, sorted oldest-first (with the protocol order as a tie-breaker), and serialized with live WhatsApp ingress. They then enter the ordinary recovery coordinator and router, so a replay repairs only missing copies and advances the cursor only after payload-dependent work is safe to forget. WhatsApp does not provide a durable application-level sequence for every live message; messages sharing a timestamp remain idempotent by remote-copy ID.

WhatsApp accounts boot dynamically through connection management without blocking and expose pairing lifecycle via connection-scoped REST APIs (`/api/connections/{id}/status`, `/api/connections/{id}/pair`). Terminal QR rendering is disabled. The companion device registration identifies as `message-sync` by default (configurable via `WHATSAPP_DEVICE_NAME`). When pairing is initiated, whatsmeow generates QR codes on a managed channel, refreshing expired codes dynamically. Pairing operations across accounts are serialized to one flow at a time. Upon successful scanning, whatsmeow automatically persists linked-device state in `/data/whatsapp-<connection-id>.db` and the client transitions to connected. On restart, stored device sessions reconnect directly without re-pairing.

WhatsApp bridge-generated edit, revoke/delete, and reaction sends install a bounded in-memory lifecycle marker before the provider call. Matching `FromSelf` lifecycle ingress consumes one marker and stops at the WhatsApp adapter boundary; unmatched `FromSelf` mutations remain eligible for routing because linked-device user actions also use `FromSelf`. The fallback marker uses only endpoint, target remote message ID, operation kind, and reaction emoji, expires promptly, and is not persisted. Since WhatsApp does not expose a separate mutation event ID for every lifecycle echo, an ambiguous provider result is retained until expiry and is not represented as exactly-once delivery.

The router processes ingress events via an ordered worker:

```text
normalized event
   -> ingress queue
   -> resolve configured endpoint/sync set
   -> deduplicate
   -> create/resolve canonical ID
   -> fan out destinations sequentially
   -> persist each successful copy immediately
```

Sequential fan-out is deliberate for MVP. It makes crash semantics and SQLite state easy to reason about. Concurrency should be added only if measurement proves it necessary.

Before canonical resolution, the router checks the configured global local-only
prefix. An exact case-sensitive match on an eligible new message records only
the source alias, opaque remote message ID, and timestamp in
`suppressed_local_messages`, then stops processing. This prevents fan-out,
canonical persistence, and media loading; later lifecycle events remain local.

### Discord gateway ingress boundary

The Discord adapter uses a gateway bot for ingress and keeps Discord protocol identity outside the canonical model. Discord connections are multi-instance safe: each connection adapter receives its decrypted token explicitly via `discord.Options.Token`, owns only the endpoint channels assigned to its connection ID, and manages its own gateway session, discovery, status, webhooks, and recovery streams:

```text
Discord MESSAGE_CREATE
   -> reject DM, unsupported format, or unconfigured parent
   -> reject bridge bot and bridge-managed webhook copies
   -> resolve a thread/post channel to its configured parent using live gateway state
   -> map configured parent channel ID to safe endpoint alias
   -> HMAC "discord:" + transient author ID
   -> replace Discord mention IDs with privacy-safe text
   -> normalize supported content into transport.Incoming
```

Only **Guild Messages**, **Guild Message Reactions**, and **Message Content** gateway intents are requested; direct-message intents are not requested, and DMs are also rejected defensively by the normalizer. Discord user IDs, display names, guild/parent-channel names, message bodies, and raw gateway events are never persisted or logged; an explicitly enabled friendly mode may persist only bounded child-thread labels in `child_scope_labels`. User mentions are converted at ingress to transient display text, with an HMAC-derived actor fallback when no display name is available; role and channel mention IDs become generic `@role` / `#channel` text. Structured Discord member IDs therefore do not cross the adapter privacy boundary.

The gateway bot is intentionally distinct from outbound Discord sender rendering. The bot owns gateway ingress, discovery, reactions, and gateway lifecycle; each configured Discord destination reuses one bridge-managed incoming webhook for outbound sender rendering. Only the webhook manager's in-memory credential map knows the webhook ID/token. The router passes transient sender metadata separately from the transport-neutral attributed text, letting the Discord adapter render the sender's transient display name (from WhatsApp or Telegram) as the webhook APP username (or the HMAC actor ID when no display name is available) without persisting either display name or message content. In friendly presentation mode, the webhook APP username carries the complete transient source/sender label, while Discord removes that wrapper from the body; child-context presentation remains in the label. These APP/webhook identities are presentation overrides, not real Discord user accounts, and no per-user webhook is created. A `ManagedWebhookChecker` suppresses bridge webhook message-create loops, while bridge-bot reaction events and bridge-initiated delete echoes are filtered at the Discord boundary. The application feeds the Discord adapter's event stream through the ConnectionManager's shared ingress path into the same ordered router worker; no second canonical worker or platform-specific canonical-ID path is introduced.

Discord attachments are downloaded only when routing needs them, bounded by the configured media limit, held in memory, and re-uploaded with bridge-generated safe filenames. Source filenames, CDN URLs, and media bytes are never stored in `sync.db`. Discord incoming-webhook execution does not accept `message_reference`; when a destination copy exists the adapter adds a transient clickable message link labelled with the source endpoint and first line of the quoted message to the single sender-attributed webhook message. If transient channel metadata or the destination copy is unavailable, an alias-based textual reply fallback is used instead.

Discord recovery reads at most the coordinator's bounded history window from each configured channel after that channel alias's accepted snowflake cursor. Discord's newest-first REST response is reversed in memory before existing create/edit normalization and the common recovery coordinator path process it. Only message create/recovery items advance the snowflake cursor; live edit, reaction, and delete events reuse a target message ID rather than carrying a distinct monotonic event ID, so they use canonical copy lookup and mutation idempotency without a recovery checkpoint. `READY` and `RESUMED` signal the same single-flight recovery operation used at startup. Per-alias history readiness is exposed only as safe enums (`unknown`, `ready`, `missing_permission`, or `unavailable`); message history payloads, attachment bytes, and provider errors are never persisted or logged. Discord history is not a complete offline event log, so deletes and reaction transitions that occur entirely while disconnected may remain unreconstructed.

#### Discord threads, forums, mentions, polls, and unsupported formats

Thread behavior deliberately flattens rather than creating dynamic routing identities. A gateway message whose channel is a Discord news/public/private thread is resolved through DiscordGo's live channel state. If that thread's `ParentID` is an already-configured Discord endpoint, the event is normalized under the **parent endpoint alias**. Replies, reactions, edits, and deletes use the same rule. Thread IDs and forum-post IDs are never inserted into `endpoints`, never become canonical IDs, and are retained only when Discord itself supplies a remote message-copy ID required by lifecycle mapping. If the parent cannot be resolved or is not configured, the thread event is ignored.

Discord forum posts are thread channels, so ingress follows the same parent-flattening rule when a forum parent has already been configured. The bridge does not dynamically create forum posts or thread-specific endpoint records, and the admin discovery UI intentionally continues to expose only sendable text/announcement channels. Outbound traffic addressed to an alias always targets that alias's configured parent channel; it does not attempt to return content to the originating thread. Automatic outbound forum-post creation remains outside this mapping.

Representable polls sent to Discord use native Discord poll messages through the session REST client because DiscordGo webhook parameters do not expose a poll field; ordinary text/media remains on the bridge-managed webhook. Friendly native polls carry the router-produced transient attribution in the REST message content while keeping the poll question/options structured; opaque mode leaves that content empty. Native Discord polls are normalized with transient question/options, and guild poll vote add/remove events fetch the poll message to reconstruct the answer-ID-to-option-index mapping. Discord answer IDs and participant IDs remain transient; actor selections cross the boundary only as HMAC actor IDs plus canonical indexes. Discord constraints (1–10 answers, question/answer lengths, 1–168 hour duration, and boolean single/multiselect semantics) use the deterministic text fallback when not representable. Poll answer mappings are rebuilt from the remote poll after restart; offline vote transitions that Discord cannot replay remain unavailable rather than gaining durable identity storage.

WhatsApp stickers sent to Discord use the existing transient-media path and are uploaded with the bridge-generated filename `sticker.webp` and `image/webp` content type. Native Discord sticker-only messages, embed/component-only messages, Discord system messages, and other unsupported message types are ignored deterministically rather than producing empty or ambiguous canonical messages. A normal text/caption plus a supported attachment continues through the standard transient-media path.

For bridged mentions toward Discord, the adapter replaces known transient remote mention tokens with a display label; if the supplied label is missing or is itself the raw remote identity, the output uses `@participant`. Discord webhook allowed-mention parsing remains disabled, so fallback text cannot unexpectedly ping Discord identities.

### Discord admin discovery and webhook readiness boundary

Discord administration reuses the transport adapter rather than introducing a second protocol client. An authenticated admin request may ask the live adapter for guild text/announcement channels; guild/channel display names are returned directly to the requesting UI and are never inserted into configuration, canonical-message tables, logs, or caches outside process/browser memory. The admin chooses a safe endpoint alias and optional sync set, then the existing transport-neutral `/api/endpoints` mutation persists only the alias, `transport=discord`, and selected channel ID.

Managed-webhook preparation tracks only a safe readiness enum per configured channel in addition to the in-memory webhook credential. A Discord REST 403 while listing/creating the bridge webhook is classified as `missing_permission`, allowing the gateway and inbound discovery to remain live while outbound webhook readiness is visibly degraded. If an execute/edit/delete operation proves its cached credential invalid, the manager invalidates only that channel, serializes list-or-create repair per channel, and retries that operation once. Unknown target messages remain idempotent for edit/delete when the credential is valid; an unknown webhook is repaired instead. The API maps readiness back to configured endpoint aliases; it never returns webhook IDs, tokens, URLs, raw Discord errors, or transient guild/channel names in the status response.

The embedded Web UI provides a connection-centric management interface (`Connections → discovered parent conversations → Endpoints → Sync Sets`). Discord and Telegram bot credentials use paste-once modal forms with password inputs; tokens are encrypted immediately into `control.db` using AES-256-GCM domain keys derived from `IDENTITY_SECRET` and are never redisplayed, stored in browser storage, or logged. WhatsApp accounts are linked via QR modals scoped to each connection. Child scopes (Discord threads/posts and Telegram topics) flatten through their parent endpoint and are not configurable endpoint rows. Operators receive read-only status and discovery views, while administrative mutating actions (add/delete connection, enable/disable, replace token, pair, logout) are guarded by server-side RBAC and role-aware UI controls. DiscordGo performs REST rate-limit retry/backoff for gateway REST, discovery, webhook, reply, reaction, edit, and delete operations. Gateway `READY`, `RESUMED`, and `DISCONNECT` events update only the adapter's in-memory connected flag with fixed safe log fields; raw lifecycle event data is never logged. On reconnect the in-memory webhook credential remains usable, while a process restart runs managed-webhook discovery again and reuses the existing bridge-owned channel webhook instead of creating one per participant or restart.

### Telegram Bot API ingress boundary

The Telegram Bot API adapter lives behind the same `transport.Adapter` boundary and uses long polling:

```text
Telegram Update
   -> reject non-message / unsupported update types
   -> map group/supergroup chat ID to safe endpoint alias
   -> HMAC "telegram:" + transient user ID
   -> replace text_mention and username entities with safe fallbacks
   -> normalize supported content into transport.Incoming
```

It maps only configured group/supergroup chat IDs to endpoint aliases, HMAC-normalizes `telegram:<user_id>` immediately, keeps display names/text/captions transient, and rejects private chats and bridge-bot echoes from canonical routing. Every accepted Bot API `update_id`, including deliberately ignored or unconfigured updates, crosses the adapter boundary only as a content-free checkpoint/no-op or a normalized event; the coordinator persists the highest contiguous accepted update ID. The first long-poll request starts at that persisted ID plus one, while the Bot API client's in-process offset continues moving to prevent continuous duplicate delivery. `text_mention` user objects are reduced to HMAC actor IDs plus transient display labels before crossing the adapter boundary, while username-only mention entities are rewritten to the generic `@mention` fallback so raw Telegram usernames are not carried into canonical content. When Telegram is the destination of a cross-endpoint message, its transient sender label is prefixed with the source endpoint alias (for example, `wa/Alice: hello`); this visible attribution has no routing authority.

The application registers Telegram in the existing transport adapter registry and consumes its event channel in the same single canonical router loop as WhatsApp and Discord. Runtime config reload updates the registry and Telegram alias-to-chat mapping without changing canonical/message-copy state. Telegram message IDs remain opaque remote-copy IDs only; Telegram outbound text/media and reply/reaction/edit/delete lifecycle operations use the same canonical/message-copy model.

#### Telegram forum topics, group migration, mentions, polls, and media handling

Telegram forum topics deliberately flatten into the configured parent group/supergroup alias while preserving a message-level `telegram_topic` scope. `message_thread_id` and forum-topic names stay out of endpoint configuration; only the opaque topic ID is retained in canonical scope lineage. Ordinary cross-platform messages target the configured parent/general context, while mapped scoped replies and lifecycle operations set the explicit Bot API `MessageThreadID` when returning to a topic. In opt-in friendly display mode, supported topic create/edit service messages update the separate `child_scope_labels` presentation catalog; service messages themselves are never canonicalized or broadcast. No permanent routing topic registry exists.

A basic-group to supergroup migration is handled as addressing maintenance: the adapter recognizes Telegram's migration service message, transactionally changes only the matching Telegram endpoint `remote_id`, preserves alias and `sync_set_id`, then replaces its in-memory chat mapping so subsequent messages from the supergroup continue routing under the same alias. Migration logs never include old/new chat IDs.

Representable Telegram polls use Bot API `sendPoll` with transient question/options and canonical single-/multiple-answer semantics; friendly native polls carry the router-produced transient attribution in the native poll description while preserving the exact question/options, and opaque mode leaves that description empty. Unsupported quiz/media/limit cases use the existing deterministic text fallback without truncation. The returned message ID remains an ordinary copy and the returned opaque `poll_id` is stored only in `poll_provider_refs`. Bot-created `poll` updates resolve that reference through the store and replace the Telegram endpoint's absolute option-count snapshot, without storing voter identities. The Bot API does not provide complete ongoing result updates for arbitrary human-created source polls that were not sent by the bot; those polls may be mirrored, but their source contribution is explicitly marked partial/unavailable in live results and no MTProto session is used.

Media formats reuse canonical kinds where practical: sticker → `sticker`, voice note → `audio`, video note → `video`, MP4 animation → `video`, and non-MP4 animation → generic `document`. Hosted Bot API media handling enforces 10 MiB for photos, 50 MiB for general uploads, and Telegram sticker format caps (512 KiB static WebP, 64 KiB TGS, 256 KiB WebM), further bounded by the configured media limit. Contacts, locations/venues, payments/games, membership/title/photo/pin/forum service messages, and otherwise unsupported service-only payloads do not create canonical content; configured-chat ignores may log only fixed event classes and safe endpoint aliases.

### Telegram admin discovery and privacy readiness boundary

Telegram administration is also adapter-owned rather than router-owned. Because the Bot API cannot enumerate every group a bot belongs to, the adapter observes group/supergroup chats from live long-poll updates before configured-endpoint filtering and keeps only a bounded process-memory cache of safe selection metadata: opaque chat ID plus transient title/username/type. Private chats never enter this cache.

The authenticated admin API exposes safe long-poll/endpoint-readiness state at `/api/connections/{id}/status` and the current transient observation cache at `/api/connections/{id}/discovery`; it never returns bot credentials or raw Bot API objects. Selecting a chat still creates an ordinary transport-neutral endpoint through `/api/endpoints`, so only the alias, `transport=telegram`, opaque chat ID, and optional sync-set membership enter `sync.db`. Chat titles/usernames are neither logged nor persisted and disappear on restart.

On authenticated status requests the adapter derives current Bot Privacy Mode readiness from the safe `getMe.can_read_all_group_messages` capability, retains only the resulting boolean, and discards the returned bot user object. If that probe fails, the failure is safe-logged, readiness is marked unknown, and status/UI falls back to fixed operator guidance.

Transport send, edit, delete, and reaction boundaries return the shared `transport.Failure` contract. It exposes only one of `transient`, `rate_limited`, `permission_denied`, `destination_missing`, `payload_rejected`, or `unsupported`, plus retryability and an optional retry-after duration. The original provider error remains available only through in-process `errors.Is`/`errors.As`; its text is never serialized or logged. Unknown failures default to retryable `transient`.

## 7. Idempotency and crash recovery

Persist each successful destination copy immediately.

Example:

```text
source A
 -> c1g2/B succeeds -> persist B
 -> process crashes before c1g3
```

On replay after restart, the router sees that c1g2 already has a copy and sends only c1g3. The in-memory sent-ID cache is only an optimization; SQLite mapping is authoritative across restarts.

The content-free `delivery_operations` table tracks current payload-dependent work by canonical ID, destination alias, operation kind, and revision. It stores only state, retry timing/counts, safe failure classes, and timestamps; the payload remains in memory. Queued and retrying rows become `awaiting_replay` during startup because their payloads cannot survive a process restart. Successful work is represented by `message_copies` or the resulting mutation and its ledger row is deleted, so the table is not an audit history. Per-endpoint summaries expose only state counts and the oldest active age.

The authenticated `/api/delivery/status` endpoint joins those SQLite summaries with the router's point-in-time lane snapshot for the embedded dashboard. It reports explicit healthy, queued, retrying, awaiting-replay, failed, and stopped states, plus safe transport readiness enums. The dashboard refreshes the read-only view on its existing short polling cadence; it has no retry, cancel, inspection, or telemetry controls.

The recovery coordinator is the single accepted-event checkpoint boundary. A normalized incoming event may carry only a stream key, ordered position, and timestamp. The coordinator serializes each stream, invokes the ordinary router handler, and advances the durable cursor only after every payload-dependent operation is complete, deliberately irrelevant, or an intentional terminal failure; recording delivery intent alone is not safe. Pending and failed positions block later acknowledgements until replayed; duplicate positions at or below the cursor are accepted no-ops. Optional adapter recovery sources receive fixed in-code count/age bounds and emit normalized events back through this same callback. Startup and reconnect recovery are single-flight and do not create a second delivery path or a periodic poller.

Create delivery uses an explicit certainty boundary. Provider adapter API failures are treated as ambiguous unless the adapter knows the request was rejected before acceptance; ambiguous attempts are recorded as awaiting replay and are never blindly retried. Successful create sub-steps are recorded in a content-free create-step ledger before the message copy is finalized, so a replay can finish local persistence without sending the provider create again. WhatsApp, Discord, and Telegram do not provide a shared client-assigned create identity that makes remote creates exactly-once, so a provider may still require operator reconciliation after an ambiguous result; message-copy uniqueness remains the authoritative completed-copy record.

Normal create fan-out uses one bounded in-memory FIFO lane per configured endpoint. The router performs canonicalization, loop prevention, reply resolution, and outgoing construction before enqueueing independent destination jobs; the lane only invokes the transport boundary and records the resulting copy. A slow lane therefore cannot hold up a healthy destination, while each destination remains ordered. Lane membership follows configuration reloads, and a full lane leaves its content-free operation row awaiting replay rather than dropping the event.

Lane create and lifecycle jobs retry only safe transient and rate-limit failures. Provider retry-after values take precedence over the fixed bounded exponential schedule; permanent failures become `failed`, while exhaustion or cancellation becomes `awaiting_replay`. Each attempt updates only ledger metadata. Lifecycle mutations are held in process when a destination copy is missing, coalesced by canonical/destination identity, and flushed behind the successful create; deletes tombstone first and cancel pending dependent work.

## 8. Text and media

Supported MVP message classes:

- text and captions;
- images;
- video;
- documents;
- audio/voice notes;
- stickers;
- native WhatsApp, Discord, and Telegram polls where representable.

Media flow:

```text
WhatsApp encrypted media
 -> whatsmeow download/decrypt
 -> bounded memory/temporary stream
 -> upload/send to each destination
 -> release/discard
```

No media database, object store, media cache or archival directory is part of MVP. `media.maxSizeMB` protects memory usage.

Audio/stickers cannot carry normal captions, so attribution may be sent as a small companion text message.

## 9. Replies

For an incoming reply, resolve the quoted remote message ID through `message_copies`. For each destination, look up the corresponding destination copy and create a native quote when enough transient/protocol metadata is available.

Because raw participant JIDs/message bodies are intentionally not stored, native quote reconstruction may sometimes be impossible after restart. The fallback is textual provenance, e.g. `↳ c1g1/u_abcd1234` rather than weakening privacy.

## 10. Reactions

Reaction state uses:

- canonical ID;
- source endpoint alias;
- HMAC actor ID;
- emoji.

This permits add/change/remove semantics without raw identity. A native reaction on a destination is necessarily made by the bridge WhatsApp account; origin attribution may require companion text if product behavior requires visible original actor identity.

## 11. Polls and vote aggregation

Polls cross the canonical router using zero-based provider-neutral option indexes. Question and option text is retained only transiently at the transport/router boundary for native sends, deterministic fallbacks, and summaries; it is never persisted in `sync.db` or application logs. Provider mappings stay in adapters: WhatsApp option hashes, Discord answer IDs, and Telegram option order resolve to canonical indexes before state reaches the store.

Each poll endpoint has exactly one authoritative contribution model. WhatsApp and Discord delta events use HMAC-derived actor selections (add/change/remove), while Telegram bot-created poll updates use absolute option-count snapshots. Opaque provider references such as Telegram `poll_id` are persisted only when needed to correlate later lifecycle updates. Raw voter identity, provider update objects, and poll content do not cross into durable application state.

For every supported poll, the router creates at most one bridge-owned editable live-result companion per poll endpoint, including the source endpoint. Companions show aggregate option counts only; they never show voter names, HMAC IDs, per-voter choices, or imply that multi-select counts equal unique voters. The companion rows contain only lifecycle metadata, and edits and deletion use the existing per-endpoint delivery lanes, idempotency ledger, and mutation revisions. If a source contribution cannot be observed, the companion marks it partial/unavailable and excludes it from totals rather than fabricating zero.

Replying `aggregate-response` to any poll copy remains an exact reply-trigger feature:
- `poll_aggregation_trigger` configures the trigger (default `aggregate-response`);
- the router suppresses the trigger message instead of fanning it out;
- the router uses the same authoritative canonical aggregate counts as live results;
- a manual aggregate summary is sent to the sync set and quotes each local poll copy when available, falling back to `Option N` labels after restart.

The fresh-development schema is changed directly for this design. No production migration framework, compatibility path, or backfill is introduced.

## 12. Edits and deletes

Edits and revokes resolve the target through canonical mapping and apply to all known copies using whatsmeow helpers/protocol APIs.

Content is never persisted merely for edit idempotency. A content hash may be considered later only if a demonstrated replay problem requires it and if the hash does not create a privacy leak; MVP should prefer protocol event IDs/tombstone state.

Deletes mark a canonical message tombstoned before/while propagation so offline recovery cannot resurrect it.

## 13. Offline recovery

Recovery is bounded and best effort. WhatsApp uses only the existing whatsmeow protocol HistorySync event; it does not scrape arbitrary chat history or query per-connection `whatsapp-<connection-id>.db` for application data. HistorySync cannot reliably reconstruct offline delete or reaction transitions, so the adapter does not guess those mutations. Provider availability and completeness remain controlled by WhatsApp's protocol history behavior.

Config bounds:

- maximum age;
- maximum messages per group.

Recovered events enter the same normalization/router path as live events. There is no separate recovery forwarding implementation.

## 14. Retention

`sync.db` mapping retention defaults to 90 days. Cleanup is batched. After expiry, very old reply/reaction/edit/delete events may fall back or no longer propagate.

WhatsApp chat history on the sync account can optionally be cleared on a daily schedule via WhatsApp AppState `ClearChatAction` patches (`whatsapp_chat_cleanup_enabled`, `whatsapp_chat_retention_days`). This clears old messages on the sync account only for groups configured in sync-sets without modifying `sync.db` mappings or deleting messages for other group participants.

Per-connection `whatsapp-<connection-id>.db` retention is controlled by whatsmeow/protocol requirements and monitored separately; it is not an application history store.

## 15. Transport abstraction and adapter registry

The core transport interface uses endpoint IDs and remote message IDs, not platform-specific canonical keys. WhatsApp groups, configured Discord channels, and configured Telegram groups/supergroups all support end-to-end text/media routing and the shared reply/reaction/edit/delete lifecycle through the same application loop and canonical copy model.

Outbound routing uses a small adapter registry (`internal/router/registry.go`) keyed by configured transport type. The registry maintains only the safe endpoint-alias → transport mapping; the canonical router still emits operations addressed by alias and never switches on Discord, Telegram, or WhatsApp remote message IDs. Config reload updates the alias mapping without changing canonical/message-copy state.

The persisted configuration is transport-aware so Discord and Telegram endpoint records can participate in alias-based sync-set configuration without changing canonical identity. The endpoint schema accepts `transport=whatsapp|discord|telegram`; Telegram stores only the opaque negative Bot API group/supergroup chat ID required for operational addressing. Human-readable platform metadata and credentials are not part of the endpoint schema.

```text
configured sync set
  WA:c1g1
  Discord:d1
  Telegram:t1
        |
        v
canonical alias-based routing model
```

All transport adapters feed normalized `transport.Incoming` events into the same single ordered router worker. Outbound fan-out dispatches each copy through the adapter registered for that destination's transport type, recording remote message IDs in `message_copies` for bidirectional reply/reaction/edit/delete lifecycle synchronization.

## 16. Rootless container model

Runtime requirements:

- non-root UID/GID 1000;
- no added capabilities;
- `no-new-privileges`;
- read-only root filesystem via Compose;
- `/data` as the only persistent writable path;
- port 8080 exposed for local/admin REST API;
- no host networking or privileged container.

`Containerfile`, `.containerignore` and `compose.yml` intentionally avoid Docker-specific naming.

## 17. Versioning and releases

`internal/version.Build` defaults to `development` for local dev builds.
The image workflow listens only for `VERSION` changes on `main`, which prevents development commits from repeatedly attempting Docker Hub/GHCR publication.


## 18. Security/logging

Never log raw whatsmeow events or arbitrary errors containing protocol structs. Use explicit safe fields such as:

```text
event=message_received endpoint=c1g1 canonical=c_... kind=text
event=fanout_failed source=c1g1 target=c1g2 error_class=timeout
```

Avoid sender JIDs, group JIDs, names, content, captions and filenames.

The WhatsApp adapter disables whatsmeow/sqlstore logging entirely. It emits only fixed connection/pairing state, configured endpoint aliases, normalized kinds, and safe error classifications through the application logger. Pairing QR output is a separate sensitive terminal UI and must not be copied into retained logs or support artifacts.

The Discord adapter replaces DiscordGo's default logger with a fixed-field warning/error classifier because raw gateway/client errors may contain protocol identifiers or other sensitive values. Application-visible Discord logs use only fixed event names, safe endpoint aliases, message kinds/counts, and safe error classes. Bot tokens are injected dynamically from encrypted control-plane credentials; managed webhook IDs/tokens are discovered or created at runtime and kept only in memory. None of those credentials are copied to `sync.db` or application logs. DiscordGo retry-on-rate-limit behavior is enabled for gateway REST operations and explicitly requested for webhook, reply, reaction, edit, and delete calls.

The Telegram adapter receives its bot credential explicitly via `telegram.Options` from encrypted control-plane credentials rather than singleton environment variables; tokens are never written to `sync.db` or application logs. Multiple Telegram bot connections run concurrently under the shared `ConnectionManager`. Each bot connection tracks an independent recovery cursor (`telegram:<connection-id>`) and namespaced poll provider references (`telegram:<connection-id>`). Endpoint ownership and group migrations are scoped strictly to the owning connection ID. The Bot API client's default raw update/error logging is not enabled: application-visible client errors pass through the fixed safe-log classifier, and ingress buffer/lifecycle logs contain only fixed event names, safe endpoint aliases, normalized kinds, and reasons. Each long-poll client starts at its connection-scoped cursor plus one, advances its Bot API `getUpdates` offset in process, and applies bounded retry/backoff (including Telegram `retry_after` responses). The adapter rejects duplicate/older update IDs within the running process before normalization; Telegram's retained update window limits how far a restart can recover.

## 19. Scope boundaries

Telegram endpoint creation and reassignment call the owning running bot's `getChat` boundary and accept only `group` or `supergroup`; private chats and broadcast channels are rejected, and unavailable validation fails closed.

`README.md` is authoritative for supported product behavior. This architecture document records implemented components, invariants, and known provider constraints rather than maintaining a feature backlog. New scope must be justified independently and documented here only after it changes an implemented architecture or invariant.

Membership-specific email challenges and advisory evidence analysis are implemented only within the isolated control plane described above.

## 20. Reliability verification

The integration harnesses in `internal/integration` connect fake WhatsApp, Discord, and Telegram adapters to the real alias registry, SQLite store, canonical router, and destination lanes. They verify:
- All-to-all fan-out, slow-destination isolation, transient retry, and ordered create/edit/reaction/delete delivery across multiple concurrent connection instances per transport.
- Cross-connection thread and topic reply lineage preserving native child scopes (`discord_thread`, `telegram_topic`) across independent connection instances.
- Delivery lanes failure isolation where stopped or failing connections do not block healthy lanes, and endpoint reassignment retries delivery on the newly owning connection without duplicating canonical copies or creating a second endpoint identity.
- Local-only message prefix suppression across multiple connections and child scopes.
- Recovery cursors and poll provider references strictly namespaced per connection ID (`telegram:<connection-id>`) to prevent cross-connection cursor stomping or poll reference collisions.
- Membership verification fulfillment resolving pipeline endpoint aliases dynamically to the active adapter on the owning connection via `ConnectionManager`, cleanly handling reassignment and offline connection states without leaking PII.
- Privacy canaries asserting zero plaintext credentials, phone numbers, or participant names in `sync.db`, `control.db` searchable fields, application logs, or audit records.
- Resource lifecycle verification asserting no goroutine, client, or handle leaks across rapid connection start/stop cycles.

Runtime and container smoke tests use a fresh disposable `/data` volume; no deployed database is upgraded.
