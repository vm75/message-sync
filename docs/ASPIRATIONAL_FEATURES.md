# Aspirational Feature Backlog

This document tracks aspirational features and capabilities from inspiration discussions and subsequent design explorations.

These items are **aspirational tracking items**, not firm commitments. Any proposed implementation must be evaluated against the project's **KISS/YAGNI** principles and **strict zero-PII/PHI privacy model**.

---

## 1. Features Already Implemented in `message-sync`

The following features have already been implemented in the core service:

- **Embedded Web Administration Console**: Zero-dependency dark-mode Web UI served on `:8080`.
- **REST Management & Status API**: Group management, sync set CRUD, QR pairing endpoints, runtime status, health check (`GET /health`), and bcrypt-authenticated admin sessions.
- **WhatsApp Native Polls & Aggregated Summaries**: Cross-group poll forwarding, vote tracking, and `aggregate-response` summary reporting.
- **Full Message Lifecycle**: Text, transient media (images, videos, audio/voice, documents, stickers), native clickable replies (with attribution fallback), reactions, edits, and deletes.
- **Privacy Core**: Ephemeral media streaming, transient push names, deterministic HMAC user pseudonyms, zero PII in SQLite `sync.db`.
- **Hardened Rootless Container**: Non-root UID `1000`, read-only rootfs, dropped capabilities, and multi-arch builds (`linux/amd64`, `linux/arm64`).

---

## 2. Aspirational Features Backlog

### A. Discord Transport Adapter
- **Description**: Bidirectional synchronization between WhatsApp and Discord (and future platforms).
- **Implementation status**: Endpoint configuration, privacy-bounded Discord gateway ingress, transport-aware dispatch, reusable webhook sender rendering, transient media forwarding, and reply/reaction/edit/delete lifecycle support are implemented. Discovery/UI, threads/forums, polls/richer format semantics, and directional routing remain staged work.
- **Tracked Capabilities**:
  - WhatsApp &harr; Discord synchronization.
  - Discord gateway bot ingress plus bridge-managed per-channel webhook rendering.
  - Channels, threads, and forum post mapping.
  - Discord replies, reactions, edits, and deletes mapped to canonical copies.
  - Directional/one-way bridge modes.
- **Architectural Requirement**: Discord remains a `transport.Adapter` at the canonical router boundary. Configured channel aliases are routing identity; Discord message IDs are remote-copy IDs only.

### B. Richer WhatsApp Message Formats
- **Description**: Support for specialized message types.
- **Tracked Capabilities**:
  - Contacts / vCards.
  - Location and live location coordinates.
  - WhatsApp Events.
  - Broad `@all` / `@everyone` participant mentions.
- **Privacy Considerations**: Contacts and locations carry PII. If implemented, they must follow strict sanitization or explicit opt-in handling.

### C. Multi-User Administration & RBAC
- **Description**: Multi-admin access control for the web console.
- **Tracked Capabilities**:
  - Separate administrator and operator accounts.
  - Role-based permissions (view-only vs configuration editing).
  - Invite codes and credential resets.
  - Administrative audit log.
- **Privacy Considerations**: Multi-user accounts and audit logs must reside in a separate subsystem to keep `sync.db` PII-free.

### D. Multiple WhatsApp Sessions / Accounts
- **Description**: Running multiple WhatsApp numbers/sessions within a single daemon instance.
- **Tracked Capabilities**:
  - Multi-session lifecycle manager for `whatsmeow` clients.
  - Per-account QR code pairing and health status.
  - Binding specific groups or sync sets to designated bridge numbers.

### E. Historical Chat Importer & Backfill
- **Description**: Tooling to import historical chat archives into a newly configured sync set.
- **Tracked Capabilities**:
  - WhatsApp exported `.zip` chat history parser.
  - Streaming historical replay with rate-limiting and progress tracking.
  - Media backfill tooling.
- **Operational Requirement**: Must stream and sanitize data without permanently storing exported chat bodies or media archives on disk.

### F. Dedicated Number & eSIM Provisioning
- **Description**: Automated provisioning of cellular numbers for bridge accounts.
- **Tracked Capabilities**:
  - Simbase (or alternative carrier) eSIM/number pool API integration.
  - Automated SIM activation and session assignment.

### G. External Email & SMS Notifications
- **Description**: Alerts and notifications sent outside WhatsApp.
- **Tracked Capabilities**:
  - System status and disconnect alerts via SMTP or SMS gateway (Twilio, AWS SNS, etc.).
  - Verification codes for administrative tasks.

### H. Membership Management & Verification Workflows
- **Description**: Automated onboarding, identity verification, and participant management.
- **Tracked Capabilities**:
  - Approval queues for group join requests.
  - External verification (e.g., OTP or email verification).
  - Automated participant add/remove/promote/demote rules.

### I. Identity Enrichment & Verification
- **Description**: Optional enrichment and authenticity verification of group participants.
- **Tracked Capabilities**:
  - LinkedIn profile or domain verification.
  - Document and identity proof verification.
  - Optional AI-assisted authenticity analysis.
- **Privacy Boundary**: Must remain completely isolated as an optional, external, consent-based subsystem.

### J. Human-Friendly Deterministic Pseudonyms
- **Description**: Generating user-friendly, memorable pseudonyms from HMAC IDs without storing real names.
- **Tracked Capabilities**:
  - Wordlist-based encoding (e.g., PGP word lists or adjective-noun pairs derived from the HMAC hash) to replace `u_xxxxxxxxxx` with names like `SwiftOtter` or `BlueFalcon`.

### K. Arbitrary Routing Topologies
- **Description**: Expanding beyond disjoint all-to-all sync sets.
- **Tracked Capabilities**:
  - Directional routing (e.g., Group A &rarr; Group B only).
  - Topic/content filtering rules.
  - Overlapping group sets.

### L. Optional Cloud Storage Backend
- **Description**: Alternative storage drivers for multi-instance or cloud-native deployments.
- **Tracked Capabilities**:
  - Cloud database adapter (e.g., Firestore / PostgreSQL).
  - External blob storage for transient media caching (e.g., GCS / S3) if media size exceeds memory limits.

---

## 3. Guiding Principles for Adoption

Before pulling any item from this backlog into implementation:

1. **KISS & YAGNI**: Avoid adding architectural complexity for hypothetical use cases.
2. **Privacy First**: Any feature introducing PII/PHI must be isolated in an explicit optional module, never mixed into core routing state (`sync.db`).
3. **Transport Neutrality**: New transports must adapt to the canonical message model rather than forcing custom schemas on other adapters.
