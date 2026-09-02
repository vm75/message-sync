# Multi-User & Membership Verification Progress

Temporary implementation tracker for multi-user Web UI support and membership verification.

This file exists only while issues #52-#62 are being implemented. **Delete this file as part of #62 after every preceding issue is Complete and permanent documentation has been updated.** Do not retain an archived/completed copy.

## Non-negotiable constraints

- Follow `AGENTS.md`, KISS, and YAGNI.
- Do not update `VERSION`.
- The project is pre-production: do not add database migration frameworks, backward-compatibility paths, dual-read/dual-write behavior, or legacy upgrade code. Change fresh schemas directly.
- `/data/sync.db` and normal application logs remain PII/PHI-free.
- Multi-user account data and membership-verification PII live only in the explicit control-plane subsystem (`/data/control.db` and the dedicated private evidence path under `/data`).
- Do not query `whatsapp.db` for application features.
- Membership verification is a control-plane feature. Do not put it in the canonical message router or message-copy schema.
- Email and AI integrations must remain optional from the perspective of core message synchronization; no paid service is mandatory for the daemon to start or synchronize messages.
- OpenRouter analysis is advisory only. Human approval is authoritative.
- Every completed issue must update relevant permanent docs per `AGENTS.md`.
- After completing an issue, update its Status below to **Complete** before closing the GitHub issue.
- Only issue #62 may delete this tracker.

## Implementation order

| Order | Issue | Status | Depends on | Purpose |
|---:|---|---|---|---|
| 1 | [#52 Control plane: add isolated PII-sensitive control.db](https://github.com/vm75/message-sync/issues/52) | Complete | — | Establish the separate sensitive persistence boundary used by accounts and verification. |
| 2 | [#53 Auth: replace shared admin password with multi-user accounts and server-side sessions](https://github.com/vm75/message-sync/issues/53) | Complete | #52 | Replace the single shared password with per-user authentication and revocable sessions. |
| 3 | [#54 RBAC: add admin/operator lifecycle, invites, deactivation, and control-plane audit](https://github.com/vm75/message-sync/issues/54) | Complete | #53 | Add the minimal two-role authorization model, user lifecycle, and structured audit events. |
| 4 | [#55 Web UI: add multi-user login, account management, and role-aware navigation](https://github.com/vm75/message-sync/issues/55) | Pending | #53, #54 | Make the embedded Web UI usable with multi-user auth and RBAC. |
| 5 | [#56 Membership: add verification pipelines and public application intake](https://github.com/vm75/message-sync/issues/56) | Pending | #52, #54 | Add verification pipeline configuration, public intake, and private evidence storage. |
| 6 | [#57 Membership: add work-email verification and deterministic corroboration](https://github.com/vm75/message-sync/issues/57) | Pending | #56 | Verify work-email ownership without SMS and add local advisory checks. |
| 7 | [#58 Membership: add optional OpenRouter free-model advisory analysis](https://github.com/vm75/message-sync/issues/58) | Pending | #56, #57 | Add slow/optional free-model analysis without making AI a gate. |
| 8 | [#59 Membership: add WhatsApp/Discord approval fulfillment and secure invite fallback](https://github.com/vm75/message-sync/issues/59) | Pending | #57 | Perform approved membership actions through narrow transport-admin interfaces. |
| 9 | [#60 Membership UI: add verification queue, evidence review, and decision workflow](https://github.com/vm75/message-sync/issues/60) | Pending | #54, #56, #57, #58, #59 | Add role-authorized review, approval/rejection, evidence access, and fulfillment retry UI/API. |
| 10 | [#61 Integration: harden privacy, retention, container behavior, and end-to-end RBAC/verification](https://github.com/vm75/message-sync/issues/61) | Pending | #52-#60, #66 | Validate the complete workflow against the corrected idempotency/recovery baseline, privacy canaries, failure isolation, retention, and rootless container behavior. |
| 11 | [#62 Docs: finalize multi-user/membership verification and remove temporary progress tracker](https://github.com/vm75/message-sync/issues/62) | Pending | #52-#61 all Complete | Update permanent docs/aspirational backlog and delete this tracker. |

## Dependency notes

- #52 is the architectural foundation and should land first.
- #53 and #54 establish the authenticated principal and authorization rules that all later admin APIs must use.
- #55 can be implemented before membership UI work; it should not invent membership-specific permissions.
- #56 starts the membership domain but intentionally stops before email delivery, AI, or platform mutations.
- #57 is the minimum verification gate. AI in #58 is optional and must not be required for #59 or manual review.
- #59 must extend narrow WhatsApp/Discord administration capabilities rather than the canonical message transport interface.
- #60 composes the already-built backend capabilities; avoid reimplementing business logic in browser JavaScript.
- #52-#60 may proceed in parallel with the separate idempotency/loop-prevention workstream (#63-#66); membership control-plane implementation must not be coupled to router internals.
- #61 is the release-readiness/hardening gate for this feature set and must use a baseline where #66 is Complete, so its routing-independence checks cover the corrected create/recovery/WhatsApp lifecycle semantics.
- #62 is documentation-only/final cleanup and remains transitively dependent on #66 through #61. It does not own or delete the idempotency tracker. It must verify all prior rows are Complete before deleting this file.

## Per-issue completion checklist

For each issue before marking Complete:

1. Read the full GitHub issue and verify its prerequisites are Complete.
2. Implement the issue scope and no more than the minimum required supporting refactor.
3. Add/adjust tests for its acceptance criteria, including privacy and authorization cases where applicable.
4. Run:
   - `make fmt`
   - `make test`
   - `make vet`
5. For container/runtime changes also run:
   - `podman build -f Containerfile -t message-sync:dev .`
   - `podman compose config`
6. Confirm `VERSION` is unchanged.
7. Update relevant permanent documentation required by `AGENTS.md`.
8. Change this issue's Status in the table to **Complete**.
9. Close the GitHub issue only after the repository, docs, tracker, and acceptance criteria agree.

## Final cleanup

Issue #62 must:

- verify #52-#61 are all Complete;
- update `README.md`, `ARCHITECTURE.md`, `AGENTS.md`, `DOCKERHUB.md`, and `docs/ASPIRATIONAL_FEATURES.md` as applicable to the final implementation;
- remove implemented items from the aspirational backlog while retaining genuinely deferred functionality;
- delete this tracker;
- remove stale references to it;
- leave `VERSION` unchanged.
