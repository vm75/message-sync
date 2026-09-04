# Feature Comparison

This document is the single repository reference for the project that informed the original design exploration.

**Upstream inspiration:** https://github.com/imvibecoder7/whatsappdiscordsync  
**Comparison snapshot:** 2026-09-04

The comparison is behavioral and architectural. `message-sync` is not intended to remain source-compatible with the upstream project, and feature parity is not a goal by itself. Capabilities are carried forward only when they fit the current privacy, reliability, self-hosting, KISS, and YAGNI constraints.

## Executive Summary

`message-sync` started from the same practical problem—keeping conversations synchronized across messaging systems—but now follows a substantially different design:

- it uses a transport-neutral canonical router instead of bridge-specific mappings as the central abstraction;
- it supports WhatsApp, Discord, and Telegram in mixed all-to-all sync sets;
- it treats message identity, copies, lifecycle events, retries, and recovery as persistent transport-neutral state;
- it keeps the core routing database and application logs free of PII/PHI;
- it isolates multi-user administration and membership verification into a separate sensitive control plane;
- it requires no cloud database, cloud object storage, paid AI service, paid email service, or managed hosting for normal operation;
- it is packaged as a small rootless container with a read-only root filesystem and local SQLite persistence.

The upstream design remains useful as a product-behavior reference, especially for webhook sender presentation, live aggregate poll results, multi-user administration, membership workflows, bridge management, and operator-facing web flows. `message-sync` deliberately reimplements those ideas behind stricter boundaries rather than copying the upstream architecture.

## Comparison Matrix

