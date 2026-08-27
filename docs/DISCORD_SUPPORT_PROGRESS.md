# Discord Support Implementation Progress

> **Temporary implementation tracker.**
>
> This file exists only while Discord Transport Adapter support is being implemented on `agent/discord-support`.
> The agent completing the final ticket **must delete this file before closing the final issue**.

## Non-negotiable constraints

- All implementation work happens on branch `agent/discord-support`.
- Do **not** update `VERSION` or any release version.
- Preserve the zero-PII/PHI privacy model.
- Endpoint aliases remain the application/router identity.
- Discord and WhatsApp message IDs are remote copy IDs only; never canonical IDs.
- Discord credentials come from environment variables or secret mounts, never `sync.db`.
- Every ticket owner must update this file when work starts and when work completes.
- Before closing a ticket, the agent must:
  1. update the row below with final status and notes;
  2. add a final GitHub issue comment summarizing implementation and tests;
  3. close the GitHub issue as completed.

## Implementation sequence

| Order | Issue | Scope | Depends on | Status | Implementation notes |
|---|---|---|---|---|---|
| 1 | [#16](https://github.com/vm75/message-sync/issues/16) | Generalize configured groups into transport-aware endpoints | — | In progress | Implementation commits `13335f4` + `9eb5d2d`: schema v11 transport-aware endpoints, v10 WhatsApp migration, config/runtime compatibility, and tests added. Verification blocked: Actions run 33108114410 failed twice before runner startup (no steps/logs), so required `make fmt/test/vet` are not verified; issue remains open. |
| 2 | [#17](https://github.com/vm75/message-sync/issues/17) | Generalize REST API and sync-set CRUD for transport endpoints | #16 | Not started | |
| 3 | [#18](https://github.com/vm75/message-sync/issues/18) | Discord adapter foundation, bot lifecycle, channel event ingestion | #16, #17 | Not started | |
| 4 | [#19](https://github.com/vm75/message-sync/issues/19) | Wire multiple adapters into application/router | #16, #18 | Not started | |
| 5 | [#20](https://github.com/vm75/message-sync/issues/20) | Discord outbound text/media/replies/reactions/edits/deletes | #18, #19 | Not started | |
| 6 | [#21](https://github.com/vm75/message-sync/issues/21) | Discord discovery + admin web UI | #17, #18, #19 | Not started | |
| 7 | [#22](https://github.com/vm75/message-sync/issues/22) | Threads/forums, mentions, polls, unsupported-format fallbacks | #20; #21 before UI exposure | Not started | |
| 8 | [#23](https://github.com/vm75/message-sync/issues/23) | End-to-end hardening, docs, privacy review, tracker cleanup | #16–#22 | Not started | |

## Target architecture

The implementation should evolve toward this shape:

```text
WhatsApp adapter -----\
                      \
                       > canonical router -> per-endpoint adapter dispatch
                      /
Discord adapter ------/

configured endpoint
  alias        = safe application identity
  transport    = whatsapp | discord
  remote_id    = transport-specific addressing value
  sync_set_id  = one sync set
```

The router must stay transport-neutral. It should resolve aliases and sync-set membership, create/resolve canonical IDs, and persist message-copy mappings. Transport adapters own protocol details.

## Detailed implementation plan

### Phase 1 — Generalize endpoint configuration
Issue #16 introduces a transport-aware endpoint schema and migrates existing WhatsApp group rows without changing user aliases or sync-set membership.

Expected outcome:
- existing WhatsApp installations migrate automatically;
- new endpoints carry `transport` + opaque `remote_id`;
- aliases remain safe endpoint IDs;
- no Discord credentials or human-readable Discord metadata enter SQLite.

### Phase 2 — Generalize management APIs
Issue #17 moves CRUD and sync-set membership toward endpoint terminology while preserving a documented compatibility path for existing WhatsApp group APIs.

Expected outcome:
- mixed WhatsApp/Discord aliases can exist in one sync set;
- API validation remains centralized and safe;
- management changes continue to trigger runtime config reloads.

### Phase 3 — Build Discord ingress adapter
Issue #18 adds the Discord client, lifecycle, safe gateway handling, channel filtering, HMAC actor identity, self-message loop prevention, webhook-loop prevention, and normalization to `transport.Incoming`. It also establishes the bot-ingress + webhook-outbound split required for per-WhatsApp-user APP rendering.

Expected outcome:
- Discord messages from configured channels can enter the same router path as WhatsApp;
- DMs/unconfigured channels are discarded at the adapter boundary;
- bridge-owned webhook messages do not loop back as fresh ingress;
- Discord tokens, webhook credentials, and raw events never persist or enter logs.

### Phase 4 — Multi-adapter router/application wiring
Issue #19 makes application wiring adapter-agnostic and dispatches each destination alias to the correct adapter.

Expected outcome:
- WA -> Discord and Discord -> WA routing use the same canonical state model;
- partial fan-out remains restart-safe and idempotent;
- WhatsApp-only deployments still work unchanged.

### Phase 5 — Complete Discord message lifecycle
Issue #20 adds Discord outbound send, transient media, replies, reactions, edits, and deletes. WhatsApp -> Discord sends must use a bridge-managed channel webhook with the transient WhatsApp display/push name as the per-message webhook username so each WhatsApp participant appears as a distinct Discord APP/webhook sender. If no display name is available, use the HMAC actor ID fallback.

Expected outcome:
- different WhatsApp participants visibly appear in Discord under their own transient APP/webhook usernames, not one generic bridge bot identity;
- no real Discord accounts or per-user webhooks are created;
- webhook-created Discord message IDs are stored as normal opaque message copies for lifecycle mapping;
- the existing canonical lifecycle semantics work across transports;
- native Discord replies/reactions are used when possible;
- privacy-preserving text fallbacks are used when mapping is unavailable;
- WhatsApp display names remain transient and are never persisted/logged;
- media is never persisted.

### Phase 6 — Admin discovery and UI
Issue #21 adds Discord connection/status, transient channel discovery, alias assignment, mixed-transport sync-set editing, and safe readiness/status for the bridge-managed channel webhook used for WhatsApp sender APP rendering.

Expected outcome:
- an admin can discover/select a Discord channel, give it a safe alias, and add it to a sync set;
- the bridge can create/find/reuse the channel webhook when permissions allow;
- missing webhook-management permissions produce a clear actionable admin error;
- admins are not asked to create per-user webhooks or store webhook URLs;
- guild/channel display metadata remains transient;
- bot token and webhook credentials are configured/managed without entering application persistence.

### Phase 7 — Discord-specific format semantics
Issue #22 defines deterministic behavior for Discord threads/forums, mentions, WhatsApp polls, stickers, and unsupported Discord message types.

Expected outcome:
- no dynamic explosion of persisted thread endpoints;
- canonical IDs stay platform-neutral;
- unsupported mappings fail/fallback predictably without weakening privacy.

### Phase 8 — Hardening, docs, and cleanup
Issue #23 runs the mixed-transport test matrix, privacy review, reconnect/rate-limit tests, sender-rendering verification, documentation updates, and final cleanup.

Expected outcome:
- all earlier issues are closed;
- end-to-end tests prove at least two WhatsApp participants render with different Discord APP/webhook usernames plus the HMAC fallback path;
- bot/gateway vs webhook responsibilities and required webhook permissions are documented;
- webhook-created copies participate correctly in canonical replies/reactions/edits/deletes;
- `make fmt`, `make test`, and `make vet` pass;
- deployment/docs accurately describe Discord support;
- `VERSION` is unchanged;
- this file is deleted.

## Cross-ticket acceptance gates

Every implementation ticket must satisfy all applicable gates below:

- `make fmt`
- `make test`
- `make vet`
- container build/config checks when runtime/deployment files change
- no raw Discord user IDs, usernames, guild names, channel names, message bodies, media, filenames, tokens, or raw event/error payloads in retained logs or `sync.db`
- no platform message ID used as canonical ID
- no version bump

## Progress update format

When updating a row, use concise notes such as:

```text
Status: In progress
Notes: Started schema migration; preserving existing WhatsApp aliases. PR #NN.
```

At completion:

```text
Status: Complete
Notes: Merged commit abc123; make fmt/test/vet passed; issue final comment posted.
```

## Final cleanup requirement

When issue #23 is complete:

1. verify issues #16–#22 are closed as completed;
2. verify `VERSION` is unchanged from the branch point;
3. record final verification in issue #23;
4. delete `docs/DISCORD_SUPPORT_PROGRESS.md`;
5. close issue #23.

