# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.7] - 2026-09-08

### Changed
- Marked the Membership Verification feature as experimental across settings and documentation.

## [0.1.6] - 2026-09-08

### Added
- Encrypted poll question and option presentation storage in `control.db`, allowing aggregate results to retain their labels across restarts without adding message content to `sync.db`.
- Source-labelled native poll presentation for cross-endpoint Discord and Telegram polls.
- Live updates for on-demand aggregate responses after subsequent poll votes or provider snapshots.

### Changed
- Telegram native polls are non-anonymous so their vote updates can contribute to aggregate synchronization; the bridge still stores and forwards aggregate state only.

## [0.2.0] - 2026-09-02

### Added
- **Multi-Transport Expansion**:
  - Discord transport adapter with gateway connection, live channel discovery, and webhook message forwarding.
  - Telegram transport adapter with Bot API long polling, observed group/supergroup discovery, and message routing.
- **Sensitive Control-Plane & Multi-User Support**:
  - Isolated control-plane database (`control.db`) with RBAC supporting Admin and Operator roles.
  - User management with one-time invite tokens, password reset flows, session revocation, and audit logging.
  - Experimental membership verification review workflow for transport endpoints.
- **Endpoint Alias Management**:
  - Atomic endpoint alias renaming via `PUT /api/endpoints/{alias}`, automatically migrating historical message copy and reaction references.
  - Inline endpoint chip alias renaming and alias pre-filling in the sync-set editor.
- **WhatsApp Community Integration**:
  - Filtered group discovery excluding root community containers and admin announcement groups.
  - Formatted subgroup labeling prefixed with parent community name (`<Community>:<Subgroup>`).

### Changed
- **Web UI & Console Cleanups**:
  - Cleaned verbose guidance subtitles from settings and dashboard views.
  - Fixed platform status cards resting state to default to "Not Configured" / "Unlinked" instead of getting stuck on "Checking…".
  - Automatic selection of the first sync set upon opening the Sync Sets view.
  - Fixed endpoint removal from sync sets to completely release the group/channel from greyed-out dropdown states.
  - Added dedicated navigation links between the Sign In and Redeem Invite screens with token query autofill.

## [0.1.0] - 2026-08-26

### Added
- **Privacy-First Core Architecture**:
  - Transport-agnostic canonical message router and SQLite database (`sync.db`) free of all PII/PHI (no phone numbers, raw JIDs, push names, message bodies, or media stored in application persistence or logs).
  - HMAC-SHA256 derived actor identifiers with `IDENTITY_SECRET`.
  - Protocol state isolated strictly in `/data/whatsapp.db`.
- **WhatsApp Group Synchronization**:
  - All-to-all bidirectional routing across configured sync-set groups.
  - Multi-kind media forwarding (images, videos, audio/voice notes, documents, stickers) directly through memory buffers without on-disk retention.
  - Text messaging with formatted bold-italic provenance attribution (`*_<alias>/<username>_*: <text>`).
  - Native replies mapping with fallback text attribution.
  - Native reactions propagation (add, change, remove) with separate actor identity mapping.
  - Message edit and deletion/revoke propagation with canonical tombstones.
  - Native WhatsApp poll creation synchronization and cross-group poll vote tracking.
  - Cross-group poll vote aggregation summaries via `aggregate-response` trigger.
  - @Mention normalization with contact name resolution (PushName / FullName / BusinessName / phone number fallback).
- **Automated WhatsApp Chat Cleanup**:
  - Scheduled AppState `ClearChatAction` mutation runner to automatically clear old messages on the sync account for sync-set groups.
  - Configurable retention duration (`whatsapp_chat_retention_days`, default 30 days) and toggle (`whatsapp_chat_cleanup_enabled`).
- **Management Console & REST API**:
  - Embedded zero-dependency Web UI console served on configured port.
  - Admin password setup, bcrypt hash storage, HMAC session cookies, and password change endpoint (`POST /api/auth/change-password`).
  - WhatsApp session status (`GET /api/whatsapp/status`), QR code pairing (`POST /api/whatsapp/pair`), pairing cancellation (`DELETE /api/whatsapp/pair`), and session logout/unlinking (`POST /api/whatsapp/logout`).
  - Ephemeral in-memory WhatsApp group discovery (`GET /api/whatsapp/groups`).
  - REST CRUD endpoints for groups (`/api/groups`) and sync sets (`/api/sync-sets`).
  - Global configuration endpoints (`/api/config`).
- **Container & Deployment**:
  - Non-root, rootless Podman / Docker support with read-only root filesystems and `/data` volume.
  - Multi-architecture container images (`linux/amd64`, `linux/arm64`) for Docker Hub and GitHub Packages (GHCR).
  - Automated release workflow triggered solely on `VERSION` modifications on `main`.