| Area | `message-sync` | Upstream inspiration | Relationship / rationale |
|---|---|---|---|
| Primary architecture | Go daemon with transport adapters, canonical events, canonical IDs, message copies, routing store, and per-destination delivery lanes | Node.js application centered on bridge mappings and platform-specific handlers | Reworked. The canonical router prevents WhatsApp or Discord from becoming the global data model. |
| Runtime | Single Go binary | Node.js application | Replaced for simpler deployment and lower runtime surface. |
| Core persistence | Local SQLite: `sync.db`, `control.db`, plus isolated per-connection `/data/whatsapp/<connection-id>.db` | Local files with optional Firestore-backed application state; uploaded files may use local storage or GCS | Reworked to remain self-contained and container-friendly. |
| WhatsApp integration | `whatsmeow` linked-device client with dynamic multi-account connections | Baileys linked-device client | Same product role, different library and state model; supports multiple WhatsApp accounts per daemon. |
| Discord integration | Dynamic multi-instance Gateway bots for ingress/discovery plus bridge-managed webhooks for sender presentation | Discord bot plus per-mapping webhook delivery | Generalized behind a transport adapter with dynamic connection lifecycle and encrypted credentials. |
| Telegram integration | Dynamic multi-instance Bot API long polling with namespaced cursors | MTProto user-session integration | Intentional difference. Bot API avoids a second user-account session and keeps deployment simpler, but Telegram bot messages cannot use a different sender name per message, so attribution remains in the body. |
| Routing model | Configured endpoint aliases belong to all-to-all mixed-transport sync sets | Bridge mappings connect WhatsApp group(s) to Discord destinations; direction and mapping ownership can be bridge-specific | Simplified and generalized. |
| Multiple WhatsApp groups in one bridge | Multiple WhatsApp endpoint aliases can share a sync set with Discord/Telegram endpoints | Multi-group mappings are supported | Preserved through a more generic sync-set model. |
| Directional / one-way routing | Not implemented; configured sync sets are symmetric | Mapping direction can be bidirectional or one-way | Not carried forward because current primary use case is seamless symmetric synchronization. |
| Discord channel targeting | Configured text/announcement channel endpoints | Channels and threads can be direct bridge targets | Partially generalized. |
| Discord thread/forum behavior | Incoming thread/forum messages flatten to the configured parent endpoint while retaining opaque canonical child context for native reply/lifecycle return; no dynamic child endpoint is persisted | Thread-oriented bridge targets and provisioning flows are available | Context-preserving flat routing without dynamic routing identity. |
| Telegram forum topics | Incoming topic messages flatten to the configured parent chat while retaining opaque canonical topic context for native reply/lifecycle return | Telegram forum-topic discovery and bridge configuration are available through MTProto | Context-preserving Bot API operation with stable endpoint identity. |
| Friendly child-context presentation | Optional global mode renders `group/user` or `group:topic-or-thread/user`; labels are local presentation metadata, generic when unknown, and never used for routing. Discord uses the label as its per-message APP name and removes the wrapper from the body; WhatsApp and Telegram retain body attribution | Platform-specific presentation and topic metadata are available in bridge flows | Deliberately opt-in; opaque mode remains the privacy-first default and disabling friendly mode clears its label catalog. Presentation is transport-specific because Telegram Bot API cannot rename a bot per message. |
| Endpoint discovery | Authenticated live Discord discovery; bounded in-memory observed Telegram group discovery; WhatsApp joined-group discovery | Web-console discovery for WhatsApp, Discord, and Telegram resources | Preserved, with stricter persistence rules for human-readable names. |
| Sender presentation in Discord | One managed webhook per destination channel. Opaque mode uses the transient sender display name or HMAC fallback, prefixing cross-endpoint messages with the source alias. Friendly mode uses the complete transient label, such as `tg1:tt1/Vijay`, as the per-message APP username and leaves the body unprefixed | Webhook delivery can override username/presentation per forwarded sender | Preserved and narrowed so sender presentation is transient metadata rather than persistent identity; no per-user webhook is created. |
| Identity model | HMAC-derived actor IDs for persisted canonical identity; transient display names where permitted | Persistent anonymized aliases plus broader contact/member identity records | Reworked to reduce stored identity. |
| Text synchronization | Bidirectional across all configured transports | Bidirectional WhatsApp/Discord and additional bridge flows | Preserved and generalized. |
| Media | Text, image, video, audio/voice, documents, stickers within configured in-memory limits | Rich media forwarding, with GCS fallback for larger files in some paths | Preserved without cloud overflow storage. |
| Large-media overflow | No cloud fallback; over-limit content fails deterministically | Can upload oversized media to GCS and send a link | Intentionally not carried forward because transient in-memory media is a privacy boundary. |
| Replies | Canonical copy lookup maps reply targets across transports; textual/clickable fallback where native reply semantics cannot be reproduced | Reply context is mapped directly between bridge sides | Preserved and generalized. |
| Reactions | Add/remove reactions are normalized and propagated across transports | Reaction propagation is implemented with platform-specific handlers | Preserved and generalized. |
| Edits | Canonical edit lifecycle routes to known destination copies | Edit propagation is implemented directly from bridge mappings | Preserved and generalized. |
| Deletes/revokes | Canonical delete lifecycle routes to known destination copies and poll companions | Delete propagation is implemented with platform-specific mapping lookup | Preserved and generalized. |
| Native polls | Native WhatsApp, Discord, and Telegram polls are used where destination semantics can represent the source; deterministic text fallback otherwise | Poll handling is centered on bridge-specific WhatsApp/Discord behavior | Expanded into transport-neutral poll semantics. |
| Live poll results | Every bridged poll gets one bridge-owned editable companion containing aggregate option counts only; no voter names or per-voter choices | Live cross-group aggregate result messages are updated from upstream poll vote handling | Preserved conceptually, with a stricter aggregate-only privacy rule and generalized lifecycle. |
| Manual poll aggregation | Exact `aggregate-response` reply remains supported | Aggregate-result behavior exists in the bridge flow | Preserved for compatibility with the established interaction. |
| Loop prevention | Persistent copy/idempotency constraints plus bounded bridge-echo suppression | In-memory sent-message caches, processed-message checks, formatting guards, and stored mappings | Strengthened so restart correctness does not depend mainly on process-local caches. |
| Canonical message identity | Random transport-neutral canonical ID | Platform message IDs are connected through bridge-specific mappings | New abstraction in `message-sync`. |
| Persistent message-copy mapping | One canonical message may have one opaque remote copy per endpoint | Discord/WhatsApp mapping records connect corresponding messages | Generalized across every transport. |
| Delivery isolation | Independent ordered lane per destination endpoint | Discord webhook queues are isolated per webhook; other flows use direct platform-specific delivery | Expanded to every destination and lifecycle operation. |
| Retry behavior | Bounded retries, persistent pending work, safe failure classes, and explicit ambiguous-create handling | Discord webhook retry/backoff and platform-specific retries | Strengthened and generalized. |
| Ambiguous remote create | Never blindly retried when the provider may already have accepted the create | No equivalent transport-neutral ambiguity contract | New reliability rule. |
| Restart recovery | Persistent delivery ledger/copies plus WhatsApp HistorySync replay, Discord bounded history recovery, and Telegram update cursor handling | Live bridge processing includes duplicate checks; Telegram history sync/watermarks and bootstrap flows exist separately | Reworked toward normal-path replay rather than special import paths. |
| Historical import/bootstrap | No user-facing historical ZIP importer | Historical/bootstrap upload and Telegram history synchronization are available | Not carried forward into the core product. |
| Multi-user Web UI | Persistent admin/operator accounts, server-side sessions, invites, activation controls, reset tokens, role checks, and audit events in `control.db` | Multi-user console with admin/sub-admin concepts, invitations, registration verification, sharing/ownership, and in-memory active sessions | Preserved at the product level but redesigned with persistent session/account state and narrower roles. |
| Bridge/config ownership | Admins/operators manage one shared endpoint/sync-set configuration according to role policy | Mapping ownership and sharing are first-class concepts | Simplified. Per-user ownership of routing configuration is not part of the current model. |
| Membership pipelines | Public high-entropy pipeline URLs bind to configured WhatsApp or Discord endpoints | WhatsApp membership pipelines and broader member-request workflows are implemented | Preserved and redesigned behind the isolated control plane. |
| Work-email verification | Optional email challenge; request remains reviewable when mail delivery is not configured | Email verification is integrated into registration/membership flows | Preserved with optional provider configuration. |
| Discord identity verification | Fulfillment resolves a Discord member and assigns a configured role; the control plane avoids copying Discord identity into `sync.db` | Registration/member flows can verify Discord identity by DM and manage roles | Narrowed to the minimum needed for the current membership workflow. |
| WhatsApp membership fulfillment | Direct add where allowed; invite fallback remains pending until membership is confirmed and the invite is rotated | Direct participant add and membership tooling are implemented | Preserved with explicit idempotent fulfillment state. |
| Discord membership fulfillment | Assign configured role through the existing bot session | Assign role and send DM notifications | Preserved with safer persisted result classes. |
| Evidence uploads | Bounded PDF/image evidence under a private local `/data` directory with retention cleanup | Uploaded evidence can be stored locally or in GCS | Preserved without mandatory cloud object storage. |
| AI-assisted review | Optional OpenRouter model; structured advisory output only; raw prompts/responses are not persisted; AI cannot approve membership | Gemini/Sightengine-style analysis and enrichment integrations are available | Replaced with an optional provider boundary that can use free models and fails safely to human review. |
| LinkedIn / company enrichment | Only narrow host/domain/evidence signals needed for membership intake; no broad enrichment subsystem | Broader search/enrichment integrations are present | Deliberately narrowed for privacy and YAGNI. |
| Dedicated WhatsApp numbers | Multiple WhatsApp linked-device accounts per daemon via dynamic connections with isolated protocol DBs | Dedicated-line/multi-session and SIM-pool/provisioning workflows are present | Multi-account WhatsApp is now supported natively via dynamic connections; dedicated hardware SIM pool management remains out of scope. |
| SIM/eSIM provisioning | Not implemented | External number/SIM provisioning integration exists | Not carried forward because it adds operational and paid-service complexity. |
| Cloud database requirement | None | Firestore can be used for application state | Removed. SQLite is sufficient for a single self-hosted instance. |
| Cloud object storage requirement | None | GCS can be used for media/evidence | Removed from core deployment. |
| Paid services required | None for normal routing, administration, or membership review; optional integrations can be omitted | Some advanced workflows are designed around external cloud/search/AI/SIM services | Reduced so the complete core can remain local and inexpensive. |
| Secrets | Environment variables or mounted secret files; transport credentials are excluded from routing tables and APIs | Environment configuration plus provider credentials for each integration | Same deployment concept, stricter separation from application persistence. |
| Logging | Fixed safe fields; raw protocol errors/objects and PII are excluded | Operational/audit logs can include bridge names, identities, IDs, filenames, URLs, and other detailed context | Reworked to enforce the privacy invariant. |
| Core privacy boundary | `sync.db` and application logs contain no PII/PHI; `control.db` is the explicit sensitive exception | Product workflows retain broader identity/contact/member data as operational state | Major architectural divergence. |
| Web UI packaging | Embedded static SPA compiled into the Go binary | Express serves a static web console | Preserved, simplified for a single deployable artifact. |
| Container model | Rootless/non-root static runtime, read-only root filesystem, `/data` as the only persistent writable path | Node/PM2/GCP-oriented deployment with local/cloud state options | Reworked for portable Docker/Podman self-hosting. |
| Release model | Image publication only when `VERSION` changes on `main` | Standard Node application release/deployment flow | Project-specific simplification. |

