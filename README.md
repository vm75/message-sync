# message-sync

[![Release images](https://img.shields.io/github/actions/workflow/status/vm75/message-sync/release-images.yml?branch=main&label=release%20images&style=flat-square&logo=githubactions)](https://github.com/vm75/message-sync/actions/workflows/release-images.yml)
[![Docker pulls](https://img.shields.io/docker/pulls/vm75/message-sync?style=flat-square&logo=docker)](https://hub.docker.com/r/vm75/message-sync)
[![Go 1.25+](https://img.shields.io/badge/go-1.25%2B-00ADD8?style=flat-square&logo=go)](https://go.dev/)
[![MIT license](https://img.shields.io/badge/license-MIT-yellow.svg?style=flat-square)](LICENSE)

`message-sync` is a self-hosted, privacy-first service that synchronizes configured WhatsApp groups, Discord channels, and Telegram groups or supergroups. Multiple accounts and bots can participate in alias-based sync sets while a single transport-neutral router owns message identity, fan-out, and lifecycle behavior.

## Capabilities

- **Multi-Transport Synchronization**: Seamless bidirectional synchronization across WhatsApp groups and communities (including community subgroups and hosted LIDs), Discord channels (including thread/forum-post lineage and webhook sender attribution), and Telegram groups/supergroups (including forum topic lineage and group-to-supergroup migration handling).
- **Comprehensive Message Lifecycle**:
  - Text forwarding with bold-italic provenance attribution (`*_<alias>/<username>_*: <text>`).
  - Transient in-memory media forwarding (images, videos, audio/voice notes, documents, and stickers) within configurable size limits and zero on-disk retention.
  - Best-effort native replies across transports with deterministic fallback attribution and clickable quote links on Discord.
  - Native emoji reaction propagation (add, change, remove) normalized across platforms with actor identity mapping.
  - Message edit and deletion/revocation propagation with canonical tombstones to prevent replay resurrection.
- **Native & Aggregate Polls**: Native polls across WhatsApp, Discord, and Telegram where destination semantics are representable; cross-endpoint Discord and Telegram polls include the source label in the visible poll presentation, while unsupported poll shapes use deterministic text fallback. Includes aggregate-only live-result companion messages, plus live on-demand aggregate responses that continue updating as votes/snapshots change, via configurable reply triggers (supports multiple triggers, e.g. `aggregate-response`, `/poll-results`). Encrypted poll questions and options survive server restarts without entering the PII-free routing database.
- **Source-Local Message Suppression**: Configurable prefix filtering (supports multiple prefixes, e.g. `!local`, `#local`, `//`, `[local]`) to suppress private or internal messages before canonicalization, transmission, or media loading; edits, reactions, and deletes for suppressed messages also remain local.
- **Thread & Topic Context (Child Scopes)**: Preserves Discord thread/forum and Telegram topic lineage without requiring dynamic child endpoints; supports privacy-first `opaque` (default) and presentation-only `friendly` label modes.
- **Reliable Ordered Delivery & Health Monitoring**: Single ordered ingress worker, independent per-destination FIFO lanes with exponential backoff retry, ambiguity-safe create handling (`awaiting_replay`), and real-time delivery health monitoring via the management console.
- **Embedded Web Management Console & REST API**: Zero-dependency embedded Web UI and authenticated REST API for dynamic connection lifecycle, serialized WhatsApp QR pairing, atomic endpoint alias renaming (with automatic copy/reaction reference migration), sync set mesh configuration, and live runtime configuration reload without process restarts.
- **Sensitive Control Plane & Multi-User RBAC**: Isolated mode-`0600` `control.db` supporting Admin and Operator roles, bcrypt passwords, HMAC session tokens/cookies, session revocation, one-time invite tokens, fixed-field audit logging, and AES-256-GCM encrypted bot credentials derived from `IDENTITY_SECRET`.
- **Membership Intake & Verification Pipeline (Experimental)**: Sync-set bound public intake forms (`/api/verification/join/{token}`), automated email verification challenges (Resend-compatible), private mode-`0600` evidence storage with automatic purge upon approval/rejection decisions, optional advisory AI image verification (strict zero-training requirement), and human-in-the-loop review with automated fulfillment (e.g. WhatsApp join invite links).
- **Automated WhatsApp Chat Cleanup**: Scheduled AppState background cleanup to prune old messages on the sync account according to configurable retention rules.
- **Hardened Container Deployment**: Static non-root container (UID 1000) with read-only root filesystem, dropped Linux capabilities, and `/tmp` tmpfs, fully compatible with Docker, rootful Podman, and rootless Podman.

Provider APIs impose some limits: recovery cannot reconstruct every offline lifecycle event, Telegram cannot provide complete live results for arbitrary human-created polls, and a provider create with an ambiguous outcome requires replay or operator reconciliation instead of a blind retry. See [Architecture](ARCHITECTURE.md) for the precise boundaries.

## Privacy model

The core routing boundary is deliberately content-free at rest:

- `/data/sync.db` and application logs contain no message bodies, media, participant identities, human-readable provider names, credentials, or raw provider objects/errors.
- User identity crossing a transport boundary is HMAC-derived from `IDENTITY_SECRET`; display names used for attribution remain transient.
- Message media is held only long enough to forward and is not persisted by the router.
- `/data/whatsapp/<connection-id>.db` is isolated sensitive whatsmeow protocol state and is never queried for application features.
- `/data/control.db` is the explicit sensitive exception for accounts, sessions, audit records, encrypted Discord/Telegram Bot API credentials, encrypted Telegram MTProto API/session/peer state, and experimental membership verification. OTPs and Telegram 2FA passwords are never persisted.
- Membership evidence is stored privately under `/data/membership-evidence/`, removed on final approve/reject decisions, and subject to bounded cleanup when the experimental verification workflow is used.

The optional `friendly` child-context display mode stores bounded current thread/topic labels in `sync.db` for presentation only. The default `opaque` mode does not; labels never control routing or identity.

## Requirements

Choose one runtime:

- Docker Compose or Podman Compose; or
- Go 1.25 or newer for source builds.

You also need at least two provider conversations and an `IDENTITY_SECRET` of 32 bytes or more. Keep this secret stable: it derives actor identities and the key that encrypts stored bot credentials.

## Quick start from source

Create the environment file and persistent data directory:

```sh
mkdir -p data
printf 'IDENTITY_SECRET=%s\nDATA_DIR=/data\nPORT=8080\nLOG_LEVEL=info\n' "$(openssl rand -hex 32)" > .env
```

Build and start the repository's hardened Compose service:

```sh
docker compose up -d --build
```

Podman users can run `podman compose up -d --build`. Open `http://localhost:8080`, create the first administrator account, then configure:

1. Connections — authenticate one or more provider accounts or bots.
2. Endpoints — assign safe aliases to discovered parent conversations.
3. Sync sets — group two or more endpoint aliases for all-to-all synchronization.

For a published-image Compose example and tag policy, see [Docker Hub](DOCKERHUB.md).

## Provider setup

### WhatsApp

Create a WhatsApp connection, start pairing, then scan the QR code from **Linked Devices -> Link a Device**. Pairing is serialized to one connection at a time; linked accounts reconnect from their own protocol database.

### Discord

Create a Discord bot, enable the privileged **Message Content** intent, and grant **View Channel**, **Read Message History**, **Send Messages**, **Add Reactions**, and **Manage Webhooks** in destination channels. Paste the bot token once when creating the connection; the service encrypts it in `control.db` and never returns it through read APIs.

### Telegram

Telegram connections support two mutually exclusive methods: **Bot API** (BotFather token, observation-based discovery, Bot Privacy Mode applies) and **Phone / MTProto** (Telegram API ID/hash + phone login, complete joined-group/forum-topic discovery, and bounded history recovery). Both use the canonical `telegram` transport, but every endpoint names exactly one connection and never falls back to the other method. Private DMs and broadcast channels are not synchronized.

See [Telegram integrations](docs/TELEGRAM.md) for the capability comparison, secure phone/code/2FA setup, session storage model, recovery behavior, and troubleshooting. See the [manual testing guide](docs/TESTING_GUIDE.md) for broader end-to-end checks.

## Configuration

Deployment settings come from the environment; routing and feature settings are configured through the authenticated Web UI or REST API.

### Environment variables

| Variable | Required | Default | Purpose |
|---|---:|---|---|
| `IDENTITY_SECRET` | yes | none | HMAC identity and credential-encryption root secret; minimum 32 bytes. |
| `DATA_DIR` | no | `/data` | Directory containing all SQLite state and private evidence. |
| `PORT` | no | `8080` | HTTP port when `API_ADDR` is unset. |
| `API_ADDR` | no | derived from `PORT` | Full HTTP listen address, mainly useful for direct source runs and tests. |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, or `error`. |
| `WHATSAPP_DEVICE_NAME` | no | `message-sync` | Companion device name shown in WhatsApp Linked Devices. |
| `VERIFICATION_MAIL_API_KEY` | no | none | Enables Resend-compatible email delivery for experimental membership verification with `VERIFICATION_MAIL_FROM`. |
| `VERIFICATION_MAIL_FROM` | no | none | Sender used by optional experimental membership email delivery. |
| `VERIFICATION_PUBLIC_BASE_URL` | no | request origin | External base URL used in experimental membership links behind a proxy. |
| `OPENROUTER_API_KEY` | no | none | Enables advisory image evidence analysis only when training is explicitly disabled. |
| `OPENROUTER_ALLOW_TRAINING` | no | unset | Must equal `false` for OpenRouter analysis to run. |
| `OPENROUTER_MODEL` | no | `openrouter/free` | Model used by optional advisory analysis. |
| `MESSAGE_SYNC_DATA_DIR` | no | `./data` | Host-side `/data` bind source used by `compose.yml`; it is not read by the service. |

Discord/Telegram Bot API tokens and Telegram MTProto application/session state are configured dynamically and encrypted with AES-256-GCM in `control.db`; they never belong in `.env`.

### Runtime settings

Global routing and feature settings are configured in the Web UI Settings tab or via `PUT /api/config`, taking effect immediately without restarting the service:

| Setting | Default | Description |
|---|---|---|
| `username_mode` | `push_name` | Attribution display mode: `push_name` (transient provider name) or `hash` (anonymized HMAC actor ID). |
| `media.max_size_mb` | `16` | Maximum allowed in-memory size (MB) for forwarded media (images, videos, audio, documents). |
| `recovery.max_age_hours` | `72` | Bounded lookback window in hours for offline message recovery. |
| `recovery.max_messages_per_group` | `1000` | Maximum messages recovered per conversation after downtime. |
| `storage.message_retention_days` | `90` | Retention duration in days for content-free routing metadata and message copies. |
| `polls.aggregation_trigger` | `aggregate-response` | Space-separated triggers to request an on-demand poll aggregate summary (e.g. `aggregate-response /poll-results #agg`). |
| `local_prefix` | `""` (disabled) | Space-separated prefix strings to suppress messages from syncing (e.g. `!local #local // [local]`). |
| `whatsapp_cleanup.enabled` | `false` | Enables scheduled AppState chat pruning for old messages on the WhatsApp sync account. |
| `whatsapp_cleanup.retention_days` | `30` | Minimum message age in days before WhatsApp AppState chat cleanup prunes messages. |
| `whatsapp_device_name` | `message-sync` | Companion device name shown in WhatsApp Linked Devices. |
| `child_scope_mode` | `opaque` | Child-conversation presentation mode: `opaque` (privacy default) or `friendly` (presentation thread/topic labels). |

The Web UI Settings tab also includes a browser-local preference to enable the experimental **Membership review** interface (disabled by default).

The current SQLite schemas initialize fresh databases and do not provide an upgrade migration path for older development databases. Back up `/data` before upgrades and consult release notes before reusing existing state.

## CLI and development

```sh
make run                 # source .env and run the service
make fmt                 # format Go sources
make test                # run all Go tests
make vet                 # run go vet
make build               # build bin/message-sync
./bin/message-sync version
DATA_DIR=./data ./bin/message-sync validate-config
```

The complete automated gate and safe smoke-test procedure are in [TESTING.md](TESTING.md).

## Documentation

- [Architecture and privacy invariants](ARCHITECTURE.md)
- [Automated testing](TESTING.md)
- [Manual provider testing](docs/TESTING_GUIDE.md)
- [Telegram Bot API and Phone/MTProto setup](docs/TELEGRAM.md)
- [Container images](DOCKERHUB.md)
- [Feature comparison](docs/FEATURE_COMPARISON.md)
- [Contributor and coding-agent guide](AGENTS.md)
- [Release history](CHANGELOG.md)

## Disclaimer

This independent project is not affiliated with WhatsApp, Meta, Discord, or Telegram. Automated and unofficial-client use may be subject to provider terms and account restrictions; operate it at your own risk.

## License

[MIT](LICENSE)
