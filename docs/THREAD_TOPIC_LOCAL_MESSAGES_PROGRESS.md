# Thread/Topic Context + Local Messages Progress

Temporary implementation tracker for:

- Discord thread/forum-post context preserved through otherwise-flat sync sets;
- Telegram forum-topic context preserved through otherwise-flat sync sets;
- privacy-safe flattening headers;
- configurable source-local messages.

This file is implementation coordination state only. It must be deleted by issue #72 after all work is complete and authoritative documentation has been updated.

## Product direction

The existing sync-set architecture remains flat:

- WhatsApp group = configured endpoint;
- Discord parent channel = configured endpoint;
- Telegram parent group/supergroup = configured endpoint.

Discord threads/forum posts and Telegram topics are **not** configured endpoints and are not added to sync sets. They are message-level conversation context carried through canonical reply lineage.

When a child context cannot be represented natively on a destination, message-sync renders a privacy-safe header. Native replies to the flattened message use durable canonical metadata to return to the relevant thread/topic. Visible tags are presentation only and must never be parsed for routing.

Group-local messages use one global configurable prefix. A newly-created message beginning exactly with that prefix remains only in its source group/channel/thread/topic and creates no cross-platform fan-out.

## Non-negotiable invariants

1. Keep endpoint and sync-set configuration unchanged.
2. Do not create dynamic thread/topic endpoint records.
3. Do not automatically create Discord threads/forum posts or Telegram topics.
4. Do not introduce MTProto.
5. Do not introduce directional bridge semantics.
6. Do not introduce paid/cloud dependencies.
7. Do not parse bridge-rendered headers/tags for routing.
8. Persist only opaque operational child/message IDs required for routing/idempotency; never persist child names or message content.
9. Never log Discord/Telegram remote scope IDs or child names.
10. Preserve existing loop prevention, delivery-lane, retry, recovery, poll, reply, reaction, edit, and delete behavior.
11. VERSION must remain unchanged.
12. Follow KISS and YAGNI; do not build a generic conversation-management subsystem.

## Target data flow

### Child conversation

1. Adapter resolves the configured parent endpoint alias.
2. Adapter also supplies an optional local child scope.
3. Router resolves the canonical message.
4. For replies, router inherits child scopes from the replied-to canonical message.
5. Incoming native child scope overrides inherited scope only for the same endpoint alias.
6. Canonical message may therefore carry independent child scopes for multiple endpoint aliases.
7. For each destination:
   - if a scope exists for that destination, send natively into the child conversation;
   - otherwise send to the configured flat endpoint with a privacy-safe presentation header.
8. Persisted reply lineage, not visible text, determines future routing.

### Local-only message

1. Preserve existing known-copy/echo/idempotency handling.
2. If source remote ID is already marked suppressed-local, stop.
3. If a new eligible create begins exactly with the configured prefix, persist only an opaque suppression marker and stop.
4. Do not create a canonical message, delivery operation, destination copy, or media download.
5. Lifecycle events for that source message remain local.
6. A later non-local reply may bridge, but must not leak quoted content from the suppressed local message.

## Implementation order

