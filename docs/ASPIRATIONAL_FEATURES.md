# Aspirational Feature Backlog

This document tracks aspirational features and capabilities from inspiration discussions and subsequent design explorations.

These items are **aspirational tracking items**, not firm commitments. Any proposed implementation must be evaluated against the project's **KISS/YAGNI** principles and **strict zero-PII/PHI privacy model**.

---

## Aspirational Features Backlog

### A. Discord Transport Extensions
- **Description**: Optional capabilities beyond the implemented bidirectional Discord transport.
- **Deferred Capabilities**:
  - Dynamic thread-specific endpoint creation.
  - Automatic outbound Discord forum-post creation.
  - Native Discord poll/vote-state bridging.
  - Directional/one-way bridge modes.
- **Architectural Requirement**: Any extension must keep Discord as a `transport.Adapter` at the canonical router boundary. Configured endpoint aliases remain routing identity; Discord message IDs remain remote-copy IDs only.

### B. Richer WhatsApp Message Formats
- **Description**: Support for specialized message types.
- **Deferred Capabilities**:
  - Contacts / vCards.
  - Location and live location coordinates.
  - WhatsApp Events.
  - Broad `@all` / `@everyone` participant mentions.
- **Privacy Considerations**: Contacts and locations carry PII. If implemented, they must follow strict sanitization or explicit opt-in handling.

### C. Multi-User Administration & RBAC
- **Description**: Multi-admin access control for the web console.
- **Deferred Capabilities**:
  - Separate administrator and operator accounts.
  - Role-based permissions (view-only vs configuration editing).
  - Invite codes and credential resets.
  - Administrative audit log.
- **Privacy Considerations**: Multi-user accounts and audit logs must reside in a separate subsystem to keep `sync.db` PII-free.

### D. Multiple WhatsApp Sessions / Accounts
- **Description**: Running multiple WhatsApp numbers/sessions within a single daemon instance.
- **Deferred Capabilities**:
  - Multi-session lifecycle manager for `whatsmeow` clients.
  - Per-account QR code pairing and health status.
  - Binding specific groups or sync sets to designated bridge numbers.

### E. Historical Chat Importer & Backfill
- **Description**: Tooling to import historical chat archives into a newly configured sync set.
- **Deferred Capabilities**:
  - WhatsApp exported `.zip` chat history parser.
  - Streaming historical replay with rate-limiting and progress tracking.
  - Media backfill tooling.
- **Operational Requirement**: Must stream and sanitize data without permanently storing exported chat bodies or media archives on disk.

### F. Dedicated Number & eSIM Provisioning
- **Description**: Automated provisioning of cellular numbers for bridge accounts.
- **Deferred Capabilities**:
  - Simbase (or alternative carrier) eSIM/number pool API integration.
  - Automated SIM activation and session assignment.

### G. External Email & SMS Notifications
- **Description**: Alerts and notifications sent outside WhatsApp.
- **Deferred Capabilities**:
  - System status and disconnect alerts via SMTP or SMS gateway (Twilio, AWS SNS, etc.).
  - Verification codes for administrative tasks.

### H. Membership Management & Verification Workflows
- **Description**: Automated onboarding, identity verification, and participant management.
- **Deferred Capabilities**:
  - Approval queues for group join requests.
  - External verification (e.g., OTP or email verification).
  - Automated participant add/remove/promote/demote rules.

### I. Identity Enrichment & Verification
- **Description**: Optional enrichment and authenticity verification of group participants.
- **Deferred Capabilities**:
  - LinkedIn profile or domain verification.
  - Document and identity proof verification.
  - Optional AI-assisted authenticity analysis.
- **Privacy Boundary**: Must remain completely isolated as an optional, external, consent-based subsystem.

### J. Human-Friendly Deterministic Pseudonyms
- **Description**: Generating user-friendly, memorable pseudonyms from HMAC IDs without storing real names.
- **Deferred Capabilities**:
  - Wordlist-based encoding (e.g., PGP word lists or adjective-noun pairs derived from the HMAC hash) to replace `u_xxxxxxxxxx` with names like `SwiftOtter` or `BlueFalcon`.

### K. Arbitrary Routing Topologies
- **Description**: Expanding beyond disjoint all-to-all sync sets.
- **Deferred Capabilities**:
  - Directional routing (e.g., Group A &rarr; Group B only).
  - Topic/content filtering rules.
  - Overlapping group sets.

### L. Optional Cloud Storage Backend
- **Description**: Alternative storage drivers for multi-instance or cloud-native deployments.
- **Deferred Capabilities**:
  - Cloud database adapter (e.g., Firestore / PostgreSQL).
  - External blob storage for transient media caching (e.g., GCS / S3) if media size exceeds memory limits.


### M. Telegram Transport Extensions
- **Description**: Optional Telegram capabilities beyond the implemented Bot API long-poll transport.
- **Deferred Capabilities**:
  - Webhook ingestion as an alternative deployment mode to long polling.
  - Optional Local Bot API server deployment for larger platform file limits.
  - Dynamically configured forum-topic endpoints or automatic outbound topic creation.
  - Native cross-platform Telegram poll/vote-state bridging rather than deterministic text fallback.
  - Telegram user-account/MTProto session support.
- **Architectural Requirement**: Any extension must preserve endpoint aliases as routing identity, keep Telegram platform IDs out of canonical identity, and retain the zero-PII/PHI persistence/logging boundary.

---

## Guiding Principles for Adoption

This backlog contains deferred capabilities only. The current MVP behavior and its reliability guarantees are documented in `README.md` and `ARCHITECTURE.md`; these items must not be added as part of routine reliability maintenance.

Before pulling any item from this backlog into implementation:

1. **KISS & YAGNI**: Avoid adding architectural complexity for hypothetical use cases.
2. **Privacy First**: Any feature introducing PII/PHI must be isolated in an explicit optional module, never mixed into core routing state (`sync.db`).
3. **Transport Neutrality**: New transports must adapt to the canonical message model rather than forcing custom schemas on other adapters.
