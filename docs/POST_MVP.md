# Post-MVP Backlog

This document captures functionality present in the previous `vm75/whatsappdiscordsync` implementation or discussed during redesign that is intentionally excluded from the `message-sync` MVP.

The list is a backlog, not a commitment to port every feature. Each item should be reconsidered against KISS/YAGNI and the privacy model before implementation.

## 1. Discord transport

Feasibility: high.

Deferred features:

- WhatsApp -> Discord and Discord -> WhatsApp synchronization;
- Discord webhooks;
- Discord gateway bot;
- channels, threads and forum posts;
- Discord replies mapped to canonical copies;
- Discord reactions, edits and deletes;
- Discord offline/backlog recovery;
- one-way/directional bridge modes.

Recommended design: implement Discord as another `transport.Adapter`; never restore Discord message IDs as canonical application IDs.

## 2. Richer WhatsApp message types

Feasibility varies by whatsmeow/protocol support.

Deferred:

- contacts/vCards;
- locations and live locations;
- polls and poll updates;
- cross-group aggregate poll results/votes;
- WhatsApp Events;
- broad `@all`/`@everyone` participant expansion;
- other newer interactive WhatsApp message formats.

Contacts/location are relatively straightforward but have additional PII implications. Polls require extra state and potentially encryption-secret/vote handling. New interactive formats may require low-level protobuf work.

## 3. Web administration and API

Feasibility: very high.

Deferred:

- local web dashboard;
- REST management/status API;
- bridge/sync-set CRUD;
- QR/session status UI;
- runtime health/storage views;
- config editing/validation UI.

A simple local/admin-only UI can preserve the privacy model if it does not introduce user accounts or identity data.

## 4. Multi-user administration

Feasibility: high, but changes privacy scope.

Deferred:

- administrator/sub-administrator accounts;
- email/password login;
- password reset;
- invite codes;
- ownership/sharing/roles;
- user audit trails.

If implemented, use a clearly separate PII subsystem/storage policy rather than weakening `sync.db` invariants.

## 5. Membership workflows

Feasibility: high with whatsmeow group APIs, but PII-heavy.

Deferred:

- known-member database;
- names/designations;
- WhatsApp/Discord membership requests;
- approval queues;
- WhatsApp OTP verification;
- email verification;
- automatic add/remove/promote/demote participants;
- group invite-link workflows.

The old known-member exception to anonymization should not return unchanged because it persists real identity.

## 6. Multiple WhatsApp accounts/sessions

Feasibility: high.

Deferred:

- session manager for several whatsmeow clients;
- per-account QR lifecycle/health;
- routing groups through different linked accounts;
- dedicated bridge numbers.

This adds operational/session complexity but fits the canonical architecture.

## 7. Dedicated number/eSIM provisioning

Feasibility: code high, operational medium.

Deferred:

- Simbase integration;
- eSIM/number pool;
- provisioning state;
- assigning a session/number to a sync set.

Provider API work is straightforward; WhatsApp account provisioning/reliability is the harder part.

## 8. Email and SMS

Feasibility: very high.

Deferred:

- email notifications;
- SMS notifications;
- verification codes;
- provider fallbacks.

These introduce external processors and recipient identifiers, so they belong outside the PII-free core.

## 9. Identity/enrichment workflows

Feasibility: provider-dependent; privacy impact high.

Deferred:

- LinkedIn profile/company enrichment;
- identity matching;
- uploaded verification evidence;
- image/document authenticity analysis;
- AI-based evidence analysis.

These should be optional modules with explicit retention, access-control and external-processing policies.

## 10. Historical import/bootstrap

Feasibility: high.

Deferred:

- WhatsApp exported ZIP parsing;
- historical replay;
- progress/cancellation;
- historical media import;
- backfill/cleanup tooling.

A future importer should stream/process files and avoid retaining exported archives. Sending large histories into live groups requires throttling and explicit boundaries.

## 11. Cloud persistence/storage

Feasibility: high, not currently justified.

Deferred:

- Firestore application state;
- Firestore WhatsApp auth store;
- GCS attachment storage;
- large-media GCS fallback.

SQLite is preferred for one self-hosted instance. Reconsider managed storage only if multi-instance/shared-state requirements appear.

## 12. Cloud deployment automation

Feasibility: high.

Deferred:

- GCP Cloud Build configuration;
- GCE-specific bootstrap/deployment;
- embedded Caddy/web-console deployment;
- provider-specific infrastructure scripts.

Keep the container vendor-neutral; provider deployment recipes can live separately later.

## 13. Audit/event history

Feasibility: high, privacy-sensitive.

The old implementation had audit functionality. A future core audit stream must be metadata-only and avoid identity/content. Any human-readable user audit trail should be treated as a separate privacy-scoped subsystem.

## 14. Human-friendly persistent pseudonyms

Technically easy but the old JID -> random name mapping persisted raw identifiers. Do not restore that design.

Possible future alternatives:

- deterministic HMAC-based friendly-word encoding;
- operator-managed opt-in labels stored separately;
- ephemeral push-name display only.

## 15. Arbitrary routing graphs

MVP uses disjoint all-to-all sync sets. Future routing could support one-way edges, filters, multiple overlapping destinations and per-message-type policies. Implement only after a concrete need because graph routing substantially complicates loop prevention and configuration validation.

## Suggested sequencing after MVP

1. lightweight status/admin UI;
2. directional/custom routing;
3. contacts/location and `@all` if needed;
4. Discord adapter;
5. multiple WhatsApp sessions;
6. historical importer;
7. polls/richer WhatsApp formats;
8. optional membership/identity/notification subsystem;
9. cloud/multi-instance storage only if operational requirements demand it.
