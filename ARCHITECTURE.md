# Architecture

## 1. Goals

`message-sync` is a small privacy-first message synchronization daemon. MVP priorities are:

1. no PII/PHI in application persistence or application logs;
2. deterministic, restart-safe synchronization;
3. simple rootless self-hosting;
4. transport adapters for WhatsApp (`tulir/whatsmeow`), Discord (gateway/webhooks), and Telegram (Bot API long polling);
5. transport-neutral canonical IDs so transport adapters do not become canonical identity.

The previous Node/Baileys project is a behavioral reference only, not the architecture baseline.

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
                         +--------------------> /data/whatsapp.db
                                               sensitive protocol state
```

### `whatsapp.db`

Owned by whatsmeow. It stores linked-device credentials, Signal/session keys, app-state/protocol data and may contain WhatsApp identifiers/contact metadata. It is sensitive and is the only protocol-state exception to the application’s zero-PII persistence rule.

Requirements:

- `EnableDecryptedEventBuffer = false`;
- `UseRetryMessageStore = false`;
- never expose/query it for product features;
- never copy contact/LID data into `sync.db`;
- protect it with restrictive filesystem permissions and encrypted storage where appropriate.

The daemon opens the database with the CGO-free SQLite driver, enables SQLite foreign keys, wraps the connection with whatsmeow `sqlstore`, and restricts the database file to mode `0600`. Both the whatsmeow client logger and sqlstore logger are no-op so protocol structs, identifiers, payloads, and arbitrary protocol errors cannot bypass the application safe-log boundary.

### `sync.db`

Owned by message-sync and designed to remain PII/PHI-free. Initial schema is in `internal/store/schema.sql`.

The application initializes this schema only for a fresh database. The product is pre-release, so the store has no schema-version table, historical migrations, upgrade dispatcher, or legacy-database compatibility path; incompatible development changes use a fresh `sync.db`.

It may store canonical IDs, configured aliases, opaque remote message IDs, HMAC actor IDs, emoji reaction state, option SHA-256 hashes for polls, timestamps and generic recovery cursors. A cursor contains only a safe stream key, ordered numeric position, event timestamp, and update timestamp. It must not store message content, poll question/option labels, or raw participant identity.

## 3. Configuration

Configuration is stored in SQLite (`sync.db`) and managed programmatically via Go packages and the REST API.

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
- `whatsapp_chat_retention_days`: integer (default `30`).
- `sync_sets`: Table of sync sets (`id TEXT PRIMARY KEY`).
- `endpoints`: Transport-aware endpoint configuration (`alias`, `transport`, opaque `remote_id`, and `sync_set_id`). Supported transport values are `whatsapp`, `discord`, and `telegram`; all configured transports route through the shared adapter registry and canonical router.

The alias is the safe endpoint ID. `remote_id` is a narrow operational addressing exception: for WhatsApp it is the configured group JID, for Discord it is the channel ID, and for Telegram it is the negative group/supergroup chat ID. Human-readable guild/channel/group/chat metadata, participant identifiers, credentials, and message content are never stored in this table or application logs.

Each validated configured endpoint must belong to exactly one sync set. Arbitrary routing graphs are post-MVP.

### REST API, Web UI & Authentication

The daemon provides an embedded Web UI console alongside the local HTTP REST server on port 8080 (configurable via `API_ADDR`):

- `GET /`: Serves the Single Page Application (SPA) administration console built with vanilla HTML/CSS/JS (embedded directly into the binary via `go:embed` without CDN or runtime filesystem dependencies).
- `GET /health`: Returns `{"status":"ok"}` with `200 OK` (public).
- `GET /api/auth/status`: Returns `{"isSetup": bool}` indicating whether the admin password has been initialized.
- `POST /api/auth/setup`: Accepts `{"password": "..."}` to configure the admin password on first run, saves the bcrypt hash into `sync.db` (`global_config.admin_password_hash`), issues an HMAC-signed session token, and sets an `HttpOnly` session cookie. Fails if already configured.
- `POST /api/auth/login`: Accepts `{"password": "..."}`, verifies against stored bcrypt hash, and returns a session token / sets an `HttpOnly` session cookie.
- `POST /api/auth/logout`: Clears the session cookie.
- `POST /api/auth/change-password`: Accepts `{"currentPassword": "...", "newPassword": "..."}`, verifies existing password hash, and updates stored bcrypt hash.
- `GET /api/whatsapp/status`, `POST /api/whatsapp/pair`, `DELETE /api/whatsapp/pair`, `POST /api/whatsapp/logout`, `GET /api/whatsapp/groups`: Manage WhatsApp client connection, QR pairing session, logout/unlinking, and on-demand ephemeral group discovery.
- `GET /api/endpoints`, `POST /api/endpoints`, `GET /api/endpoints/{alias}`, `PUT /api/endpoints/{alias}`, `DELETE /api/endpoints/{alias}`: Manage transport-neutral endpoint configuration. Endpoint DTOs contain only the safe alias, transport, opaque `remoteId`, and optional `syncSetId`; credentials and tokens are not part of this API.
- `GET /api/discord/status`: Authenticated safe Discord runtime state. It reports configured/connected state plus per-endpoint-alias managed-webhook readiness (`ready`, `missing_permission`, or `unavailable`) and never returns bot/webhook credentials or raw Discord names/IDs beyond already configured endpoint fields.
- `GET /api/discord/channels`: Authenticated on-demand live discovery of guild text/announcement channels. Guild/channel display names and discovery IDs exist only in the request/response/UI lifetime; selecting a channel persists only its operational channel ID through `/api/endpoints`.
- `GET /api/telegram/status`: Authenticated safe Telegram runtime state. It reports configured/token-source state, long-poll status, endpoint readiness, and Bot Privacy Mode readiness derived from `getMe.can_read_all_group_messages`, never exposing tokens or raw Bot API user objects.
- `GET /api/telegram/chats`: Authenticated on-demand discovery returning the bounded in-memory cache of observed group/supergroup chats. Chat titles and usernames exist only transiently in memory; selecting a chat persists only its opaque negative chat ID through `/api/endpoints`.
- `GET /api/sync-sets`, `POST /api/sync-sets`, `GET /api/sync-sets/{id}`, `PUT /api/sync-sets/{id}`, `DELETE /api/sync-sets/{id}`: Manage sync set collections across all configured transports. The JSON member field is `endpoints`; every value is an endpoint alias and may identify a WhatsApp, Discord, or Telegram endpoint. Generic `/api/groups` CRUD routes do not exist.
- `GET /api/config`, `PUT /api/config`: Read and modify global configuration options with immediate reload notifications to the router.

Auth middleware protects all other `/api/*` endpoints, including both endpoint-management API shapes, returning `401 Unauthorized` if a valid Bearer token or session cookie is missing or invalid. Endpoint request bodies are never logged, and validation/error responses never echo a transport remote target. Non-API client paths (such as `/setup`, `/login`, `/dashboard`) fall back cleanly to `index.html` for client-side routing. Session tokens and plaintext passwords are never written to application logs.

The management and runtime routing models are transport-aware. Discord and Telegram endpoints can be configured and placed in mixed sync sets. The Discord gateway adapter and Telegram long-poll adapter start when endpoints for their respective transport exist or deployment-time token sources are configured, enabling discovery before the first endpoint is created. The application dispatches each destination alias through the adapter registered for that endpoint's configured transport. Outbound text/media plus reply/reaction/edit/delete lifecycle operations are implemented behind those adapter boundaries; the canonical router still addresses only endpoint aliases and remote message copies.

## 4. Canonical message model

Every source message receives an opaque application canonical ID unrelated to transport IDs.

```text
canonical c_...
   +-- c1g1 -> WhatsApp msg A (source copy)
   +-- c1g2 -> WhatsApp msg B
   +-- c1g3 -> WhatsApp msg C
```

`message_copies` maps each endpoint’s opaque remote message ID back to the canonical ID. This is the basis for replies, reactions, edits, deletes, deduplication and restart recovery.

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

For first login, the application boots without blocking in an unpaired state and exposes the pairing lifecycle via the REST API (`/api/whatsapp/status`, `/api/whatsapp/pair`). Terminal QR rendering is gated and disabled by default. When pairing is initiated, whatsmeow generates QR codes on a managed channel, refreshing expired codes dynamically. Upon successful scanning, whatsmeow automatically persists linked-device state in `whatsapp.db` and the client transitions to connected. On restart, the stored device session connects directly.

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

### Discord gateway ingress boundary

The Discord adapter uses a gateway bot for ingress and keeps Discord protocol identity outside the canonical model:

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

Only **Guild Messages** plus **Message Content** gateway intents are requested; direct-message intents are not requested, and DMs are also rejected defensively by the normalizer. Discord user IDs, display names, guild/channel names, message bodies, and raw gateway events are never persisted or logged. User mentions are converted at ingress to transient display text, with an HMAC-derived actor fallback when no display name is available; role and channel mention IDs become generic `@role` / `#channel` text. Structured Discord member IDs therefore do not cross the adapter privacy boundary.

The gateway bot is intentionally distinct from outbound Discord sender rendering. The bot owns gateway ingress, discovery, native reply markers, reactions, and gateway lifecycle; each configured Discord destination reuses one bridge-managed incoming webhook for outbound sender rendering. Only the webhook manager's in-memory credential map knows the webhook ID/token. The router passes transient sender metadata separately from the transport-neutral attributed text, letting the Discord adapter render the sender's transient display name (from WhatsApp or Telegram) as the webhook APP username (or the HMAC actor ID when no display name is available) without persisting either display name or message content. These APP/webhook identities are presentation overrides, not real Discord user accounts, and no per-user webhook is created. A `ManagedWebhookChecker` suppresses bridge webhook message-create loops, while bridge-bot reaction events and bridge-initiated delete echoes are filtered at the Discord boundary. The application reads the Discord event channel alongside WhatsApp and Telegram in one select loop and feeds all into the same ordered router worker; no second canonical worker or platform-specific canonical-ID path is introduced.

Discord attachments are downloaded only when routing needs them, bounded by the configured media limit, held in memory, and re-uploaded with bridge-generated safe filenames. Source filenames, CDN URLs, and media bytes are never stored in `sync.db`. Native Discord replies require the bot message API because Discord incoming-webhook execution does not accept `message_reference`; when a destination copy exists the adapter emits a minimal native reply marker referencing that copy and sends the actual content under the sender-specific webhook APP identity. If no destination copy exists, an alias-based textual reply fallback is used instead.

#### Discord threads, forums, mentions, polls, and unsupported formats

Thread behavior deliberately flattens rather than creating dynamic routing identities. A gateway message whose channel is a Discord news/public/private thread is resolved through DiscordGo's live channel state. If that thread's `ParentID` is an already-configured Discord endpoint, the event is normalized under the **parent endpoint alias**. Replies, reactions, edits, and deletes use the same rule. Thread IDs and forum-post IDs are never inserted into `endpoints`, never become canonical IDs, and are retained only when Discord itself supplies a remote message-copy ID required by lifecycle mapping. If the parent cannot be resolved or is not configured, the thread event is ignored.

Discord forum posts are thread channels, so ingress follows the same parent-flattening rule when a forum parent has already been configured. The bridge does not dynamically create forum posts or thread-specific endpoint records, and the admin discovery UI intentionally continues to expose only sendable text/announcement channels. Outbound traffic addressed to an alias always targets that alias's configured parent channel; it does not attempt to return content to the originating thread. Automatic outbound forum-post creation remains outside this mapping.

WhatsApp native polls sent to Discord use a deterministic textual representation: `Poll: <question>`, numbered option lines, and a single-/multi-select hint. Poll question and option labels remain transient; only the existing SHA-256 option hashes used by canonical poll aggregation may be persisted. Native Discord poll objects are ignored because mapping Discord vote state would require additional identity/state semantics not present in this ticket.

WhatsApp stickers sent to Discord use the existing transient-media path and are uploaded with the bridge-generated filename `sticker.webp` and `image/webp` content type. Native Discord sticker-only messages, embed/component-only messages, Discord system messages, and other unsupported message types are ignored deterministically rather than producing empty or ambiguous canonical messages. A normal text/caption plus a supported attachment continues through the standard transient-media path.

For bridged mentions toward Discord, the adapter replaces known transient remote mention tokens with a display label; if the supplied label is missing or is itself the raw remote identity, the output uses `@participant`. Discord webhook allowed-mention parsing remains disabled, so fallback text cannot unexpectedly ping Discord identities.

### Discord admin discovery and webhook readiness boundary

Discord administration reuses the transport adapter rather than introducing a second protocol client. An authenticated admin request may ask the live adapter for guild text/announcement channels; guild/channel display names are returned directly to the requesting UI and are never inserted into configuration, canonical-message tables, logs, or caches outside process/browser memory. The admin chooses a safe endpoint alias and optional sync set, then the existing transport-neutral `/api/endpoints` mutation persists only the alias, `transport=discord`, and selected channel ID.

Managed-webhook preparation tracks only a safe readiness enum per configured channel in addition to the in-memory webhook credential. A Discord REST 403 while listing/creating the bridge webhook is classified as `missing_permission`, allowing the gateway and inbound discovery to remain live while outbound webhook readiness is visibly degraded. If an execute/edit/delete operation proves its cached credential invalid, the manager invalidates only that channel, serializes list-or-create repair per channel, and retries that operation once. Unknown target messages remain idempotent for edit/delete when the credential is valid; an unknown webhook is repaired instead. The API maps readiness back to configured endpoint aliases; it never returns webhook IDs, tokens, URLs, raw Discord errors, or transient guild/channel names in the status response.

The embedded Web UI has no Discord credential form. It only explains the deployment-time `DISCORD_BOT_TOKEN` / `DISCORD_BOT_TOKEN_FILE` configuration, performs authenticated status/discovery calls, and uses transport-neutral endpoint/sync-set APIs for persistence. DiscordGo performs REST rate-limit retry/backoff for gateway REST, discovery, webhook, reply, reaction, edit, and delete operations. Gateway `READY`, `RESUMED`, and `DISCONNECT` events update only the adapter's in-memory connected flag with fixed safe log fields; raw lifecycle event data is never logged. On reconnect the in-memory webhook credential remains usable, while a process restart runs managed-webhook discovery again and reuses the existing bridge-owned channel webhook instead of creating one per participant or restart.

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

It maps only configured group/supergroup chat IDs to endpoint aliases, HMAC-normalizes `telegram:<user_id>` immediately, keeps display names/text/captions transient, and rejects private chats and bridge-bot echoes from canonical routing. Every accepted Bot API `update_id`, including deliberately ignored or unconfigured updates, crosses the adapter boundary only as a content-free checkpoint/no-op or a normalized event; the coordinator persists the highest contiguous accepted update ID. The first long-poll request starts at that persisted ID plus one, while the Bot API client's in-process offset continues moving to prevent continuous duplicate delivery. `text_mention` user objects are reduced to HMAC actor IDs plus transient display labels before crossing the adapter boundary, while username-only mention entities are rewritten to the generic `@mention` fallback so raw Telegram usernames are not carried into canonical content.

The application registers Telegram in the existing transport adapter registry and consumes its event channel in the same single canonical router loop as WhatsApp and Discord. Runtime config reload updates the registry and Telegram alias-to-chat mapping without changing canonical/message-copy state. Telegram message IDs remain opaque remote-copy IDs only; Telegram outbound text/media and reply/reaction/edit/delete lifecycle operations use the same canonical/message-copy model.

#### Telegram forum topics, group migration, mentions, polls, and media handling

Telegram forum topics deliberately flatten into the configured parent group/supergroup alias. `message_thread_id` and forum-topic names stay transient and never become endpoint, canonical, or database keys. Ordinary cross-platform outbound messages target the configured parent/general chat context; mapped replies use only the existing remote-copy reply reference and do not create a persisted topic mapping.

A basic-group to supergroup migration is handled as addressing maintenance: the adapter recognizes Telegram's migration service message, transactionally changes only the matching Telegram endpoint `remote_id`, preserves alias and `sync_set_id`, then replaces its in-memory chat mapping so subsequent messages from the supergroup continue routing under the same alias. Migration logs never include old/new chat IDs.

Telegram polls intentionally do not add a second poll state machine. Existing native poll content routed toward Telegram becomes deterministic text containing the question, numbered options, and selection guidance. A Telegram poll is normalized to that same text form before it reaches the canonical router, so other transports receive text rather than Telegram poll identity or vote state.

Media formats reuse canonical kinds where practical: sticker → `sticker`, voice note → `audio`, video note → `video`, MP4 animation → `video`, and non-MP4 animation → generic `document`. Hosted Bot API media handling enforces 10 MiB for photos, 50 MiB for general uploads, and Telegram sticker format caps (512 KiB static WebP, 64 KiB TGS, 256 KiB WebM), further bounded by the configured media limit. Contacts, locations/venues, payments/games, membership/title/photo/pin/forum service messages, and otherwise unsupported service-only payloads do not create canonical content; configured-chat ignores may log only fixed event classes and safe endpoint aliases.

### Telegram admin discovery and privacy readiness boundary

Telegram administration is also adapter-owned rather than router-owned. Because the Bot API cannot enumerate every group a bot belongs to, the adapter observes group/supergroup chats from live long-poll updates before configured-endpoint filtering and keeps only a bounded process-memory cache of safe selection metadata: opaque chat ID plus transient title/username/type. Private chats never enter this cache.

The authenticated admin API exposes safe long-poll/endpoint-readiness state at `/api/telegram/status` and the current transient observation cache at `/api/telegram/chats`; it never returns bot credentials or raw Bot API objects. Selecting a chat still creates an ordinary transport-neutral endpoint through `/api/endpoints`, so only the alias, `transport=telegram`, opaque chat ID, and optional sync-set membership enter `sync.db`. Chat titles/usernames are neither logged nor persisted and disappear on restart.

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

The recovery coordinator is the single accepted-event checkpoint boundary. A normalized incoming event may carry only a stream key, ordered position, and timestamp. The coordinator serializes each stream, invokes the ordinary router handler, and advances the durable cursor only after canonical/delivery intent is durable or the router returns a deliberate no-op. Failed positions block later acknowledgements until replayed; duplicate positions at or below the cursor are accepted no-ops. Optional adapter recovery sources receive fixed in-code count/age bounds and emit normalized events back through this same callback. Startup and reconnect recovery are single-flight and do not create a second delivery path or a periodic poller.

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
- native WhatsApp polls.

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

Because raw participant JIDs/message bodies are intentionally not stored, native quote reconstruction may sometimes be impossible after restart. The fallback is textual provenance, e.g. `↪ c1g1/u_abcd1234` rather than weakening privacy.

## 10. Reactions

Reaction state uses:

- canonical ID;
- source endpoint alias;
- HMAC actor ID;
- emoji.

This permits add/change/remove semantics without raw identity. A native reaction on a destination is necessarily made by the bridge WhatsApp account; origin attribution may require companion text if product behavior requires visible original actor identity.

## 11. Polls and vote aggregation

Poll creation preserves native WhatsApp polls on WhatsApp destinations and renders the same transient question/options as deterministic text on Discord and Telegram destinations. Incoming WhatsApp poll updates (`PollUpdateMessage`) are decrypted using whatsmeow's message-secret capabilities and recorded per HMAC actor and option SHA-256 hash in `sync.db`; Discord and Telegram textual copies do not introduce a second vote-state model. Incoming Telegram polls are likewise normalized to deterministic text before entering the canonical router.

Replying `aggregate-response` to any poll copy triggers cross-group aggregation:
- the router intercepts the trigger (it is not fanned out);
- sums the votes for each option across all groups;
- formats and sends an aggregated text summary to all groups in the sync set, quoting each group's local copy of the poll.

Option text is retained transiently in memory for formatted summaries during the session and falls back cleanly to generic option indices (`Option 1`, `Option 2`) upon server restart.

## 12. Edits and deletes

Edits and revokes resolve the target through canonical mapping and apply to all known copies using whatsmeow helpers/protocol APIs.

Content is never persisted merely for edit idempotency. A content hash may be considered later only if a demonstrated replay problem requires it and if the hash does not create a privacy leak; MVP should prefer protocol event IDs/tombstone state.

Deletes mark a canonical message tombstoned before/while propagation so offline recovery cannot resurrect it.

## 13. Offline recovery

Recovery is bounded and best effort. Use WhatsApp offline/history events and, where appropriate, whatsmeow history-sync primitives.

Config bounds:

- maximum age;
- maximum messages per group.

Recovered events enter the same normalization/router path as live events. There is no separate recovery forwarding implementation.

## 14. Retention

`sync.db` mapping retention defaults to 90 days. Cleanup is batched. After expiry, very old reply/reaction/edit/delete events may fall back or no longer propagate.

WhatsApp chat history on the sync account can optionally be cleared on a daily schedule via WhatsApp AppState `ClearChatAction` patches (`whatsapp_chat_cleanup_enabled`, `whatsapp_chat_retention_days`). This clears old messages on the sync account only for groups configured in sync-sets without modifying `sync.db` mappings or deleting messages for other group participants.

`whatsapp.db` retention is controlled by whatsmeow/protocol requirements and monitored separately; it is not an application history store.

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

The Discord adapter replaces DiscordGo's default logger with a fixed-field warning/error classifier because raw gateway/client errors may contain protocol identifiers or other sensitive values. Application-visible Discord logs use only fixed event names, safe endpoint aliases, message kinds/counts, and safe error classes. The bot token is read only from `DISCORD_BOT_TOKEN` or `DISCORD_BOT_TOKEN_FILE`; managed webhook IDs/tokens are discovered or created at runtime and kept only in memory. None of those credentials are copied to `sync.db` or application logs. DiscordGo retry-on-rate-limit behavior is enabled for gateway REST operations and explicitly requested for webhook, reply, reaction, edit, and delete calls.

The Telegram adapter reads its bot credential only from `TELEGRAM_BOT_TOKEN` or `TELEGRAM_BOT_TOKEN_FILE`; ambiguous dual-source configuration is rejected and the token is never written to `sync.db`. The Bot API client's default raw update/error logging is not enabled: application-visible client errors pass through the fixed safe-log classifier, and ingress buffer/lifecycle logs contain only fixed event names, safe endpoint aliases, normalized kinds, and reasons. The long-poll client starts at the persisted generic `telegram` cursor plus one, advances its Bot API `getUpdates` offset in process, and applies bounded retry/backoff (including Telegram `retry_after` responses). The adapter rejects duplicate/older update IDs within the running process before normalization; Telegram's retained update window limits how far a restart can recover.

## 19. Deliberate MVP exclusions

Dynamic Discord thread endpoint creation, automatic outbound forum-post creation, native Discord poll/vote bridging, and directional bridge modes remain excluded. Telegram webhook ingestion, local Bot API server deployment, dynamic forum-topic endpoints, native cross-platform Telegram poll/vote bridging, and MTProto user-account sessions also remain excluded. Events/locations/contacts, dedicated-number provisioning, cloud persistence, email/SMS, LinkedIn/enrichment, AI document analysis and historical ZIP bootstrap remain deferred. See `docs/ASPIRATIONAL_FEATURES.md`.