## Detailed Design Differences

### 1. Transport-neutral routing instead of bridge-specific orchestration

The most important difference is where synchronization semantics live.

In `message-sync`, every configured destination is an endpoint alias. Incoming transport objects are normalized into canonical events before the router decides where the event should go. The router and store understand canonical messages, endpoint copies, lifecycle operations, recovery checkpoints, and delivery state without depending on Discord, WhatsApp, or Telegram-specific schemas.

This makes a sync set such as:

```text
WhatsApp A <-> Discord B <-> Telegram C <-> WhatsApp D
```

one routing problem rather than several pairwise bridge implementations.

The upstream model is more direct and can be simpler for a small number of WhatsApp/Discord mappings. It also makes specialized behaviors—directional mappings, thread targets, ownership, dedicated numbers, bootstrap flows—easy to attach to an individual bridge. `message-sync` intentionally gives up some of that mapping-specific flexibility to keep one coherent synchronization model.

### 2. Reliability and idempotency are first-class state

The upstream implementation contains practical loop-prevention techniques: sent-message caches, duplicate checks, bot/webhook filtering, formatting guards, message mappings, and isolated Discord webhook queues.

`message-sync` keeps the same operational goal but moves correctness into persistent transport-neutral state:

- canonical message and remote-copy uniqueness;
- idempotent fan-out;
- one ordered delivery lane per destination;
- persistent pending work;
- bounded retries;
- explicit distinction between definite failure and ambiguous provider acceptance;
- recovery that re-enters the same routing path instead of bypassing it.

