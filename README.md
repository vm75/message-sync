# message-sync

[![CI](https://img.shields.io/github/actions/workflow/status/vm75/message-sync/ci.yml?branch=main&label=CI&style=flat-square&logo=githubactions)](https://github.com/vm75/message-sync/actions/workflows/ci.yml)
[![Release images](https://img.shields.io/github/actions/workflow/status/vm75/message-sync/release-images.yml?label=release%20images&style=flat-square&logo=githubactions)](https://github.com/vm75/message-sync/actions/workflows/release-images.yml)
[![Docker pulls](https://img.shields.io/docker/pulls/vm75/message-sync?style=flat-square&logo=docker)](https://hub.docker.com/r/vm75/message-sync)
[![Go 1.25+](https://img.shields.io/badge/go-1.25%2B-00ADD8?style=flat-square&logo=go)](https://go.dev/)
[![MIT license](https://img.shields.io/badge/license-MIT-yellow.svg?style=flat-square)](LICENSE)

`message-sync` is a self-hosted, privacy-first service that synchronizes configured WhatsApp groups, Discord channels, and Telegram groups or supergroups. Multiple accounts and bots can participate in alias-based sync sets while a single transport-neutral router owns message identity, fan-out, and lifecycle behavior.

## Capabilities

- **Multi-Transport Synchronization**: Seamless bidirectional synchronization across WhatsApp groups and communities (including community subgroups and hosted LIDs), Discord channels (including thread/forum-post lineage and webhook sender attribution), and Telegram groups/supergroups (including forum topic lineage and group-to-supergroup migration handling).
- **Comprehensive Message Lifecycle**:
  - Text forwarding with bold-italic provenance attribution (`*_<group>/<name> (<participant-alias>)_*: <text>`).
  - Transient in-memory media forwarding (images, videos, audio/voice notes, documents, and stickers) within configurable size limits and zero on-disk retention.
  - Best-effort native replies across transports with deterministic fallback attribution and clickable quote links on Discord.
  - Native emoji reaction propagation (add, change, remove) normalized across platforms with actor identity mapping.
  - Message edit and deletion/revocation propagation, including plain text WhatsApp edits from the linked account's phone, with canonical tombstones to prevent replay resurrection.
- **Native & Aggregate Polls**: Native polls across WhatsApp, Discord, and Telegram where destination semantics are representable; cross-endpoint Discord and Telegram polls include the source label in the visible poll presentation, while unsupported poll shapes use deterministic text fallback. Includes aggregate-only live-result companion messages, plus live on-demand aggregate responses that continue updating as votes/snapshots change, via configurable reply triggers (supports multiple triggers, e.g. `aggregate-response`, `/poll-results`). Encrypted poll questions and options survive server restarts without entering the PII-free routing database.
- **Source-Local Message Suppression**: Configurable prefix filtering (supports multiple prefixes, e.g. `!local`, `#local`, `//`, `[local]`) to suppress private or internal messages before canonicalization, transmission, or media loading; edits, reactions, and deletes for suppressed messages also remain local.
- **Thread & Topic Context (Child Scopes)**: Preserves Discord thread/forum and Telegram topic lineage without requiring dynamic child endpoints. Forwarded child messages use `<group-alias>/<thread-or-topic>/<name> (<participant-alias>)`; opaque provider child IDs never appear in message text. `opaque` (default) keeps names transient, while `friendly` may persist bounded labels for restart/replay presentation.
- **Reliable Ordered Delivery & Health Monitoring**: Single ordered ingress worker, independent per-destination FIFO lanes with exponential backoff retry, ambiguity-safe create handling (`awaiting_replay`), and real-time delivery health monitoring via the management console.
- **Zero-PII Privacy & Route-Aware Anonymization**: Routing database (`sync.db`) and application logs contain no message bodies, media, phone numbers, or user IDs. Optional per-sync-set one-way Discord anonymization (`anonymizeToDiscord`) transforms sender names, phone numbers, and structured mentions into assigned aliases (e.g., `Amber Badger`) exclusively for Discord destinations while WhatsApp and Telegram retain ordinary names with aliases.
- **Embedded Web Management Console & REST API**: Zero-dependency embedded Web UI and authenticated REST API for dynamic connection lifecycle, serialized WhatsApp QR pairing, atomic endpoint alias renaming (with automatic copy/reaction reference migration), sync set mesh configuration with Discord anonymization controls, and live runtime configuration reload without process restarts.
- **Sensitive Control Plane & Multi-User RBAC**: Isolated mode-`0600` `control.db` supporting Admin and Operator roles, bcrypt passwords, HMAC session tokens/cookies, session revocation, one-time invite tokens, fixed-field audit logging, and AES-256-GCM encrypted bot credentials derived from `IDENTITY_SECRET`.
- **Membership Intake & Verification Pipeline (Experimental)**: Sync-set bound public intake forms (`/api/verification/join/{token}`), automated email verification challenges (Resend-compatible), private mode-`0600` evidence storage with automatic purge upon approval/rejection decisions, optional advisory AI image verification (strict zero-training requirement), and human-in-the-loop review with automated fulfillment (e.g. WhatsApp join invite links).
- **Automated WhatsApp Chat Cleanup**: Scheduled AppState background cleanup to prune old messages on the sync account according to configurable retention rules.
- **Hardened Container Deployment**: Static non-root container (UID 1000) with read-only root filesystem, dropped Linux capabilities, and `/tmp` tmpfs, fully compatible with Docker, rootful Podman, and rootless Podman.

Provider APIs impose some limits: recovery cannot reconstruct every offline lifecycle event, Telegram cannot provide complete live results for arbitrary human-created polls, and a provider create with an ambiguous outcome requires replay or operator reconciliation instead of a blind retry. See [Architecture](ARCHITECTURE.md) for the precise boundaries.

## Privacy model

The core routing boundary is deliberately content-free at rest:

- `/data/sync.db` and application logs contain no message bodies, media, participant identities, human-readable provider names, credentials, or raw provider objects/errors.
- User identity crossing a transport boundary is HMAC-derived from `IDENTITY_SECRET`; display names used for attribution remain transient.
- Each sync-set actor has a permanent assigned alias. `sync.db` stores only its HMAC actor ID, safe alias and ordinal; real participant names remain transient. One-way Discord anonymization uses that assigned alias without the real name.
- Message media is held only long enough to forward and is not persisted by the router.
- `/data/whatsapp/<connection-id>.db` is isolated sensitive whatsmeow protocol state and is never queried for application features.
- `/data/control.db` is the explicit sensitive exception for accounts, sessions, audit records, encrypted Discord bot/webhook credentials, encrypted Telegram Bot API credentials, encrypted Telegram MTProto API/session/peer state, and experimental membership verification. Discord webhook URLs and bot tokens are never returned through read APIs; Telegram OTPs and 2FA passwords are never persisted.
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
3. Sync sets — group two or more endpoint aliases for all-to-all synchronization, with optional one-way Discord anonymization (`anonymizeToDiscord`).

For a published-image Compose example and tag policy, see [Docker Hub](DOCKERHUB.md).

## Provider setup

### WhatsApp

Create a WhatsApp connection, start pairing, then scan the QR code from **Linked Devices -> Link a Device**. Pairing is serialized to one connection at a time; linked accounts reconnect from their own protocol database.

### Discord

Discord always uses a **bot token** for inbound Gateway events and native Discord operations. Enable the privileged **Message Content** intent in the Discord Developer Portal, install the bot in the target server, and grant the permissions required by the features you use. At minimum for normal bidirectional message sync, grant **View Channel**, **Read Message History**, and **Send Messages**; grant **Add Reactions** for reaction sync and the appropriate thread/poll permissions for those features.

For native cross-group mentions immediately after restart, optionally enable
**Server Members Intent** in the Developer Portal. It permits an on-demand
guild-roster lookup when the temporary native-ID cache is empty. Live or
recovered participant messages also populate this cache.

`message-sync` supports two outbound webhook setups. Choose one when creating the Discord connection.

#### Option A — managed webhook (default)

Use this when you want the simplest setup or one Discord bot connection to serve multiple channels.

1. Create the Discord application and bot, enable **Message Content** intent, and copy the bot token.
2. Install the bot in the server with **View Channel**, **Read Message History**, **Send Messages**, **Manage Webhooks**, plus any reaction/thread/poll permissions you need.
3. In `message-sync`, create a Discord connection and choose **Managed by message-sync**.
4. Paste the bot token once.
5. Use **Discover** to add one or more Discord channels as endpoints.

For every configured channel, `message-sync` finds or creates one bridge-owned webhook and keeps the webhook credential only inside the Discord adapter process. The bot needs **Manage Webhooks** because it provisions and repairs those webhooks.

#### Option B — existing webhook + channel ID

Use this when you prefer to create the webhook yourself and do **not** want to grant the bot **Manage Webhooks**.

1. Create the Discord application and bot, enable **Message Content** intent, and copy the bot token.
2. Install the bot in the server with **View Channel**, **Read Message History**, **Send Messages**, plus any reaction/thread/poll permissions you need. **Manage Webhooks is not required.**
3. In Discord, open the target channel's **Edit Channel → Integrations → Webhooks**, create a webhook, and copy its webhook URL.
4. Enable Discord Developer Mode if necessary, then copy the same channel's numeric **Channel ID**.
5. In `message-sync`, create a Discord connection and choose **Use existing webhook + channel ID**.
6. Paste the **bot token**, **webhook URL**, and matching **channel ID**.
7. Use **Discover**; this connection exposes only the configured channel, which you can then add as an endpoint.

The bot token is still required in this mode. The bot listens for Discord → WhatsApp/Telegram messages, edits, reactions, polls, threads, and history events; the supplied webhook handles WhatsApp/Telegram → Discord message delivery and webhook-owned edits/deletes. Messages sent by that webhook are recognized as bridge-owned and ignored on ingress to prevent loops.

An explicit-webhook Discord connection is intentionally bound to **one Discord channel**. Use managed-webhook mode when one connection must serve multiple Discord channels. The bot token and explicit webhook URL are encrypted in `control.db` and are never returned by connection read APIs.

| Discord requirement | Managed webhook | Existing webhook + channel ID |
|---|---:|---:|
| Bot application + bot token | Required | Required |
| Message Content intent | Required for ordinary message ingress | Required for ordinary message ingress |
| View Channel / Read Message History | Required | Required |
| Send Messages | Required for native bot operations | Required for native bot operations |
| Add Reactions | Required only for reaction sync | Required only for reaction sync |
| Manage Webhooks | **Required** | **Not required** |
| Webhook creation | Automatic | Manual |
| Channel ID entry | Discovered | Entered during connection setup |
| Multiple Discord channels per connection | Yes | No — one configured channel |

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

Discord bot tokens, optional explicit Discord webhook URLs, Telegram Bot API tokens, and Telegram MTProto application/session state are configured dynamically and encrypted with AES-256-GCM in `control.db`; they never belong in `.env`.

### Runtime settings

Routing and feature settings belong to each sync set. Open a sync set and choose **Edit settings** to open its settings modal, or use `PUT /api/sync-sets/{id}` with a nested `settings` object. Omitted fields retain their current values, including nested fields and `anonymizeToDiscord`. Changes reload without restarting the service. Save is enabled only while the draft differs from the saved settings; saving and failure feedback appears beside the button. Saving or reverting a change disables Save again.

| Setting inside `settings` | New-set default | Description |
|---|---|---|
| `membershipReviewEnabled` | `false` | Enable the experimental membership review UI for this set, shared across browsers. |
| `privateDMRelayEnabled` | `false` | Permit private relays within this set only when the global administrator switch is also enabled. |
| `media.enabled`, `media.maxSizeMB` | `true`, `100` | Forward media with an in-memory size limit in MB; provider limits still apply. |
| `recovery.enabled`, `recovery.maxAgeHours`, `recovery.maxMessagesPerGroup` | `true`, `24`, `200` | Automatic recovery bounds for conversations in this set; provider availability and limits still apply. |
| `storage.messageRetentionDays` | `90` | Retain content-free routing metadata for the configured days. |
| `polls.aggregationTrigger` | `aggregate-response` | Space-separated triggers for poll aggregate summaries. |
| `localPrefix` | `""` | Space-separated prefixes that suppress messages from syncing. |
| `whatsappCleanup.enabled`, `whatsappCleanup.retentionDays` | `false`, `30` | Scheduled WhatsApp AppState cleanup for this set's chats. |
| `childContextDisplayMode` | `opaque` | `opaque` uses transient child labels; `friendly` also persists observed thread/topic labels for restart/replay. |

For example, `{"settings":{"media":{"maxSizeMB":50}}}` updates one set while preserving its other settings. Upgrade initialization copies existing routing policies into each existing sync set once. Subsequent changes are independent. Retention follows the original sync set of each canonical message, even after an endpoint moves; orphan history and suppression markers expire after 90 days.

The WhatsApp linked-device name belongs to its connection, shared by every set using that connection. Set `whatsappDeviceName` when creating a WhatsApp connection, or edit **Connections → Device name** (`PUT /api/connections/{id}`). It defaults to `message-sync` for new connections and applies when linking the device. Existing connections import their former global name once. User accounts and admin security stay global. `/api/config` exposes only the administrator's default-off `privateDMRelayEnabled` safety switch; other runtime settings belong to sync sets.

Private messaging requires **Settings → Allow private DM relays** and the
matching switch in each sync set's **Edit settings** modal. In a private chat
with the owning sync account/bot, `/dm @{Amber Badger} message` addresses one observed,
currently verified member of the same set. Continue only with native **Reply**
on the bot's relayed message. `/help` and Telegram `/start` explain usage.
Text only, 1600 UTF-8 bytes per relayed body. Reply mappings expire after 24
hours. Private edits and deletes never propagate. WhatsApp attempts per-message
**Delete for Me** only; Discord and Telegram Bot API retain provider copies
because their delete APIs would also remove the participant's message.
MTProto personal inboxes are not intercepted. See [private messaging](docs/PRIVATE_MESSAGING.md)
for reachability, privacy, cleanup and restart limits.

Enable **Membership review** in each sync set’s settings modal. The saved setting replaces the former browser-local switch. Instructions, evidence guidance and custom fields stay specific to that set. **Review membership** opens requests and pipelines for the selected set; the Membership selector switches between enabled sets. This controls the review interface; existing pipeline enablement and in-flight applications keep their existing behavior.

The project does not have a general versioned migration framework; schema changes that must preserve existing installations may use narrowly scoped idempotent initialization migrations with explicit upgrade tests. Back up `/data` before upgrades and consult release notes before reusing existing state.

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
- [Maintainer release procedure](RELEASING.md)
- [Feature comparison](docs/FEATURE_COMPARISON.md)
- [Contributor and coding-agent guide](AGENTS.md)
- [Release history](CHANGELOG.md)

## Disclaimer

This independent project is not affiliated with WhatsApp, Meta, Discord, or Telegram. Automated and unofficial-client use may be subject to provider terms and account restrictions; operate it at your own risk.

## License

[MIT](LICENSE)


### Cross-group participant references

Each accepted group-message author receives a persistent alias, such as
`Amber Badger`, scoped to that actor and sync set. Aliases are unique across
this installation: the same platform account in another set receives a different
alias. New aliases follow a fixed scrambled order spread across both word lists.
There are 65,536 frozen two-word names; subsequent allocations add a
numeric tier, such as `Amber Badger 2`. Deletion never makes an alias reusable.

Ordinary attribution is `group/Alice (Amber Badger)`; anonymized Discord
attribution is `group/Amber Badger`. Child messages retain `group/child/name`
context. There is no username-mode selector or configurable discriminator.
The alias is always shown and retained when a long webhook name is truncated.

After observing an author's alias, send `Hi @{Amber Badger}` from another
configured group in the same sync set. `@{amber badger}` also works. Each
actual destination independently verifies current membership and permissions
before rendering a native mention. WhatsApp requires a verified LID; Discord
checks guild/channel access and private-thread membership and allowlists only
verified users; Telegram Bot API checks membership in the destination chat.
A verified explicit reference may tag a local Discord account even on an
anonymized route. Other sender and ordinary mention anonymization remains active.

Unknown, out-of-set, escaped or unverifiable references retain the exact authored
token, including braces and case. Use `\@{Amber Badger}` to avoid requesting a
notification. Telegram MTProto and edits retain literal braced reference text.
Plain provider-native mentions remain a separate namespace. Structured mentions
reuse an existing author alias; they do not enroll a new actor. Names and native
user IDs are not stored in the alias registry, and names never link providers.

Alias assignments survive restarts. Discord/Telegram native-ID caches remain
transient; Discord restores observations from accepted history and can match a
cold cache against guild identities with optional **Server Members Intent**.
Denied or unavailable lookups preserve literal text. WhatsApp author allocation
requires a verified canonical LID, including for PN-form messages. If the LID
cannot be established, forwarding fails safely before delivery or checkpoint
advancement; provider recovery windows limit whether it can be recovered later.

Private `/dm @{Amber Badger} message` uses the same unique alias. Both participants
must already have assigned group-author aliases, and the sender must currently
belong to the recipient alias's owning set. Knowing an alias does not authorize
DM delivery. See [private messaging](docs/PRIVATE_MESSAGING.md).
