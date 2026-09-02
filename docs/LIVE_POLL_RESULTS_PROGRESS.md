# Live Poll Results Progress

> Temporary implementation tracker. Delete this file as part of issue #51 after all live-poll issues are complete and durable behavior/design/limitations have been moved into the authoritative project documentation.

## Objective

Implement proper native polling for WhatsApp, Discord, and Telegram where each destination can represent the poll, plus live cross-platform aggregate result companions.

Live result messages must show **aggregate option counts only**. They must never show who voted for what.

Retain the existing `aggregate-response` reply trigger throughout this series.

## Non-negotiable constraints

- Preserve existing text, media, reply, reaction, edit, delete, recovery, configuration, discovery, and delivery behavior.
- The canonical router remains the owner of cross-endpoint synchronization semantics.
- Provider normalizers remain part of each transport boundary; do not create a new normalization service.
- Do not replace canonical routing with direct WhatsApp/Discord/Telegram cross-calls.
- Reuse the existing per-endpoint delivery lanes, delivery ledger, mutation revision ordering, retries, and failure isolation.
- Do not add a poll worker, event bus, distributed queue, Redis, polling daemon, generic mutation framework, or new database.
- `sync.db` and retained application logs must remain free of poll question/option text and raw participant identity.
- Aggregate-only presentation does not remove the need for HMAC actor state on providers whose vote add/change/remove events require reconciliation.
- Telegram absolute poll snapshots should be preferred over storing Telegram voter identities where available.
- Human-created Telegram source polls have a Bot API observability limitation; do not add MTProto to eliminate it.
- Never silently truncate poll semantics. Use deterministic native-vs-text-fallback behavior.
- Keep `aggregate-response` and `poll_aggregation_trigger` for this series.
- This product is not deployed to production. Modify the fresh-development schema directly; do **not** add schema migrations, legacy compatibility, dual-read/dual-write paths, or data backfills.
- Do not update `VERSION`.

## Implementation order

Agents should work in the order below. Before starting a ticket:
1. read this tracker;
2. read `AGENTS.md` and the relevant parts of `ARCHITECTURE.md`;
3. open the GitHub issue and read its full scope, dependencies, privacy requirements, out-of-scope items, acceptance criteria, and completion protocol;
4. verify all required dependency tickets are complete;
5. implement only the current ticket's scope;
6. run the ticket's required tests plus `make fmt`, `make test`, and `make vet`;
7. verify `VERSION` is unchanged;
8. update this tracker and the issue completion comment before closing the issue.

