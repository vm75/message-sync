# Must-Have Sync Reliability Tracker

Last updated: 2026-09-01  
Program status: **Ready to implement**  
Branch: `agent/sync-reliability`  
Plan: [MUST_HAVE_SYNC_RELIABILITY_IMPLEMENTATION_PLAN.md](MUST_HAVE_SYNC_RELIABILITY_IMPLEMENTATION_PLAN.md)  
Branch head when tracker was created: `4c4dc923c5da3c04f8802b9572b7374283f92a16`

## Next action

Start [#32 — Remove pre-release SQLite migration machinery](https://github.com/vm75/message-sync/issues/32). Do not start a dependent issue until every dependency in its row is closed and its documentation/tracker update is committed.

## Status legend

- **Open:** ready only when dependencies are closed.
- **In progress:** one agent is actively implementing on the program branch.
- **Blocked:** acceptance cannot proceed; the row must state one concrete next action.
- **Done:** acceptance passed, docs and tracker are committed, final issue comment exists, and GitHub issue is closed as completed.

Only one issue should be `In progress` on the shared branch unless the work is demonstrably file-independent and both agents have coordinated ownership. Prefer the dependency order over speculative parallel work.

## Issue tracker

| Phase | Issue | Status | Dependencies | Completion commit | Tests | Docs updated | Next action / blocker |
|---|---|---|---|---|---|---|---|
| Clean baseline | [#32 Remove pre-release SQLite migration machinery](https://github.com/vm75/message-sync/issues/32) | Done | — | `c65ec75` | `GOCACHE=/tmp/message-sync-go-cache go test ./internal/store`; `make fmt`; `GOCACHE=/tmp/message-sync-go-cache make test`; `GOCACHE=/tmp/message-sync-go-cache make vet`; `git diff --check`; `git diff --exit-code VERSION` — PASS | `ARCHITECTURE.md`, `internal/store/schema.sql`, `internal/store/store.go`, `internal/store/store_test.go`, `internal/app/app.go` | Next: #33; no follow-up |
| Clean baseline | [#33 Remove legacy groups API and naming compatibility](https://github.com/vm75/message-sync/issues/33) | Done | #32 | `0817a55` | `make fmt`; `GOCACHE=/tmp/message-sync-go-cache make test`; `GOCACHE=/tmp/message-sync-go-cache make vet`; `node --check internal/api/web/js/app.js`; `node --check internal/api/web/js/api.js`; `git diff --check`; `git diff --exit-code VERSION` — PASS | `README.md`, `ARCHITECTURE.md`, `internal/api`, `internal/config`, `internal/router`, `internal/app`, `internal/transport` | Next: #34 and #35; no follow-up |
| Failure contract | [#34 Add privacy-safe delivery failure classification](https://github.com/vm75/message-sync/issues/34) | Done | #32, #33 | `9a0a60a` | `make fmt`; `GOCACHE=/tmp/message-sync-go-cache make test`; `GOCACHE=/tmp/message-sync-go-cache make vet`; focused transport/router tests; `git diff --check`; `git diff --exit-code VERSION` — PASS | `ARCHITECTURE.md`, `internal/transport/failure.go`, `internal/transport/{discord,telegram,whatsapp}/failure.go` and tests, `internal/router/registry.go` | Next: #35; no follow-up |
| State | [#35 Add a content-free delivery ledger](https://github.com/vm75/message-sync/issues/35) | In progress | #32, #33; coordinate states with #34 | — | — | — | Starting from `11ab0bf`; add current-schema content-free delivery operation state and safe summaries |
| Isolation | [#36 Add independent ordered delivery lanes](https://github.com/vm75/message-sync/issues/36) | Open | #34, #35 | — | — | — | Wait for #34-#35 |
| Semantics | [#37 Add bounded retries and lifecycle ordering](https://github.com/vm75/message-sync/issues/37) | Open | #34-#36 | — | — | — | Wait for #34-#36 |
| Discord resilience | [#38 Make Discord managed webhooks self-healing](https://github.com/vm75/message-sync/issues/38) | Open | #34, #37 | — | — | — | Wait for #37 |
| Recovery foundation | [#39 Add common accepted-event checkpoints and recovery coordination](https://github.com/vm75/message-sync/issues/39) | Open | #35, #37 | — | — | — | Wait for #37 |
| Telegram recovery | [#40 Persist and recover Telegram update offsets](https://github.com/vm75/message-sync/issues/40) | Open | #39 | — | — | — | Wait for #39 |
| Discord recovery | [#41 Add bounded Discord channel history recovery](https://github.com/vm75/message-sync/issues/41) | Open | #38, #39 | — | — | — | Wait for #38-#39 |
| WhatsApp recovery | [#42 Reconcile WhatsApp HistorySync through recovery checkpoints](https://github.com/vm75/message-sync/issues/42) | Open | #39 | — | — | — | Wait for #39 |
| Operations | [#43 Expose privacy-safe delivery health in the admin UI](https://github.com/vm75/message-sync/issues/43) | Open | #35, #37, #39-#42 | — | — | — | Wait for recovery issues |
| Integration | [#44 Run end-to-end reliability and privacy hardening](https://github.com/vm75/message-sync/issues/44) | Open | #32-#43 | — | — | — | Final gate |

## Program invariants

These checks apply to every row, not only the final gate:

- [ ] Work is committed only to `agent/sync-reliability`.
- [ ] `VERSION` is unchanged.
- [ ] No SQLite schema version, historical `ALTER TABLE`, upgrade dispatcher, data conversion, or backward-compatibility branch is added.
- [ ] Generic `/api/groups` and the old sync-set `groups` payload do not return after #33.
- [ ] `sync.db` contains no message content, media, quoted text, poll text/options, display names, phone numbers, raw provider errors, or credentials.
- [ ] Logs/API contain safe operation labels and failure classes only.
- [ ] Recovered events use the same canonical router and delivery path as live events.
- [ ] Sync sets remain simple all-to-all endpoint sets.
- [ ] No broker, microservice, distributed lease, durable payload queue, generic rules engine, or unused configuration surface is introduced.
- [ ] Issue-specific tests plus `make fmt`, `make test`, and `make vet` pass.

## Per-issue start procedure

1. Confirm every dependency issue is closed as completed.
2. Pull the latest `agent/sync-reliability` head.
3. Read:
   - the issue body;
   - [the implementation plan](MUST_HAVE_SYNC_RELIABILITY_IMPLEMENTATION_PLAN.md);
   - this tracker;
   - `AGENTS.md`;
   - the relevant current architecture/transport/store files.
4. Change the row to **In progress**, add the agent/working note in “Next action / blocker,” and commit that tracker update before implementation.
5. Implement only the issue scope. If a prerequisite contract is wrong, update the plan and issue before expanding code.

## Per-issue completion procedure

The implementing agent must complete all steps in this order:

1. Run issue-specific tests, `make fmt`, `make test`, and `make vet`. Run focused `go test -race` when the issue changes lanes, timers, reconnect/recovery, config reload, or shared state.
2. Review the diff for:
   - content/PII/PHI/secret persistence;
   - raw provider error logging;
   - compatibility/migration code;
   - accidental `VERSION` changes;
   - scope not required by acceptance criteria.
3. Update permanent docs affected by the implementation.
4. Update the plan if actual interfaces, states, dependencies, limitations, or ownership differ.
5. Update the tracker row:
   - Status = **Done**
   - Completion commit = final implementation/docs commit SHA
   - Tests = exact commands and result, or a concise link to the final issue comment
   - Docs updated = exact filenames
   - Next action = next unblocked issue, or a linked deferred follow-up
6. Commit the docs/tracker update on `agent/sync-reliability`.
7. Add a final GitHub issue comment using the template below.
8. Close the issue as **completed**. If any acceptance criterion is incomplete, keep it open and mark the tracker **Blocked**.

## Final issue comment template

```markdown
Implemented on `agent/sync-reliability`.

### Summary
- <behavior delivered>
- <important design decision>

### Material files
- `path/to/file` — <reason>

### Verification
- `make fmt` — PASS
- `make test` — PASS
- `make vet` — PASS
- `go test -race ...` — PASS / not required because <reason>

### Privacy and scope review
- No message content/media/PII/PHI/secrets/raw provider errors persisted or logged.
- No migration/backward-compatibility code added.
- `VERSION` unchanged.
- KISS/YAGNI review: <what was deliberately not added>.

### Documentation
- Updated: <files>
- Tracker row updated: <commit SHA>

Completion commit: `<sha>`
```

## Decision log

| Date | Decision | Reason |
|---|---|---|
| 2026-09-01 | Use `agent/sync-reliability` from merged `main` | Isolate the complete must-have program from `main` |
| 2026-09-01 | Remove pre-release schema/API compatibility before reliability work | No deployments exist; compatibility would be permanent unused complexity |
| 2026-09-01 | Keep all-to-all sync sets | It satisfies the seamless cross-group use case; directed routes are not must-have |
| 2026-09-01 | Use bounded in-memory destination lanes plus a content-free ledger | Isolates failures without violating zero-content-at-rest |
| 2026-09-01 | Checkpoint accepted events, not received or delivered events | Prevents skipped work while allowing remote delivery to proceed asynchronously |
| 2026-09-01 | Use bounded provider history only | Avoids payload archives, unbounded crawls, and false recovery guarantees |
| 2026-09-01 | Do not change `VERSION` | Explicit program constraint; this is not a release |

## Completion gate

The program is complete only when:

- [ ] issues #32-#44 are closed as completed;
- [ ] every tracker row is **Done** with commit/tests/docs evidence;
- [ ] [#44](https://github.com/vm75/message-sync/issues/44) records the final branch head and residual provider limitations;
- [ ] permanent docs match the tested implementation;
- [ ] `git diff main...agent/sync-reliability -- VERSION` is empty;
- [ ] the branch is ready for owner review but has not been merged without a separate request.
