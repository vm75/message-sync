# MVP Implementation Plan

This plan defines the implementation sequence for `message-sync`. Each phase should be independently testable and should not pull future-phase complexity forward.

## MVP definition of done

The MVP is complete when a rootless Podman deployment can pair one WhatsApp account, synchronize configured groups, forward common media, preserve canonical reply/reaction/edit/delete relationships, survive normal restarts, recover a bounded recent backlog, and prove that application persistence/logs contain no PII/PHI.

No `VERSION` file exists until these criteria pass.

## Phase 0 — Foundation, privacy boundaries, SQLite application store

### Deliverables

- Go package layout and commands.
- JSON config loading/defaults/validation.
- HMAC identity helper using `IDENTITY_SECRET`.
- `sync.db` migration/repository implementation based on `internal/store/schema.sql`.
- structured PII-safe logging conventions.
- rootless `Containerfile`, `.containerignore`, `compose.yml`.
- unit tests for config, identity and SQLite constraints.

### Acceptance

- `go test ./...` and `go vet ./...` pass;
- `podman build` and `podman compose config` pass;
- representative `sync.db` contains no raw JID/name/message content;
- no `VERSION` file exists.

## Phase 1 — whatsmeow session lifecycle and event normalization

### Deliverables

- pin a tested `go.mau.fi/whatsmeow` commit/version;
- open `/data/whatsapp.db` through whatsmeow SQL store;
- QR pairing for first login;
- persistent reconnect without QR after normal restart;
- explicitly disable decrypted event buffer and retry message store;
- normalize group events into safe internal event types;
- ignore DMs and unconfigured groups in MVP;
- classify/log errors without identifiers/content.

### Acceptance

Pair once, restart container, reconnect successfully and receive configured-group events with no application PII persistence/logging.

## Phase 2 — Text mesh, aliases, attribution and idempotent fan-out

### Deliverables

- ordered ingress/router worker;
- all-to-all text fan-out within each sync set;
- `<group-alias>/<username>` attribution;
- push-name transient and HMAC-only modes;
- canonical message + copy persistence;
- persistent duplicate/loop prevention plus small in-memory acceleration cache;
- partial fan-out retry semantics.

### Acceptance

Two-/three-group tests demonstrate no echo loops or duplicate destination copies, including replay after simulated crash between destination sends.

## Phase 3 — Common media forwarding

### Deliverables

- images/videos/documents with captions;
- audio/voice notes;
- stickers;
- bounded in-memory/streaming download and resend;
- configurable max size;
- attribution companion message for formats without captions;
- cleanup paths for failure/cancellation.

### Acceptance

All supported media types fan out successfully; oversized media fails safely; no media bytes/files remain in application DB or persistent filesystem.

## Phase 4 — Replies and reactions

### Deliverables

- quoted remote ID -> canonical lookup;
- destination canonical -> destination copy lookup;
- native quote when enough metadata exists;
- textual provenance fallback otherwise;
- reaction add/change/remove;
- HMAC actor reaction state;
- loop prevention for bridge-origin reactions.

### Acceptance

Replies/reactions remain coherent across a process restart without storing raw participant identity or message content.

## Phase 5 — Edits, deletes and bounded offline recovery

### Deliverables

- incoming whatsmeow edit detection;
- edit propagation to known copies;
- revoke/delete propagation;
- tombstone state preventing deleted-message resurrection;
- offline/history events through normal router;
- recovery max-age and max-count limits;
- recovery cursors;
- partial fan-out completion after restart.

### Acceptance

Edits/deletes/recovery are idempotent across redelivery and restart, and deleted messages are not resurrected by recovery.

## Phase 6 — Hardening and release readiness

### Deliverables

- configurable/default 90-day mapping retention;
- graceful shutdown and DB close/checkpoint;
- bounded queues, operation timeouts and reconnect/backoff policy;
- storage-size observability without identity data;
- privacy/security review of logs and both database boundaries;
- rootless Podman validation on supported architectures;
- amd64/arm64 release build validation;
- README/ARCHITECTURE/AGENTS reconciliation;
- Docker Hub + GHCR secret/workflow validation.

### Release acceptance

Only after all previous phases pass:

1. merge/complete MVP on `main`;
2. create `VERSION` with the first semantic version (expected `0.1.0`);
3. push that VERSION change;
4. verify Docker Hub and GHCR multi-architecture publication.

## Cross-phase engineering rules

- Never store message content to make testing/recovery easier.
- Never add raw participant identity to `sync.db`.
- Treat `whatsapp.db` as a sensitive protocol vault and keep it separate.
- Reuse one canonical event pipeline for live/recovered messages.
- Persist successful fan-out copies immediately.
- Prefer deterministic sequential behavior before concurrency.
- Add user-facing behavior only with tests and corresponding docs updates.
