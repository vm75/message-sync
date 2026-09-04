# Membership Review V2 Progress

> Temporary implementation tracker for the `agent/membership-review-v2` branch.
>
> Delete this file in issue #100 after all implementation issues are complete and all durable behavior has been moved into permanent documentation.

## Goal

Refine the existing membership-verification subsystem around a small, human-authorized workflow:

- membership application configuration belongs to a **sync set**;
- existing work-email verification and deterministic checks remain;
- the current OpenRouter Document AI integration remains optional and advisory;
- a human reviewer makes the final approve/reject decision;
- uploaded evidence is purged after a final approve/reject decision;
- WhatsApp membership never uses server-side direct participant addition;
- approved WhatsApp applicants join through a secure invite and user-initiated pending join request that `message-sync` may approve after safe identity matching;
- high-volume/bulk membership infrastructure is not part of this work.

## Branch

`agent/membership-review-v2`

All implementation commits for this series must remain on this branch until the owner separately chooses to merge them.

## Non-negotiable decisions

1. **Per-sync-set configuration** — custom application fields, applicant instructions, evidence requirements, and reviewer guidance are shared by all membership pipelines whose target endpoints belong to the same sync set.
2. **Endpoint-bound fulfillment** — verification pipelines remain bound to an endpoint alias; the sync set is resolved from that endpoint rather than duplicated as an independently editable pipeline target.
3. **Human authority** — deterministic checks and Document AI are advisory only. No automatic approval/rejection or scoring policy engine.
4. **Current Document AI retained** — keep the existing OpenRouter analyzer/provider boundary. Do not add a new scraping/OCR/provider stack.
5. **Terminal evidence purge** — approve/reject purge local evidence. `needs_review` retains it.
6. **No WhatsApp direct add** — membership code must not call `ParticipantChangeAdd`, expose `AddParticipant`, or retain a feature-flag fallback to direct addition.
7. **Safe WhatsApp fulfillment** — human approval -> secure invite -> applicant requests join -> safe pending-request match -> approve pending request -> confirm membership.
8. **Join approval prerequisite** — the target WhatsApp group must require approval for invite-link joins; if it does not, surface an administrator action instead of bypassing the gate.
9. **No per-member invite rotation** — normal membership fulfillment must not rotate the group invite after each member.
10. **Low-volume only** — no bulk approval, durable membership queue, broker, Redis, worker pool, analytics subsystem, or configurable scheduler.
11. **Privacy boundaries stay intact** — applicant PII/custom answers/evidence remain in the explicit `control.db`/private evidence boundary and never enter `sync.db`, canonical routing, delivery state, or retained logs.
12. **No protocol-store queries for identity** — do not query per-connection WhatsApp protocol databases for membership matching.
13. **Pre-release schema discipline** — change current fresh schemas directly; do not add migrations, backfills, compatibility aliases, dual reads/writes, or upgrade shims for this series.
14. **No version change** — `VERSION` must remain byte-for-byte unchanged.
15. **Tracker is temporary** — issue #100 removes this file after permanent docs are final.

## Implementation order

Recommended dependency order:

```text
#95
  ↓
#96
  ↓
#97 ─────┐
         ├─> #99
#98 ─────┘
  ↓
#100 final integration/docs/privacy/tracker cleanup
```

#98 may proceed in parallel with #95-#97 when file ownership permits, but #99 must not start until both #97 and #98 are complete.

## Issue tracker

| Issue | Scope | Dependencies | Status | Commit / PR | Verification / notes |
|---|---|---|---|---|---|
| #95 | Per-sync-set membership application/review configuration and bounded custom-field schema | — | Complete | `bc4c434` + `f0cc723` | Fresh `control.db` schema; admin-only sync-set CRUD; bounded text/textarea/select/checkbox fields and select options; endpoint→sync-set resolution; deletion cleanup. `make test`, `make vet`, `git diff --check`, and unchanged `VERSION` pass. |
| #96 | Public dynamic form, strict submission validation, immutable request answer/definition snapshot, sync-set UI | #95 | Complete | `bc4c434` + `f0cc723` | Public metadata excludes reviewer/transport addressing; answers are strictly validated and snapshotted with the definition; reviewer API/UI reads snapshots; sync-set editor manages the shared definition. |
| #97 | Retain current OpenRouter Document AI, fix MIME propagation, purge evidence on final human decision | #96 | Complete | `bc4c434` | Existing OpenRouter boundary retained; validated MIME reaches analyzer; approve/reject unlink evidence immediately while needs-review retains it. Focused evidence test and full suite pass. |
| #98 | Remove WhatsApp direct add; add pending join-request/readiness admin primitives | — | Complete | `bc4c434` | Direct-add interface/provider call and per-member rotation removed; readiness, pending phone-JID listing, and targeted approval use pinned whatsmeow APIs; LID-only requests are not guessed. |
| #99 | Secure approved invite + pending WhatsApp join matching/approval + idempotent low-volume reconciliation | #97, #98 | Complete | `f0cc723` + `5797117` | Hashed 48-hour join tokens, verified-email delivery path, readiness gate, endpoint-owned matching, ambiguity/claim protection, explicit retry plus fixed one-minute bounded reconciliation; no durable queue. Full suite/container checks pass. |
| #100 | End-to-end hardening, privacy/stale-code audit, permanent docs, tracker deletion | #95-#99 | In progress | — | Final audit and issue closure/tracker deletion remain. |

## Per-issue completion protocol

For every issue before marking its row **Complete**:

1. Read the full issue body and current relevant code/tests before implementation.
2. Implement only that issue plus the smallest adjacent refactoring required by its acceptance criteria.
3. Preserve KISS/YAGNI and the existing control-plane/canonical-router separation.
4. Update permanent documentation whenever the issue makes a behavior or invariant authoritative/user-visible.
5. Run the exact tests required by the issue plus at minimum:

   ```sh
   make fmt
   make test
   make vet
   git diff --check
   git diff --exit-code VERSION
   ```

6. For runtime/container changes, also run where available:

   ```sh
   podman build -f Containerfile -t message-sync:dev .
   podman compose config
   ```

7. Record the implementation commit SHA, tests, privacy checks, known provider limitations, and any strictly in-scope follow-up in this tracker row.
8. Add a final GitHub issue comment with the same completion evidence.
9. Close the issue only when every acceptance criterion passes.

Do not use the tracker as a substitute for permanent documentation. Durable behavior belongs in `README.md`, `ARCHITECTURE.md`, `AGENTS.md`, testing/deployment docs, and `docs/FEATURE_COMPARISON.md` only where applicable.

## Final cleanup

Issue #100 is the only issue allowed to delete this file. Before deletion it must verify #95-#99 against code/tests, complete the regression/privacy/documentation audits, and ensure no durable behavior exists only in this tracker.
