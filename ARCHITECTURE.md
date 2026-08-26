# Architecture

## 1. Goals

`message-sync` is a small privacy-first message synchronization daemon. MVP priorities are:

1. no PII/PHI in application persistence or application logs;
2. deterministic, restart-safe synchronization;
3. simple rootless self-hosting;
4. WhatsApp transport through `tulir/whatsmeow`;
5. transport-neutral canonical IDs so Discord or another adapter can be added later.

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
                                    v
                           +------------------+
                           | WhatsApp Adapter |
                           | whatsmeow        |
                           +--------+---------+
                                    |
                                    +----------> /data/whatsapp.db
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

It may store canonical IDs, configured aliases, opaque remote message IDs, HMAC actor IDs, emoji reaction state, option SHA-256 hashes for polls, timestamps and recovery cursors. It must not store message content, poll question/option labels, or raw participant identity.

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
- `groups`: Table of groups (`alias TEXT PRIMARY KEY`, `jid TEXT NOT NULL`, `sync_set_id TEXT REFERENCES sync_sets(id)`).

The alias is the safe endpoint ID. Group JIDs are stored only in the configuration table for WhatsApp addressing and are never written to message tables or application logs.

Each configured group must belong to exactly one sync set. Arbitrary routing graphs are post-MVP.

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
- `GET /api/groups`, `POST /api/groups`, `GET /api/groups/{alias}`, `PUT /api/groups/{alias}`, `DELETE /api/groups/{alias}`: Manage group definitions and sync set mappings.
- `GET /api/sync-sets`, `POST /api/sync-sets`, `GET /api/sync-sets/{id}`, `PUT /api/sync-sets/{id}`, `DELETE /api/sync-sets/{id}`: Manage sync set collections and member group assignments.
- `GET /api/config`, `PUT /api/config`: Read and modify global configuration options with immediate reload notifications to the router.

Auth middleware protects all other `/api/*` endpoints, returning `401 Unauthorized` if a valid Bearer token or session cookie is missing or invalid. Non-API client paths (such as `/setup`, `/login`, `/dashboard`) fall back cleanly to `index.html` for client-side routing. Session tokens and plaintext passwords are never written to application logs.

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

## 6. WhatsApp ingress and router event flow

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

## 7. Idempotency and crash recovery

Persist each successful destination copy immediately.

Example:

```text
source A
 -> c1g2/B succeeds -> persist B
 -> process crashes before c1g3
```

On replay after restart, the router sees that c1g2 already has a copy and sends only c1g3. The in-memory sent-ID cache is only an optimization; SQLite mapping is authoritative across restarts.

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

Poll creation creates native WhatsApp polls across all destination groups in the sync set. Incoming poll updates (`PollUpdateMessage`) are decrypted using whatsmeow's message-secret capabilities and recorded per HMAC actor and option SHA-256 hash in `sync.db`.

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

## 15. Transport abstraction

The core transport interface uses endpoint IDs and remote message IDs, not platform-specific canonical keys. MVP ships WhatsApp only.

Post-MVP Discord becomes another adapter:

```text
              canonical router
              /      |       \
        WA:c1g1  WA:c1g2  Discord:d1
```

This prevents the previous design’s Discord-centric message identity from returning.

## 16. Rootless container model

Runtime requirements:

- non-root UID/GID 10001;
- no added capabilities;
- `no-new-privileges`;
- read-only root filesystem via Compose;
- `/data` as the only persistent writable path;
- port 8080 exposed for local/admin REST API;
- no host networking or privileged container.

`Containerfile`, `.containerignore` and `compose.yml` intentionally avoid Docker-specific naming.

## 17. Versioning and releases

There is no `VERSION` during MVP development. `internal/version.Build` defaults to `development`.

The image workflow listens only for `VERSION` changes on `main`. The first version file is created only after MVP acceptance, which prevents development commits from repeatedly attempting Docker Hub/GHCR publication.

Release builds inject `VERSION` using `-ldflags` and publish `amd64`/`arm64` images.

## 18. Security/logging

Never log raw whatsmeow events or arbitrary errors containing protocol structs. Use explicit safe fields such as:

```text
event=message_received endpoint=c1g1 canonical=c_... kind=text
event=fanout_failed source=c1g1 target=c1g2 error_class=timeout
```

Avoid sender JIDs, group JIDs, names, content, captions and filenames.

The WhatsApp adapter disables whatsmeow/sqlstore logging entirely. It emits only fixed connection/pairing state, configured endpoint aliases, normalized kinds, and safe error classifications through the application logger. Pairing QR output is a separate sensitive terminal UI and must not be copied into retained logs or support artifacts.

## 19. Deliberate MVP exclusions

Discord, events/locations/contacts, dedicated-number provisioning, cloud persistence, email/SMS, LinkedIn/enrichment, AI document analysis and historical ZIP bootstrap are deferred. See `docs/ASPIRATIONAL_FEATURES.md`.
