# Consolidated Follow-up Progress

Temporary execution tracker for the remaining post-dynamic-connections fixes, UX work, and optional friendly thread/topic presentation.

This file is intentionally temporary. **Issue #94 must delete it after every preceding tracked issue is complete and permanent documentation is updated.**

## Working rules

- Follow KISS/YAGNI and implement the smallest change that satisfies each issue.
- Do not update `VERSION`.
- The project is pre-production: do **not** add migrations, backward-compatibility paths, legacy fallbacks, backfills, dual reads/writes, or upgrade shims.
- Preserve the core hierarchy: **Connection authenticates -> Endpoint addresses a parent conversation -> ChildScope addresses Discord thread / Telegram topic context -> Sync Set routes between endpoint aliases.**
- Thread/topic names in friendly mode are presentation metadata only. Opaque ChildScope IDs remain authoritative for routing.
- Telegram broadcast channels remain unsupported.
- Update permanent docs as each user-visible behavior changes.
- After completing an issue: satisfy its acceptance criteria, run required tests, update this tracker row to `Complete`, add the issue completion summary, and close the GitHub issue.
- Do not delete this tracker early; only #94 removes it.

## Consolidated implementation order

| Order | Issue | Area | Dependencies / rationale | Status |
|---:|---|---|---|---|
| 1 | [#84](https://github.com/vm75/message-sync/issues/84) | Remove singleton/backward-compatibility fallbacks | Establish one clean explicit connection model before additional work. | Complete |
| 2 | [#83](https://github.com/vm75/message-sync/issues/83) | Dynamic Discord/Telegram credential replacement | Build on #84's final connection lifecycle; replacement must affect only the target connection. | Complete |
| 3 | [#85](https://github.com/vm75/message-sync/issues/85) | Telegram groups/supergroups-only endpoint validation | Establish final Telegram parent-target boundary before adding topic-name learning. | Complete |
| 4 | [#89](https://github.com/vm75/message-sync/issues/89) | Bug: Telegram reactions do not propagate | Correct lifecycle behavior before expanding presentation metadata. | Complete |
| 5 | [#88](https://github.com/vm75/message-sync/issues/88) | Bug: Telegram destination loses source group alias | Required by friendly `group[:context]/user` presentation. | Complete |
| 6 | [#90](https://github.com/vm75/message-sync/issues/90) | Friendly contexts 1/4: config + persisted label catalog | Depends on #84. Adds optional `opaque|friendly` mode without changing routing. | Pending |
| 7 | [#91](https://github.com/vm75/message-sync/issues/91) | Friendly contexts 2/4: learn Discord thread / Telegram topic names | Depends on #90 and #85. Persist names only in friendly mode. | Pending |
| 8 | [#92](https://github.com/vm75/message-sync/issues/92) | Friendly contexts 3/4: human-readable client notation | Depends on #88, #90, #91. Friendly mode removes `[contexts ...]` from client display and uses `group:context/user`. | Pending |
| 9 | [#93](https://github.com/vm75/message-sync/issues/93) | Friendly contexts 4/4: UI, docs, privacy disclosure, E2E tests | Finalize optional feature after #90-#92. | Pending |
| 10 | [#86](https://github.com/vm75/message-sync/issues/86) | Discord/Telegram online setup help | Do after Telegram/friendly-context behavior is stable to avoid duplicated documentation churn. | Pending |
| 11 | [#87](https://github.com/vm75/message-sync/issues/87) | WhatsApp one-shot Add Connection + QR pairing | Independent UX improvement; keep existing pairing backend/state machine. | Pending |
| 12 | [#94](https://github.com/vm75/message-sync/issues/94) | Final cleanup and tracker removal | Must run last, after every preceding row is Complete/closed. | Pending |

## Dependency graph

```text
#84
 +--> #83
 +--> #90

#85 ------> #91
#90 ------> #91

#88 ------> #92
#90 ------> #92
#91 ------> #92

#92 ------> #93

#84/#83/#85/#89/#88/#90/#91/#92/#93/#86/#87
                         |
                         v
                        #94
```

Issues #86, #87, and #89 are largely independent of the friendly-context chain, but the order above minimizes overlapping UI/docs changes and keeps defect fixes ahead of presentation enhancements.

## Friendly-context target behavior

### Opaque mode (default)

- Preserve current privacy-first context-token behavior.
- Do not persist Discord thread / Telegram topic names.

### Friendly mode (opt-in)

```text
Root message:
<group-alias>/<user>

Discord thread / Telegram topic:
<group-alias>:<thread-or-topic-name>/<user>
```

Examples:

```text
family/Alice
family:Dinner Plans/Alice
family:Travel/Alice
```

Rules:

- Persist one current label per `(endpoint, scope kind, opaque remote scope ID)`.
- Never put names into canonical identity or use them for routing.
- Renames affect future presentation only; historical delivered messages are not rewritten.
- Duplicate names are allowed; unique names within a parent are recommended only for human clarity.
- If a friendly name is unknown, display generic `thread` or `topic`; never expose raw provider IDs or opaque `s_...` tokens in friendly mode.
- Disabling friendly mode clears persisted child-scope names while preserving `canonical_scopes` and all routing history.
- Pre-existing Telegram topics may remain unnamed until a supported lifecycle event reveals their name; no MTProto/backfill/topic registry is required.

## Completion summaries

### #84 — Remove singleton/backward-compatibility fallbacks

- Removed production API transport fields and the fallback connection service.
- Removed implicit WhatsApp ownership, automatic singleton startup/registration, primary transport selection, and unscoped Telegram poll/recovery lookups.
- Routed membership administration strictly through the endpoint owner’s connection adapter.
- Added explicit connection-bound API test coverage and documented the invariant.
- Validation: `make fmt`, `make test`, `make vet`, and `git diff --check` passed.

### #83 — Dynamic Discord/Telegram credential replacement

- Track only an in-memory SHA-256 fingerprint of each active encrypted credential and nonce.
- On replacement, construct the new adapter and use `ConnectionManager.Restart` for only the changed connection; unchanged connections are not restarted.
- Preserve encrypted-at-rest storage and transient plaintext token handling.
- Documented replacement behavior and isolation guarantees.
- Focused validation passed; the full required gate is rerun before commit.

### #85 — Telegram groups/supergroups-only endpoint validation

- The existing endpoint boundary accepts only negative Telegram group/supergroup chat IDs.
- Discovery and ingress both reject private chats and broadcast channels by chat type; migration reuses the same validator.
- Permanent README and architecture documentation already describe Telegram parent endpoints as groups/supergroups only.
- Validation: existing configuration, API, discovery, normalizer, and migration tests pass under the full repository gate.

### #89 — Telegram reaction propagation

- Verified the existing shared reaction path resolves Telegram reactions by parent endpoint and message copy, preserving topic-agnostic canonical lookup, HMAC actor IDs, idempotent state updates, and bridge echo suppression.
- Added adapter-level regression coverage for Telegram reaction add, removal, replacement, endpoint/message targeting, and recovery checkpoint emission.
- Unsupported custom, paid, and multiple reactions remain safely rejected by the existing normalizer.
- Validation: the full required repository gate passes.

### #88 — Telegram destination source alias

- Telegram outbound text rendering now prefixes cross-endpoint sender labels with the configured source alias.
- The shared renderer is used by text, media captions, poll fallbacks, and reply fallback content, while same-endpoint/local presentation remains unchanged.
- Added permanent documentation and regression coverage for the exact `source/sender: content` form.
- Provider names/IDs remain transient and visible prefixes have no routing authority.

## Completion protocol

For every issue except #94:

1. Read this tracker and the issue in full.
2. Verify prerequisite rows are Complete.
3. Implement only that issue plus minimum required refactoring.
4. Update permanent docs for final user-visible behavior.
5. Run at minimum:
   - `make fmt`
   - `make test`
   - `make vet`
   - `git diff --check`
6. Run container/runtime checks when the issue changes runtime/config/deployment behavior.
7. Verify `VERSION` is unchanged.
8. Mark this row `Complete` only after acceptance criteria and tests pass.
9. Add a GitHub completion comment summarizing implementation, tests, privacy/security checks, and deferred items.
10. Close the issue.

For #94:

1. Verify every preceding row and GitHub issue is complete/closed.
2. Run the final regression/documentation review.
3. Delete this file.
4. Close #94 only after the tracker is gone and permanent docs are consistent.