This is intentionally more infrastructure than a small pairwise bridge needs, but it is what allows mixed multi-endpoint sync sets to remain restart-safe without multiplying transport-specific loop rules.

### 3. Poll behavior preserves the useful upstream interaction while hiding voters

The live poll-results idea is retained, but its privacy contract is stricter.

`message-sync` keeps only canonical option indexes and aggregate counts needed for synchronization. The live companion shows the poll question/options and aggregate counts but never voter names or a mapping of a voter to an option. Native polls are used on destinations that can represent the source semantics; otherwise a deterministic text representation is used.

The result companion is bridge-owned so it can be edited as aggregate state changes and deleted when the poll is deleted. The existing `aggregate-response` interaction remains available as a manual refresh mechanism.

### 4. Multi-user administration is separated from routing state

Both projects provide more than a single shared admin password. The difference is the data boundary.

`message-sync` stores users, sessions, invites, resets, audit events, verification pipelines, membership requests, and evidence references in `control.db`. The canonical router never queries that database. Routing state stays in `sync.db`, where PII/PHI is prohibited.

This separation allows membership workflows to handle the minimum necessary work email, transport identity, and evidence without weakening the privacy rules for normal message synchronization.

### 5. Membership verification keeps human authority

The upstream implementation demonstrates a broad automated workflow: account verification, email verification, Discord checks, membership requests, evidence analysis, enrichment, WhatsApp participant actions, Discord role assignment, and external provider integrations.

`message-sync` keeps the useful workflow but narrows automation:

1. accept a bounded public request against a configured pipeline;
2. optionally verify the work email;
3. run deterministic local checks;
4. optionally request advisory image analysis;
5. require an authenticated human decision;
6. perform a narrow WhatsApp add/invite or Discord role action;
7. retain only the bounded control-plane state needed to review, audit, retry, and clean up the request.

AI output can never approve or reject membership by itself.

### 6. Local-first deployment is a product requirement

The upstream implementation can use managed services for state, media, search/enrichment, AI checks, email, and number provisioning.

`message-sync` treats those dependencies as optional at most. Normal message synchronization uses local SQLite and memory. Membership verification can also operate without a paid service: email and AI integrations are optional, OpenRouter can use a free model, and slow human review is acceptable.

The result is a service that can remain containerized on a single machine with no mandatory cloud account beyond the messaging platforms themselves.

## Capabilities Not Carried Forward

The following upstream capabilities are intentionally absent from the current `message-sync` implementation:

- directional one-way bridge mappings;
- user-owned/shared bridge mappings as a routing authorization model;
- dynamically persisted Discord thread/forum endpoints;
- Telegram MTProto user-account sessions and directly configured topic bridges;
- historical ZIP/bootstrap import workflows;
- cloud overflow storage for large message media;
- automated SIM/eSIM/number-pool provisioning;
- broad LinkedIn/company enrichment outside the narrow membership intake checks.

Their absence should not be interpreted as an implementation queue. New scope should be justified independently against current use cases and project invariants.

## Capabilities Generalized Beyond the Upstream Design

`message-sync` now has several capabilities whose architecture is materially broader than the original bridge model:

- dynamic platform connections supporting multiple concurrent WhatsApp accounts, Discord bots, and Telegram bots without restart;
- mixed WhatsApp/Discord/Telegram all-to-all sync sets;
- a common canonical message and lifecycle model;
- transport-neutral persistent copy mapping;
- destination-isolated persistent delivery state;
- recovery through the normal router path;
- explicit ambiguous-provider-create handling;
- zero-PII/PHI core persistence and logging;
- a separate sensitive control plane for accounts and membership;
- rootless read-only container deployment with local SQLite only;
- aggregate-only poll-result synchronization across supported transports.

## Maintenance Rule

This file is a comparison snapshot, not a backlog.

Update it when an implemented feature materially changes the comparison, when an existing implementation is removed, or when an architectural decision changes why the two projects differ. Product scope remains authoritative in `README.md`; architecture and privacy invariants remain authoritative in `ARCHITECTURE.md` and `AGENTS.md`.
