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
 config.json                     IDENTITY_SECRET
 aliases + group JIDs            environment/secret
      |                                |
      v                                v
 +---------+                      +----------+
 | Config  |                      | Identity |
 +----+----+                      +----+-----+
      |                                |
      +---------------+----------------+
                      |
                      v
              +------------------+       /data/sync.db
 events ----->| Canonical Router |<----> PII-free app state
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

### `sync.db`

Owned by message-sync and designed to remain PII/PHI-free. Initial schema is in `internal/store/schema.sql`.

It may store canonical IDs, configured aliases, opaque remote message IDs, HMAC actor IDs, emoji reaction state, timestamps and recovery cursors. It must not store message content or raw participant identity.

## 3. Configuration

Configuration is JSON and contains topology, not secrets.

```json
{
  "groups": {
    "c1g1": { "jid": "...@g.us" },
    "c1g2": { "jid": "...@g.us" }
  },
  "syncSets": [
    { "id": "community1", "groups": ["c1g1", "c1g2"] }
  ]
}
```

Group JIDs are operator-managed sensitive configuration because they are required to address WhatsApp groups. They are never written to logs or `sync.db`. The alias is the application endpoint ID.

MVP restricts each group to one sync set. Arbitrary routing graphs are post-MVP.

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

## 6. Router event flow

The initial router uses one ordered worker:

```text
whatsmeow callback
   -> normalize safe event
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
- stickers.

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

## 11. Edits and deletes

Edits and revokes resolve the target through canonical mapping and apply to all known copies using whatsmeow helpers/protocol APIs.

Content is never persisted merely for edit idempotency. A content hash may be considered later only if a demonstrated replay problem requires it and if the hash does not create a privacy leak; MVP should prefer protocol event IDs/tombstone state.

Deletes mark a canonical message tombstoned before/while propagation so offline recovery cannot resurrect it.

## 12. Offline recovery

Recovery is bounded and best effort. Use WhatsApp offline/history events and, where appropriate, whatsmeow history-sync primitives.

Config bounds:

- maximum age;
- maximum messages per group.

Recovered events enter the same normalization/router path as live events. There is no separate recovery forwarding implementation.

## 13. Retention

`sync.db` mapping retention defaults to 90 days. Cleanup is batched. After expiry, very old reply/reaction/edit/delete events may fall back or no longer propagate.

`whatsapp.db` retention is controlled by whatsmeow/protocol requirements and monitored separately; it is not an application history store.

## 14. Transport abstraction

The core transport interface uses endpoint IDs and remote message IDs, not platform-specific canonical keys. MVP ships WhatsApp only.

Post-MVP Discord becomes another adapter:

```text
              canonical router
              /      |       \
        WA:c1g1  WA:c1g2  Discord:d1
```

This prevents the previous design’s Discord-centric message identity from returning.

## 15. Rootless container model

Runtime requirements:

- non-root UID/GID 10001;
- no added capabilities;
- `no-new-privileges`;
- read-only root filesystem via Compose;
- `/data` as the only persistent writable path;
- config mounted read-only;
- no host networking or privileged container.

`Containerfile`, `.containerignore` and `compose.yml` intentionally avoid Docker-specific naming.

## 16. Versioning and releases

There is no `VERSION` during MVP development. `internal/version.Build` defaults to `development`.

The image workflow listens only for `VERSION` changes on `main`. The first version file is created only after MVP acceptance, which prevents development commits from repeatedly attempting Docker Hub/GHCR publication.

Release builds inject `VERSION` using `-ldflags` and publish `amd64`/`arm64` images.

## 17. Security/logging

Never log raw whatsmeow events or arbitrary errors containing protocol structs. Use explicit safe fields such as:

```text
event=message_received endpoint=c1g1 canonical=c_... kind=text
event=fanout_failed source=c1g1 target=c1g2 error_class=timeout
```

Avoid sender JIDs, group JIDs, names, content, captions and filenames.

## 18. Deliberate MVP exclusions

Discord, web admin, user accounts, membership verification, polls/events/locations/contacts, dedicated-number provisioning, cloud persistence, email/SMS, LinkedIn/enrichment, AI document analysis and historical ZIP bootstrap are deferred. See `docs/POST_MVP.md`.
