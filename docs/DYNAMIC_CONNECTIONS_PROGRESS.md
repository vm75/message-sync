# Dynamic Connections Progress

> Temporary implementation tracker for issues #74-#82.
>
> **Delete this file when issue #82 is completed.** This is coordination state, not permanent product documentation.

## Goal

Replace singleton transport authentication/runtime assumptions with dynamically managed multiple WhatsApp, Discord, and Telegram connections while preserving the existing endpoint/sync-set/canonical architecture.

The required architecture invariant is:

```
Connection
  authenticates one provider identity
      ↓
Endpoint
  addresses one configured parent conversation
      ↓
ChildScope
  optionally addresses a Discord thread/forum post or Telegram topic
      ↓
Canonical Router / Sync Set
```

Connections must not become canonical identities. Discord threads/forum posts and Telegram topics remain message-level child scopes and must not become endpoints or sync-set members.

## Project constraints

- Follow KISS/YAGNI.
- `VERSION` must not change.
- Backward compatibility and schema migration are intentionally out of scope because the project is not deployed to production.
- Modify fresh-development schemas directly; do not add migration/backfill/dual-read/legacy compatibility code.
- Keep permanent docs updated as behavior lands; do not defer all documentation until the final issue.
- `sync.db` and logs must remain free of PII/PHI and plaintext provider credentials.
- Discord/Telegram bot tokens must never be returned by read APIs or stored plaintext.
- WhatsApp protocol state remains isolated from application routing state.
- Preserve loop prevention, idempotency, delivery lanes, recovery, polls, membership verification, local-only messages, and thread/topic scope lineage.
- Preserve the rootless/read-only container model with `/data` as the persistent writable location.
- Every completed issue must update its row below to **Complete** before the issue is closed.
- Only #82 may delete this tracker.

## Implementation order

```
#74 Connection model / credential storage / endpoint ownership
  ↓
#75 Dynamic ConnectionManager / adapter dispatch / shared ingress
  ↓
  ├───────────────┬────────────────┐
  ↓               ↓                ↓
#76 Discord     #77 Telegram     #78 WhatsApp
  └───────────────┴────────────────┘
                  ↓
#79 Connection API / RBAC / auditing / endpoint reassignment
                  ↓
#80 Web UI
                  ↓
#81 Cross-cutting hardening
                  ↓
#82 Final docs/regression/tracker cleanup
```

Issues #76, #77, and #78 may be implemented independently after both #74 and #75 are complete. Later issues should not start until their dependencies are satisfied.

## Progress

