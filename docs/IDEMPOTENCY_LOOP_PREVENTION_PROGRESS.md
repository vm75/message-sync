# Idempotency & Loop Prevention Progress

Temporary implementation tracker for post-reliability idempotency, recovery-checkpoint, and WhatsApp lifecycle-loop hardening.

This workstream corrects gaps found after the completed sync-reliability program. It does **not** replace the canonical/message-copy architecture and does not reopen the old reliability tracker.

This file exists only while issues #63-#66 are being implemented. **Delete this file as part of #66 after every preceding issue is Complete and permanent documentation has been updated.** Do not retain an archived/completed copy.

## Working branch

All implementation work for this tracker must happen on:

`agent/idempotency-hardening`

Do not implement these issues directly on `main`. Issue/tracker maintenance may be committed separately as normal repository planning metadata.

## Review baseline

The findings that created this workstream were verified against `main` at:

`277465f75c2c50bb613ea1aecc6d6365a04636f9`

The implementation must re-read current code before changing it and must adapt to any intervening main-branch changes rather than assuming this SHA is still current.

## Non-negotiable constraints

- Follow `AGENTS.md`, KISS, and YAGNI.
- Do not update `VERSION`.
- The project is pre-production: do not add schema migration frameworks, backward-compatibility paths, dual-read/dual-write behavior, upgrade shims, or legacy database conversion.
- Modify the fresh current schema directly when a content-free state change is required.
- `/data/sync.db` and application logs remain PII/PHI-free and message-content-free.
- Do not add a durable message payload queue, broker, external database, or paid service.
- Canonical IDs and `message_copies` remain the authoritative cross-transport identity/completed-copy model.
- Do not port `whatsappdiscordsync` architecture wholesale. It is a behavioral reference for loop handling only.
- Provider limitations must be documented honestly. Do not claim exactly-once remote create semantics where a provider cannot guarantee them.
- Every completed issue must update relevant permanent docs per `AGENTS.md`.
- After completing an issue, update its Status below to **Complete** before closing the GitHub issue.
- Only issue #66 may delete this tracker.

## Implementation order

| Order | Issue | Status | Depends on | Purpose |
|---:|---|---|---|---|
| 1 | [#63 Idempotency: make outbound create retries ambiguity-safe and companion sends single-shot](https://github.com/vm75/message-sync/issues/63) | Complete | — | Prevent blind duplicate creates after ambiguous sends and prevent duplicate multi-send companions. |
| 2 | [#64 Recovery: do not advance checkpoints past payload-dependent pending delivery](https://github.com/vm75/message-sync/issues/64) | Pending | #63 | Align source checkpoint advancement with the point where transient payload is genuinely safe to forget. |
| 3 | [#65 Loop prevention: suppress WhatsApp bridge-generated edit/delete lifecycle echoes](https://github.com/vm75/message-sync/issues/65) | Pending | #64 | Terminate bridge-generated WhatsApp lifecycle echoes without suppressing genuine linked-device actions. |
| 4 | [#66 Integration: verify idempotency/recovery hardening and remove temporary tracker](https://github.com/vm75/message-sync/issues/66) | Pending | #63-#65 all Complete | Verify the fixes together, update permanent docs, and delete this tracker. |

## Why this is a separate workstream

The completed reliability work already established:

- content-free delivery ledger;
- independent ordered delivery lanes;
- bounded retries;
- recovery checkpoints;
- Discord/Telegram/WhatsApp recovery paths;
- canonical message-copy deduplication.

These issues are corrective hardening of specific edge cases found in that implementation:

1. remote create may succeed before its `message_copy` is persisted;
2. a multi-send compatibility step may be repeated by a later retry;
3. a source cursor can currently become durable before asynchronous payload-dependent delivery is actually safe to forget;
4. WhatsApp edit/delete echoes do not have the same explicit suppression discipline as reactions.

Do not redesign unrelated reliability code merely because this tracker exists.

## Cross-workstream coordination with multi-user/membership verification

The multi-user and membership-verification feature can proceed independently through issues #52-#60. Those features live in the control plane and should not be coupled to router internals.

However, the membership integration gate #61 must validate against the corrected routing/recovery behavior. Therefore:

- #52-#60 may proceed in parallel with #63-#66;
- #59 must continue using narrow transport-administration interfaces and must not depend on the canonical router;
- #61 should not be marked Complete until #66 is Complete/merged into its implementation baseline;
- #62 remains transitively dependent through #61 and does not delete this tracker—#66 owns this tracker's cleanup.

## Per-issue completion checklist

For each issue before marking Complete:

1. Read the full GitHub issue and verify its prerequisites are Complete.
2. Rebase/refresh `agent/idempotency-hardening` from the intended baseline according to repository workflow before implementation.
3. Implement only the issue scope plus the minimum supporting refactor required by its acceptance criteria.
4. Add focused deterministic tests, including privacy and race/concurrency tests where applicable.
5. Run:
   - `make fmt`
   - `make test`
   - `make vet`
6. Run focused `go test -race` for changed concurrency-sensitive packages.
7. For #66, also run binary and fresh-volume container smoke tests.
8. Confirm `VERSION` is unchanged.
9. Update relevant permanent documentation required by `AGENTS.md`.
10. Change this issue's Status in the table to **Complete**.
11. Add a final GitHub issue comment with files changed, exact tests/results, privacy review, provider limitations, and commit SHA.
12. Close the issue only after code, docs, tracker, and acceptance criteria agree.

## Final cleanup

Issue #66 must:

- independently verify #63-#65 rather than trusting tracker status;
- update permanent architecture/testing/agent guidance for the final semantics;
- verify no durable payload/content/PII persistence was introduced;
- verify `VERSION` is unchanged;
- delete this tracker;
- remove stale references to it;
- leave any genuinely deferred provider-exactly-once limitations documented as limitations/aspirational work rather than pretending they were solved.