| Order | Issue | Purpose | Dependencies | Status |
|---|---|---|---|---|
| 1 | [#45](https://github.com/vm75/message-sync/issues/45) | Provider-neutral canonical poll state | None | Complete |
| 2 | [#46](https://github.com/vm75/message-sync/issues/46) | Native Discord polls + vote ingestion | #45 | Complete |
| 3 | [#47](https://github.com/vm75/message-sync/issues/47) | Native Telegram polls + poll-state ingestion | #45 | Complete |
| 4 | [#48](https://github.com/vm75/message-sync/issues/48) | Aggregate-only live result companions | #45, #46, #47 | Complete |
| 5 | [#49](https://github.com/vm75/message-sync/issues/49) | Simplify poll router flow; retain `aggregate-response` | #48 | Pending |
| 6 | [#50](https://github.com/vm75/message-sync/issues/50) | Restart/concurrency/fallback/no-regression hardening | #45-#49 | Pending |
| 7 | [#51](https://github.com/vm75/message-sync/issues/51) | Final docs, regression/privacy/version audit, tracker removal | #45-#50 | Pending |

Use only these status values: `Pending`, `In Progress`, `Complete`, `Blocked`.

## Target design

The intended high-level data flow is:

```text
provider event
    |
    v
transport adapter / provider normalization
    |
    v
canonical router
    |
    +--> privacy-safe canonical poll state in sync.db
    |
    +--> existing delivery lane for poll/result operations
    |
    v
destination adapter
```

### Canonical poll option identity

Use canonical zero-based `option_index` as the provider-neutral option identity.

Provider-specific mapping remains at the boundary:
- WhatsApp decrypted SHA-256 option hash -> canonical option index;
- Discord poll `answer_id` -> canonical option index;
- Telegram option order/index -> canonical option index.

Do not make a provider-specific identifier the canonical option identity.

### Endpoint contribution models

Each endpoint contributes aggregate counts through exactly one authoritative model:

1. **Actor-backed selection state**
   - used where provider events are add/change/remove deltas;
   - actor identity is HMAC-derived;
   - selections are stored as canonical option indexes;
   - the aggregate is derived from current actor selections.

2. **Absolute endpoint snapshot**
   - used where the provider supplies current option voter counts;
   - store only canonical option index -> integer count for that endpoint;
   - do not additionally count actor state for the same endpoint.

Do not double count an endpoint.

### Live result companions

For every supported canonical poll:
- native poll remains the poll UI where representable;
- each member endpoint, including the source endpoint, gets at most one bridge-owned editable result companion;
- the companion remote message ID is privacy-safe lifecycle metadata;
- result content itself is not persisted.

Restart-safe example:

```text
📊 Live results across synced groups
Option 1 — 8
Option 2 — 5
Option 3 — 3
```

Requirements:
- aggregate counts only;
- no voter names;
- no HMAC IDs;
- no phone/JID/user IDs;
- no per-voter history;
- no claim that multi-select counts equal unique voters;
- if a provider's source contribution is not observable, show a concise partial/unavailable indicator rather than fabricating a zero.

The native poll next to the result companion carries the option labels, so the companion must remain understandable without persistent poll text.

### `aggregate-response`

Retain the current feature.

The trigger:
- remains configurable through `poll_aggregation_trigger`;
- must be an exact reply to a canonical poll;
- is not itself fanned out;
- continues to produce/fan an aggregate summary;
- uses the same authoritative canonical aggregate counts as automatic live results;
- may use transient option labels while available;
- may fall back to `Option N` after restart.

Do not keep a second independent aggregation definition solely for this command.

## Ticket notes

### #45 — Canonical poll state
Expected result:
- canonical option-index model;
- actor-backed and snapshot-backed store APIs;
- opaque provider poll references where required;
- current WhatsApp poll and `aggregate-response` behavior still works;
- no user-visible feature expansion yet.

### #46 — Discord native polls
Expected result:
- native poll ingress/outbound for representable cases;
- guild poll vote add/remove ingestion;
- answer-ID mapping reconstructed from the remote poll after cache loss;
- existing managed-webhook text/media sender rendering does not regress;
- deterministic fallback remains for unsupported polls.

### #47 — Telegram native polls
Expected result:
- native poll ingress/outbound via Bot API;
- persisted opaque `poll_id` correlation;
- bot-created poll absolute-state snapshots update the canonical endpoint counts;
- no Telegram voter identity needed for aggregate-only results;
- human-created source poll limitation explicitly retained without MTProto.

### #48 — Live aggregate companions
Expected result:
- one result companion per poll/endpoint;
- live automatic aggregate edits;
- source endpoint included;
- existing delivery/revision machinery reused;
- stale updates cannot overwrite newer totals;
- no duplicate companions on retry/replay.

### #49 — Simplification
Expected result:
- automatic and manual aggregation share store query/rendering helpers;
- `pollCache` is removed or strictly transient presentation-only;
- no transport-to-transport coupling;
- no new abstraction framework;
- `aggregate-response` externally behaves as before.

### #50 — Hardening
Expected result:
- restart/concurrency/replay/failure/tombstone coverage;
- cross-provider semantic matrix;
- deterministic fallback boundaries;
- full non-poll regression matrix remains green.

### #51 — Final documentation and cleanup
Expected result:
- `README.md`, `ARCHITECTURE.md`, and `docs/ASPIRATIONAL_FEATURES.md` accurately describe shipped behavior;
- `AGENTS.md` changes only if contributor invariants require it;
- `DOCKERHUB.md` changes only if deployment/configuration changed;
- privacy and no-regression audit complete;
- `VERSION` unchanged;
- this tracker deleted.

## Required test themes across the series

At minimum, the completed series must cover:
- WhatsApp-created poll -> native Discord + native Telegram when representable;
- Discord-created poll -> native WhatsApp + native Telegram when representable;
- Telegram-created poll -> native WhatsApp + native Discord when representable;
- WhatsApp vote/change/retract -> correct global aggregate;
- Discord add/remove/multiselect -> correct global aggregate;
- Telegram bot-created absolute poll snapshot -> correct global aggregate;
- simultaneous votes across endpoints;
- restart followed by a new vote/state update;
- result-edit retry without duplicate companion creation;
- stale revision suppression;
- unavailable endpoint isolation;
- tombstoned poll suppression;
- non-representable poll fallback without silent truncation;
- degraded human-created Telegram source poll behavior;
- no poll question/option text in `sync.db`;
- no raw voter identity in `sync.db` or retained logs;
- existing `aggregate-response`;
- all existing non-poll behavior.

## Per-ticket completion record

Agents should append short durable implementation notes here while the tracker exists. Keep notes concise; detailed long-term behavior belongs in authoritative docs before #51 completes.

### #45
- Status: Complete
- Implementation notes: Canonical poll state now uses zero-based option indexes; WhatsApp hashes resolve only at the store boundary. Actor selections, endpoint snapshots, explicit source-path exclusivity, and opaque provider references are persisted in fresh-schema tables. Router aggregation reads the shared index-based aggregate while retaining `aggregate-response` and transient labels.
- Tests: `make fmt`; `make test`; `make vet`; `git diff --check`; `git diff --exit-code VERSION`.

### #46
- Status: Complete
- Implementation notes: Discord guild poll intent and vote add/remove handlers are enabled. Native polls are normalized and representable outbound polls use the session REST client because DiscordGo webhook parameters lack poll support; ordinary text/media remains managed-webhook based. Answer IDs are mapped by fetched poll answer order, selections use HMAC actors plus canonical indexes, and unsupported limits fall back deterministically.
- Tests: `make fmt`; `make test`; `make vet`; `git diff --check`; `git diff --exit-code VERSION`.

### #47
- Status: Complete
- Implementation notes: Telegram representable polls now use Bot API `SendPoll`; ingress carries canonical option order and opaque `poll_id` metadata, while unsupported quiz/media/limits retain deterministic text fallback. Bot-created `poll` updates resolve persisted poll references and atomically replace endpoint snapshots without Telegram voter state. Human-created source polls retain the Bot API degraded-observability limitation; MTProto is not used.
- Tests: `make fmt`; `make test`; `make vet`; `git diff --check`; `git diff --exit-code VERSION`.

### #47
- Status: Pending
- Implementation notes:
- Tests:

### #48
- Status: Complete
- Implementation notes: Added one unique bridge-owned result companion per canonical poll/endpoint, including the source endpoint. Companion creation occurs after a durable poll copy and is idempotent; aggregate-only edits use existing endpoint lanes, delivery ledger, revision coalescing, and retry paths. Result text uses only `Option N` plus counts and is never stored; tombstoned polls are ignored and endpoint failures remain isolated.
- Tests: `make fmt`; `make test`; `make vet`; `git diff --check`; `git diff --exit-code VERSION`.

### #49
- Status: Pending
- Implementation notes:
- Tests:

### #49
- Status: Pending
- Implementation notes:
- Tests:

### #50
- Status: Pending
- Implementation notes:
- Tests:

### #51
- Status: Pending
- Documentation/audit notes:
- Final verification:

## Final tracker deletion rule

This file exists only to coordinate the issue series.

Issue #51 must:
1. verify #45-#50 are closed as completed;
2. update authoritative docs with all durable behavior, architecture, privacy rules, and provider limitations;
3. run the final regression/privacy/version checks;
4. delete this file;
5. not create a replacement permanent progress tracker;
6. close #51 only after the deletion is committed.

`VERSION` must remain unchanged for the entire series.