| Order | Issue | Scope | Dependencies | Status |
|---:|---|---|---|---|
| 1 | [#74](https://github.com/vm75/message-sync/issues/74) | Connection data model, encrypted Discord/Telegram credentials, required endpoint ownership | None | Complete |
| 2 | [#75](https://github.com/vm75/message-sync/issues/75) | Dynamic ConnectionManager, connection-aware adapter dispatch, shared ingress, dynamic recovery-source lifecycle | #74 | Complete |
| 3 | [#76](https://github.com/vm75/message-sync/issues/76) | Multi-instance Discord bots, connection-scoped discovery/status/webhooks/recovery, thread safety | #74, #75 | Complete |
| 4 | [#77](https://github.com/vm75/message-sync/issues/77) | Multi-instance Telegram bots, isolated polling/cursors/poll refs, topic scopes, migration | #74, #75 | Complete |
| 5 | [#78](https://github.com/vm75/message-sync/issues/78) | Multiple WhatsApp accounts, per-connection DBs, serialized QR pairing, recovery/cleanup/membership | #74, #75 | Complete |
| 6 | [#79](https://github.com/vm75/message-sync/issues/79) | Authenticated connection APIs, RBAC, auditing, endpoint connectionId/reassignment/deletion semantics | #74-#78 | Complete |
| 7 | [#80](https://github.com/vm75/message-sync/issues/80) | Web UI flow: Connections → Endpoints → Sync Sets; safe paste-once secrets and scoped discovery | #76-#79 | Complete |
| 8 | [#81](https://github.com/vm75/message-sync/issues/81) | Cross-cutting recovery/delivery/membership/thread/topic/poll/local-message/failure-isolation hardening | #74-#80 | Complete |
| 9 | [#82](https://github.com/vm75/message-sync/issues/82) | Final documentation/regression cleanup; remove stale singleton assumptions and delete this tracker | #74-#81 | Pending |

## Status definitions

- **Pending** — dependencies may or may not be complete; implementation not finished.
- **In Progress** — an agent is actively implementing the issue.
- **Blocked** — a concrete dependency/technical blocker exists; document it in the GitHub issue.
- **Complete** — all acceptance criteria pass, required tests pass, applicable permanent docs are updated, final issue comment is posted, and the GitHub issue is ready to close/closed.

Do not mark an issue Complete based only on code being written.

## Required completion discipline for every issue

Before marking a row Complete:

1. Re-read the issue acceptance criteria.
2. Run:
   ```
   make fmt
   make test
   make vet
   git diff --check
   git diff --exit-code VERSION
   ```
3. Run container checks when runtime/config/container behavior changed:
   ```
   podman build -f Containerfile -t message-sync:dev .
   podman compose config
   ```
4. Update permanent docs affected by the implemented behavior.
5. Verify no plaintext Discord/Telegram credential was added to:
   - `sync.db`;
   - `control.db`;
   - logs;
   - API GET responses;
   - audit records;
   - browser-persistent storage.
6. Verify thread/topic behavior still follows parent Endpoint + message-level ChildScope; do not introduce child endpoints.
7. Update this tracker row.
8. Add the issue's required final GitHub comment.
9. Close the issue only after tracker/docs/tests agree.

## Architecture decisions that must remain stable through the batch

### Connection

A Connection is one authenticated provider runtime:

- WhatsApp linked-device account;
- Discord bot;
- Telegram bot.

It owns credentials/session lifecycle and a set of configured parent endpoints.

### Endpoint

An Endpoint remains the stable routing identity and sync-set member.

It contains:

- alias;
- transport;
- connection ID;
- opaque provider parent remote ID;
- sync-set membership.

The same physical parent remote conversation must not be configured twice under different connections.

### ChildScope

Existing `canonical_scopes` remains keyed by canonical message + endpoint alias.

No connection ID belongs in that table.

Examples:

- `discord_thread`;
- `telegram_topic`.

Visible thread/topic headers remain presentation-only and have no routing authority.

### Canonical routing

The canonical router must continue addressing endpoint aliases only.

Connection selection belongs at the adapter registry/runtime boundary:

```
endpoint alias -> owning connection -> adapter
```

### Credentials

- Discord/Telegram bot tokens live encrypted only in the sensitive control plane.
- Runtime decrypts only for the relevant connection.
- Tokens are paste-once/write-only from the Web UI/API perspective.
- WhatsApp auth state lives in one sensitive protocol DB per connection.
- Changing/loss of `IDENTITY_SECRET` may make stored encrypted bot credentials unreadable; document this behavior rather than adding compatibility machinery.

### WhatsApp device name

The companion device name remains a global application setting because the current whatsmeow device property is process-global.

QR registration must be serialized, although multiple already-linked WhatsApp connections may run concurrently.

### Telegram provider-global state

Provider state that lacks an endpoint key must be namespaced by connection.

At minimum:

- recovery stream/cursor: `telegram:<connection-id>`;
- poll provider namespace/reference resolution: connection-aware.

Do not solve this by putting connection IDs into canonical identity.

## Final cleanup (#82 only)

When #74-#81 are all Complete and the final regression passes:

1. Update all permanent docs.
2. Remove stale singleton assumptions/code/documentation.
3. Verify a fresh-development startup using the new schemas.
4. Verify `VERSION` is unchanged.
5. **Delete `docs/DYNAMIC_CONNECTIONS_PROGRESS.md`.**
6. Mention tracker deletion in #82's final issue comment.
7. Close #82.
