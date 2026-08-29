# message-sync

[![Build & Publish](https://img.shields.io/github/actions/workflow/status/vm75/message-sync/release-images.yml?branch=main&label=build&style=flat-square&logo=githubactions)](https://github.com/vm75/message-sync/actions)
[![Go Version](https://img.shields.io/badge/go-1.25%2B-00ADD8?style=flat-square&logo=go)](https://go.dev/)
[![Docker Pulls](https://img.shields.io/docker/pulls/vm75/message-sync?style=flat-square&logo=docker)](https://hub.docker.com/r/vm75/message-sync)
[![Docker Image Size](https://img.shields.io/docker/image-size/vm75/message-sync/latest?style=flat-square&logo=docker)](https://hub.docker.com/r/vm75/message-sync)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg?style=flat-square)](LICENSE)
[![Platforms](https://img.shields.io/badge/platforms-linux%2Famd64%20%7C%20linux%2Farm64-326CE5?style=flat-square&logo=linux)](https://github.com/vm75/message-sync)
[![Privacy](https://img.shields.io/badge/privacy-zero%20PII%2FPHI-success?style=flat-square&logo=shield)](ARCHITECTURE.md#privacy-invariants)
[![Security](https://img.shields.io/badge/container-rootless%20%2F%20non--root-blueviolet?style=flat-square)](Containerfile)

`message-sync` is a simple server to sync messages between multiple messaging channels. End-to-end routing supports WhatsApp groups, configured Discord channels, and configured Telegram groups/supergroups through one transport-neutral canonical router. The authenticated Web UI provides Discord channel discovery plus transient Telegram observed-chat discovery without persisting human-readable remote names.

*Note: This project is inspired by earlier explorations and prototypes in multi-platform message synchronization and bridging.*

## Features

- **Mixed-Transport Synchronization**: Connect WhatsApp groups, configured Discord channels, and configured Telegram groups/supergroups in the same alias-based sync sets.
- **Rich Media Support**: Forwards text, images, videos, audio/voice notes, documents, and stickers.
- **Native WhatsApp Polls**: Syncs polls and aggregates votes across all connected groups.
- **Reactions & Replies**: Preserves clickable native reply structures and message reactions across groups.
- **Message Edits & Deletions**: Automatically propagates edits and deleted/revoked messages.
- **Automated Chat Cleanup**: Optional daily message clearing for connected groups on the sync account to keep device storage lean.
- **Embedded Web UI**: Simple, zero-dependency management console to inspect WhatsApp/Discord/Telegram status, discover Discord channels and transiently observed Telegram groups, configure transport-neutral endpoint aliases, and manage mixed sync sets directly from your browser.
- **Hardened Security**: Runs as a static, non-root binary in read-only containers.

## Privacy Model

`message-sync` is built with a strict privacy-first architecture. It guarantees that no personal data is ever logged or persisted to the application database.

- **Zero PII/PHI**: The application database (`sync.db`) never stores participant phone numbers/JIDs, Discord or Telegram user names/IDs, guild/channel/chat names, message bodies, media, source filenames, CDN/file URLs, or contact cards. Configured transport endpoint IDs are stored only as the minimum operational addressing needed to reach an endpoint; human-readable remote names are not stored.
- **Transient Media**: Media files are only downloaded into memory long enough to forward them to the peer groups, and are never retained on disk.
- **Anonymized Identity**: User identity is represented purely by stable, HMAC-derived hashes or configured group aliases (e.g. `c1g1`).
- **Separation of State**: The WhatsApp protocol state (`whatsapp.db`), which naturally requires some contact metadata for the connection to work, is strictly isolated and never accessed by the application logic or exposed through the API.

## Quick Start

The easiest way to run the server is using Docker or Podman Compose.

### 1. Preparation

Generate a secure secret for user anonymization and create an environment file:

```sh
mkdir -p data
echo "IDENTITY_SECRET=$(openssl rand -hex 32)" > .env
echo "DATA_DIR=/data" >> .env
echo "PORT=8080" >> .env
```

If you want Discord channel discovery or your configuration contains Discord endpoints, also configure a Discord bot credential. Use one source only:

```sh
# Environment source (automatically passed by the repository's env_file setup)
echo "DISCORD_BOT_TOKEN=your-bot-token" >> .env

# Or mount a secret file into the container and set its in-container path:
# DISCORD_BOT_TOKEN_FILE=/run/secrets/discord_bot_token
```

The Discord application must enable the **Guild Messages** gateway intent and the privileged **Message Content** intent in the Discord Developer Portal. In each channel you want to bridge, grant the bot **View Channel**, **Read Message History**, **Send Messages**, **Add Reactions**, and **Manage Webhooks**. `message-sync` finds or creates one bridge-managed incoming webhook per configured Discord channel and reuses it across source users and restarts.

The Web UI never accepts a Discord token. Configure `DISCORD_BOT_TOKEN` or `DISCORD_BOT_TOKEN_FILE` at deployment time and restart the service. When a token source is configured, the Discord gateway starts even before the first Discord endpoint exists so the authenticated admin UI can discover guild text/announcement channels. Guild and channel display names returned by discovery are transient response/UI data only; only the selected channel ID is persisted as the endpoint's operational `remote_id`. Bot tokens and managed-webhook credentials are never stored in `sync.db`.

If **Manage Webhooks** is missing, Discord ingress/discovery can remain connected but the admin status reports the affected endpoint alias as `missing_permission`; grant **Manage Webhooks** in that destination channel and refresh. Webhook IDs, URLs, and tokens are never exposed by the management API.

The Telegram Bot API adapter uses **long polling** and reads its credential only from one deployment source:

```sh
# Environment source
echo "TELEGRAM_BOT_TOKEN=your-bot-token" >> .env

# Or mount a secret file and set its in-container path:
# TELEGRAM_BOT_TOKEN_FILE=/run/secrets/telegram_bot_token
```

Create the bot with **BotFather**, add it to each intended Telegram group/supergroup, and ensure it can receive the messages you intend to synchronize. For ordinary group messages, disable **Bot Privacy Mode** through BotFather or grant the bot the administrator visibility required by your deployment. The admin status derives Bot Privacy Mode readiness from Telegram's safe `getMe` capability flag when the probe succeeds; it retains only the boolean state, never the returned bot user object. If the probe is unavailable, the UI falls back to fixed operator guidance.

Telegram discovery is observation-based because the Bot API cannot enumerate every group a bot belongs to. After the bot is added and has suitable visibility, send a message in the target group/supergroup and open **Telegram → Refresh Observed Chats** in the authenticated Web UI. Observed chat titles/usernames live only in a bounded in-memory cache and disappear on process restart; only the selected opaque negative chat ID is persisted as endpoint `remote_id`. The browser never accepts or stores the bot token.

### 2. Start the Server

Create a `compose.yml` file (see [compose.yml](compose.yml) in this repository for an example) and start the server:

```sh
docker compose up -d
```
*(If using Podman, simply replace `docker` with `podman`)*

### 3. Setup and Pairing

1. Open `http://localhost:8080` in your web browser.
2. Complete the initial admin password setup.
3. Go to the WhatsApp pairing section, display the QR code, and scan it from WhatsApp on your phone (**Linked Devices** → **Link a Device**).
4. Configure WhatsApp, Discord, and Telegram endpoints and sync sets in the web console. For Telegram, send a group message first so the Bot API observation cache can discover the chat.

## Resetting Admin Password

If you forget the admin password, you can clear it to set up a new one on your next visit:

```sh
docker exec -it message-sync sqlite3 /data/sync.db "UPDATE global_config SET admin_password_hash = '' WHERE id = 1;"
```

## Management API & Administration

The authenticated management API provides transport-neutral endpoint CRUD at `/api/endpoints`. Endpoint records contain only `alias`, `transport`, `remoteId`, and optional `syncSetId`; transport credentials are configured separately and are never accepted by endpoint CRUD. The configuration model accepts `whatsapp`, `discord`, and `telegram`; Telegram endpoint `remoteId` values are negative Bot API group/supergroup chat IDs. Runtime configuration reload updates routing targets across all active transports without restart.

- **Endpoint Management**: `GET`, `POST`, `PUT`, `DELETE` at `/api/endpoints`.
- **Discord Administration**: `GET /api/discord/status` reports safe connection/webhook-readiness state; `GET /api/discord/channels` provides on-demand live discovery of guild text/announcement channels.
- **Telegram Administration**: `GET /api/telegram/status` reports safe token-source/long-poll/endpoint-readiness and Bot Privacy Mode status; `GET /api/telegram/chats` returns the bounded in-memory list of observed groups/supergroups.
- **Backward Compatibility**: Existing `/api/groups` routes remain available as WhatsApp-only compatibility wrappers using the legacy `jid` payload shape. Sync-set payloads continue to use the `groups` field name for compatibility, but those values are endpoint aliases and may refer to WhatsApp, Discord, or Telegram endpoints.

Transport credentials, tokens, and raw protocol update payloads are never returned by the API or persisted in `sync.db`.

## Transport Adapters & Semantics

### Discord Transport

The Discord adapter provides bidirectional text/media synchronization plus replies, reactions, edits, deletes, authenticated channel discovery, and safe managed-webhook readiness through the same canonical/message-copy model used by WhatsApp and Telegram:

- **Gateway Bot & Webhooks**: The **gateway bot** owns Discord ingress, discovery, reply markers, reactions, and connection lifecycle; the **bridge-managed incoming webhook** owns bridged outbound message rendering. Messages toward Discord reuse one managed webhook per destination channel: the sender's transient display name is supplied only as that message's webhook APP username, with the HMAC actor ID as fallback, while the message body remains separate. An APP/webhook username is Discord presentation metadata—not a real Discord user account—and the bridge never creates a Discord account or webhook per participant.
- **Replies**: Discord's incoming-webhook API does not support `message_reference`, so a mapped reply emits a minimal bot-authored native reply marker and sends the actual bridged content under the sender-specific webhook APP identity; if the destination copy is unavailable, the content uses an alias-based textual reply fallback instead.
- **Thread & Forum Flattening**: Discord thread messages are flattened to the already-configured parent channel alias using live gateway state; no thread/post endpoint is created or persisted. Replies, reactions, edits, and deletes from such threads therefore use the same parent alias and ordinary remote-copy message IDs. Forum-post threads follow the same ingress rule when their parent forum channel has already been configured. The admin discovery UI continues to expose only text/announcement channels and the bridge does not dynamically create Discord forum posts.
- **Polls, Stickers & Mentions**: WhatsApp/native polls sent to Discord use a deterministic text representation (question plus numbered options); native Discord polls, embed/component-only messages, system messages, and native Discord sticker-only events are ignored rather than creating ambiguous canonical content. WhatsApp stickers sent to Discord are forwarded as transient `sticker.webp` attachments. Discord user mentions are rendered as transient display text or an HMAC fallback, while role/channel mentions use generic text fallbacks; raw Discord member, role, and channel IDs never cross the adapter boundary.
- **Media**: Discord attachment bytes and CDN URLs stay transient and are bounded by the configured media size limit.

### Telegram Transport

The Telegram Bot API adapter provides bidirectional text/media synchronization plus replies, reactions, edits, deletes, and bounded transient chat discovery through the same canonical router loop and message-copy model:

- **Long Polling & Reliability**: The pinned Telegram Bot API client retries transient `getUpdates` failures with bounded backoff (including Telegram `retry_after` responses), while the adapter rejects duplicate or older update IDs within the running process. Shutdown cancels long polling and waits for the polling worker to exit.
- **Sender Attribution**: Cross-transport delivery uses transient sender attribution where Telegram cannot impersonate another platform's participant, while Telegram sender identity remains HMAC-normalized at the adapter boundary.
- **Forum-Topic Flattening**: Telegram forum-topic messages are flattened to the configured parent group/supergroup endpoint alias; topic IDs and topic names are never persisted and do not create endpoints. Ordinary bridged outbound messages target the configured parent/general chat context, while mapped replies continue to use Telegram's ordinary reply-to message reference without storing a thread mapping.
- **Group to Supergroup Migration**: Basic-group to supergroup migration service messages transactionally replace only that endpoint's opaque `remote_id`, preserving its alias and sync-set membership; migration logs contain only the safe alias and fixed event class.
- **Polls, Mentions & Formats**: WhatsApp/native poll content sent to Telegram is rendered as deterministic text (question, numbered options, and selection guidance), and Telegram polls are likewise normalized to deterministic text before entering the canonical router, avoiding duplicate poll/vote state machines. Telegram `text_mention` entities render a transient display label when allowed or the existing HMAC actor ID otherwise; username-only mentions use the generic `@mention` fallback so raw usernames do not cross the adapter boundary.
- **Media Limits**: Hosted Bot API media handling enforces 10 MiB for photos, 50 MiB for general uploads, and Telegram sticker format caps (512 KiB static WebP, 64 KiB TGS, 256 KiB WebM), further bounded by the configured media limit. Over-limit media fails deterministically without requiring a Local Bot API server. Contacts, locations/venues, and unsupported service-only payloads are ignored.

## Local Development

If you'd like to build and test the project locally from source:

```sh
make fmt test vet build
```

Run the server attached to inspect local events or pair via terminal:

```sh
IDENTITY_SECRET="$(openssl rand -hex 32)" DATA_DIR=./data go run ./cmd/message-sync run
```

## Documentation & Architecture

- `ARCHITECTURE.md` — In-depth overview of the architecture, data boundaries, event flows, and restart semantics.
- `docs/TESTING_GUIDE.md` — Complete step-by-step testing guide for WhatsApp, Discord, and Telegram.
- `DOCKERHUB.md` — Information related to the published container images.
- `docs/ASPIRATIONAL_FEATURES.md` — Deferred extensions and future capabilities beyond the implemented Discord and Telegram transports.
- `AGENTS.md` — Instructions for AI agents and code contributors.

## Disclaimers

- **Non-Affiliation**: This project is an independent open-source tool and is not affiliated, associated, authorized, endorsed by, or in any way officially connected with WhatsApp, Meta Platforms, Inc., or any of their subsidiaries or affiliates.
- **Terms of Service**: Automated interactions and unofficial clients are subject to WhatsApp's Terms of Service. Use this tool responsibly and at your own risk. The maintainers assume no liability for any account restrictions, bans, or service disruptions.

## License

This project is licensed under the [MIT License](LICENSE).