| Order | Issue | Status | Depends on | Primary responsibility |
|---|---|---|---|---|
| 1 | [#67 Conversation context 1/6: add transport-neutral child-context persistence and reply inheritance](https://github.com/vm75/message-sync/issues/67) | Pending | — | Core transport/store/router scope model, persistence, inheritance, lifecycle access |
| 2 | [#68 Conversation context 2/6: add privacy-safe flattening headers for threads and topics](https://github.com/vm75/message-sync/issues/68) | Pending | #67 | Stable non-reversible context tokens, transient labels, rendering rules, anti-spoofing |
| 3 | [#69 Conversation context 3/6: preserve Discord thread and forum-post routing through flat sync sets](https://github.com/vm75/message-sync/issues/69) | Pending | #67, #68 | Discord ingress scope, thread-aware webhook/bot sends, lifecycle, bounded thread recovery |
| 4 | [#70 Conversation context 4/6: preserve Telegram forum-topic routing through flat sync sets](https://github.com/vm75/message-sync/issues/70) | Pending | #67, #68 | Telegram message_thread_id ingress/outbound, replies, media/polls, update replay |
| 5 | [#71 Local messages 5/6: add configurable group-local message prefix](https://github.com/vm75/message-sync/issues/71) | Pending | — | Prefix config/API/UI, durable suppression, lifecycle/privacy rules |
| 6 | [#72 Integration 6/6: harden thread/topic context and local messages, update docs, remove tracker](https://github.com/vm75/message-sync/issues/72) | Pending | #67–#71 | Cross-transport integration, restart/privacy regression, docs, tracker deletion |

Implementation should normally follow the order above. #71 is conceptually independent of #67–#70, but keeping one sequential order simplifies autonomous agent execution and final verification.

## Detailed implementation plan

### Phase 1 — Core child-context model (#67)

- Extend the transport-neutral create/reference boundary with the minimum optional scope metadata.
- Add durable canonical-message scope persistence keyed by canonical message + endpoint alias.
- Inherit scopes through native reply mapping.
- Keep one scope per endpoint alias; local ingress scope wins for its own endpoint.
- Allow scopes for different endpoint aliases to coexist.
- Make scope metadata available to create and lifecycle paths without leaking provider objects into the router.
- Ensure cleanup follows canonical retention and all writes are idempotent.

Exit condition: the core can represent/recover scoped reply lineage without any Discord/Telegram-specific routing logic.

### Phase 2 — Presentation headers (#68)

- Generate a deterministic privacy-safe visible child token using domain-separated HMAC or the smallest equivalent existing helper.
- Never expose raw child IDs.
- Optionally include a transient sanitized child label when available live.
- Render headers only for child contexts that are flattened at the specific destination.
- Preserve existing sender attribution and media/poll fallback presentation.
- Never parse inbound headers for routing.

Exit condition: users can distinguish flattened child conversations, but forged/copied tags have no routing effect.

### Phase 3 — Discord threads/forum posts (#69)

- Stop erasing thread identity during parent endpoint resolution.
- Normalize child messages as parent endpoint + Discord child scope.
- Preserve one managed webhook per configured parent and add optional thread target execution.
- Route native bot sends/polls to the thread channel.
- Make edit/delete/reaction/poll companion lifecycle thread-aware where Discord requires a channel/thread ID.
- Fix poll-vote thread resolution consistency.
- Extend bounded recovery to accessible active/archived child threads beneath configured parents, without a permanent discovery registry.
- Explicit scoped failures must not silently fall back to parent.

Exit condition: a flattened Discord child conversation can be replied to from another platform and continues in the original thread/forum post across restart.

### Phase 4 — Telegram topics (#70)

- Capture non-zero message_thread_id as Telegram child scope while retaining the parent chat endpoint alias.
- Set MessageThreadID on all supported create paths when a destination scope exists.
- Keep native reply metadata plus explicit topic target where appropriate.
- Preserve topic scope through Bot API update replay.
- Keep group-to-supergroup migration limited to parent chat addressing.
- Explicit scoped failures must not silently fall back to General.
- Do not add MTProto or topic discovery/configuration.

Exit condition: a flattened Telegram topic can be replied to from another platform and continues in the original topic across restart.

### Phase 5 — Group-local prefix (#71)

- Add one global local-only prefix to persisted config, authenticated config API, and Web UI.
- Empty disables; exact case-sensitive prefix match starts at the first character.
- Persist only source endpoint alias + opaque remote message ID + timestamp for suppressed messages.
- Suppress before canonical creation, fan-out, and media download.
- Keep edit/delete/reaction lifecycle local for suppressed messages.
- Prevent a non-local reply to a suppressed message from leaking the suppressed quoted text.
- Apply the same rule inside Discord threads and Telegram topics.

Exit condition: operator can intentionally keep a message in exactly the source conversation with restart-safe behavior and no content persistence.

### Phase 6 — Integration, documentation, cleanup (#72)

- Exercise WhatsApp <-> Discord thread/forum and WhatsApp <-> Telegram topic reply return.
- Exercise Discord thread <-> Telegram topic reply lineage with both endpoint-specific scopes present.
- Exercise 3-way sync with WhatsApp flat presentation.
- Exercise local-only prefix on every parent/child source combination.
- Verify loop prevention, retries, recovery, polls, lifecycle events, retention, and privacy after restart.
- Update README.md, ARCHITECTURE.md, docs/FEATURE_COMPARISON.md, docs/TESTING_GUIDE.md and other applicable docs.
- Keep upstream-inspiration references limited to docs/FEATURE_COMPARISON.md.
- Delete this tracker after all acceptance criteria are satisfied.

## Required regression matrix

At minimum verify:

- Discord parent -> normal flat fan-out unchanged;
- Discord thread/forum -> flat other transports + native reply return;
- Telegram parent/general -> normal flat fan-out unchanged;
- Telegram topic -> flat other transports + native reply return;
- WhatsApp reply -> Discord thread;
- WhatsApp reply -> Telegram topic;
- Discord parent reply -> Telegram topic;
- Telegram parent reply -> Discord thread;
- Telegram topic reply to flattened Discord-thread message retains both contexts;
- later WhatsApp reply returns to both applicable child contexts;
- native polls and aggregate-only result companions stay in the relevant child context;
- edit/delete/reaction lifecycle stays correctly targeted;
- deleted/locked/missing child destination fails safely instead of flattening silently;
- manually typed context header never routes;
- local-only prefix suppresses text, captioned media, and documented eligible poll text;
- textless media behavior is documented;
- suppressed media is not downloaded;
- suppressed-message lifecycle does not bridge;
- unprefixed reply to suppressed message does not leak its quoted text;
- restart/recovery preserves child scope and local suppression;
- retention cleanup is bounded;
- no PII/content/raw provider IDs leak to sync.db or logs.

## Per-issue completion protocol

For each issue:

1. Read this tracker and the issue in full.
2. Verify prerequisite issues are complete.
3. Implement only the issue scope.
4. Run make fmt, make test, and make vet.
5. Run additional container checks when runtime/config packaging is affected.
6. Verify VERSION is unchanged.
7. Update the issue row in this tracker to Complete.
8. Update any authoritative docs required by that issue without duplicating the final documentation pass.
9. Add a final GitHub issue comment with implementation decisions, privacy verification, and test results.
10. Close the issue.

## Final completion

Issue #72 is complete only after:

- #67 through #71 are closed complete;
- all cross-transport/restart/privacy regression checks pass;
- authoritative docs describe the implemented behavior;
- no upstream-inspiration references exist outside docs/FEATURE_COMPARISON.md;
- VERSION is unchanged;
- this file is deleted.
