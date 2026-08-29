# message-sync

[![GitHub Repository](https://img.shields.io/badge/github-vm75%2Fmessage--sync-blue?style=flat-square&logo=github)](https://github.com/vm75/message-sync)
[![Docker Pulls](https://img.shields.io/docker/pulls/vm75/message-sync?style=flat-square&logo=docker)](https://hub.docker.com/r/vm75/message-sync)
[![Docker Image Size](https://img.shields.io/docker/image-size/vm75/message-sync/latest?style=flat-square&logo=docker)](https://hub.docker.com/r/vm75/message-sync)
[![Platforms](https://img.shields.io/badge/platforms-linux%2Famd64%20%7C%20linux%2Farm64-326CE5?style=flat-square&logo=linux)](https://hub.docker.com/r/vm75/message-sync)
[![Go Version](https://img.shields.io/badge/go-1.25%2B-00ADD8?style=flat-square&logo=go)](https://go.dev/)
[![Privacy](https://img.shields.io/badge/privacy-zero%20PII%2FPHI-success?style=flat-square&logo=shield)](https://github.com/vm75/message-sync#privacy-model)
[![Security](https://img.shields.io/badge/container-rootless%20%2F%20non--root-blueviolet?style=flat-square)](https://github.com/vm75/message-sync#rootless-podman)

`message-sync` is a privacy-first server for transport-neutral message synchronization across WhatsApp groups, configured Discord channels, and configured Telegram groups/supergroups through one canonical router.

---

## Features

- **Mixed-Transport Synchronization**: Connect WhatsApp groups, configured Discord channels, and configured Telegram groups/supergroups in the same alias-based sync sets.
- **Telegram Bot API Transport**: Long-poll ingress, transient observed-chat discovery, text/transient media, replies/reactions/edits/deletes, safe sender attribution, forum-topic flattening, deterministic poll/format fallbacks, and group-to-supergroup migration all use the shared canonical/message-copy model.
- **Rich Media Support**: Forwards text, images, videos, audio/voice notes, documents, and stickers.
- **Native WhatsApp Polls**: Syncs polls and aggregates votes across all connected groups.
- **Reactions & Replies**: Preserves clickable native reply structures and message reactions across groups.
- **Message Edits & Deletions**: Automatically propagates edits and deleted/revoked messages.
- **Automated Chat Cleanup**: Optional daily message clearing for connected groups on the sync account to keep device storage lean.
- **Embedded Web UI**: Zero-dependency management console for WhatsApp pairing, Discord status/channel discovery, endpoint aliases, and mixed sync sets.
- **Hardened Security**: Runs as a static, non-root binary in read-only containers.

---

## Privacy Model

`message-sync` is built with a strict privacy-first architecture. It guarantees that no personal data is ever logged or persisted to the application database.

- **Zero PII/PHI**: The application database (`sync.db`) never stores participant phone numbers/JIDs, Discord or Telegram user IDs/names, guild/channel/chat names, message bodies, captions, media, source filenames, CDN/file URLs, or contact cards. Configured transport target IDs are the narrow operational addressing exception.
- **Transient Media**: Media files are only downloaded into memory long enough to forward them to the peer groups, and are never retained on disk.
- **Anonymized Identity**: User identity is represented purely by stable, HMAC-derived hashes or configured group aliases (e.g. `c1g1`).
- **Separation of State**: The WhatsApp protocol state (`whatsapp.db`), which naturally requires some contact metadata for the connection to work, is strictly isolated and never accessed by the application logic or exposed through the API.
- **Transport Credentials**: Discord bot/webhook credentials and Telegram bot credentials come only from environment variables or mounted secrets. They are never stored in `sync.db` or written to application logs.

---

## Quick Start

### 1. Prepare Environment

Generate a 32-byte secret for anonymized identity derivation:

```sh
mkdir -p data
openssl rand -hex 32 > .identity_secret
```

Create a `.env` file:

```env
IDENTITY_SECRET=your-generated-32-byte-hex-secret
DATA_DIR=/data
PORT=8080
# Required for Discord discovery or configured Discord endpoints. Use this OR DISCORD_BOT_TOKEN_FILE.
DISCORD_BOT_TOKEN=
# For a mounted secret instead, set its in-container path and leave DISCORD_BOT_TOKEN empty.
DISCORD_BOT_TOKEN_FILE=

# Telegram Bot API credential. Use this OR TELEGRAM_BOT_TOKEN_FILE, never both.
TELEGRAM_BOT_TOKEN=
# For a mounted secret instead, set its in-container path and leave TELEGRAM_BOT_TOKEN empty.
TELEGRAM_BOT_TOKEN_FILE=
```

### Discord setup

Discord credentials are deployment-only. Set exactly one of `DISCORD_BOT_TOKEN` or `DISCORD_BOT_TOKEN_FILE`; the latter must point to a secret file you mount into the container. The Web UI never accepts bot or webhook credentials, and neither bot tokens nor managed-webhook IDs/tokens/URLs are stored in `sync.db` or retained logs.

In the Discord Developer Portal, enable the **Guild Messages** gateway intent and privileged **Message Content** intent. In each bridged channel grant the bot **View Channel**, **Read Message History**, **Send Messages**, **Add Reactions**, and **Manage Webhooks**.

The gateway bot handles Discord ingress, discovery, native reply markers, reactions, and connection lifecycle. For WhatsApp → Discord outbound messages, `message-sync` finds or creates **one bridge-managed incoming webhook per configured channel** and reuses it across participants and restarts. The transient WhatsApp display/push name becomes that message's Discord APP/webhook username; if no display name is available, the HMAC actor ID is used. These APP labels are not real Discord accounts, and no Discord account or webhook is created per WhatsApp participant. Display names remain transient and are never persisted or logged.

### Telegram setup

Create the Telegram bot with BotFather and set exactly one of `TELEGRAM_BOT_TOKEN` or `TELEGRAM_BOT_TOKEN_FILE`; mounted-secret mode must point to an in-container secret path. The token never enters endpoint CRUD, `sync.db`, or retained logs.

Add the bot to each intended Telegram group/supergroup. To receive ordinary group messages, disable **Bot Privacy Mode** through BotFather or grant the bot the administrator visibility required for your deployment. The bridge does not bypass Telegram platform visibility rules.

The Telegram adapter uses Bot API **long polling**. It accepts only configured group/supergroup chats, drops private/unconfigured chats and bridge-bot echoes, HMAC-normalizes Telegram user IDs immediately, and keeps names/text/captions transient. The pinned Bot API client retries transient `getUpdates` failures with bounded backoff and honors `retry_after`; the adapter additionally rejects duplicate/older update IDs in-process and shuts polling down with the application context.

Telegram discovery is observation-based because the Bot API cannot enumerate every group a bot belongs to. Send activity in the target group, then use the authenticated Web UI to refresh observed chats and assign a safe alias. Titles/usernames remain in a bounded in-memory cache only; only the selected opaque chat ID becomes endpoint `remote_id`.

Telegram text/media plus replies, reactions, edits, and deletes use the same canonical/message-copy lifecycle as WhatsApp and Discord. WhatsApp/Discord senders are rendered in Telegram content with a transient display name or HMAC fallback; Telegram sender display identity can flow transiently to Discord's existing managed-webhook APP rendering. Forum topics flatten to the configured parent alias, basic-group to supergroup migration updates only endpoint addressing, and polls use deterministic text instead of a separate vote-state system. Contacts/locations and unsupported service-only payloads are ignored. Hosted Bot API uploads are conservatively limited to 10 MiB photos, 50 MiB general files, and Telegram's tighter sticker format caps; over-limit media fails deterministically.

### 2. Docker Compose / Podman Compose

Create a `compose.yml` file:

```yaml
services:
  message-sync:
    container_name: message-sync
    image: docker.io/vm75/message-sync:latest
    read_only: true
    cap_drop:
      - ALL
    security_opt:
      - no-new-privileges:true
    tmpfs:
      - /tmp:rw,noexec,nosuid,nodev,size=64m
    env_file:
      - .env
    environment:
      - DATA_DIR=${DATA_DIR:-/data}
      - IDENTITY_SECRET=${IDENTITY_SECRET}
      - PORT=${PORT:-8080}
      - LOG_LEVEL=${LOG_LEVEL:-info}
    ports:
      - "${PORT:-8080}:${PORT:-8080}"
    volumes:
      - ${MESSAGE_SYNC_DATA_DIR:-./data}:/data:Z,U
    restart: unless-stopped
    stop_grace_period: 20s
```

### 3. Setup and Pairing

1. Start the container:
   ```sh
   docker compose up -d
   ```
2. Open `http://localhost:8080` in your browser.
3. Complete initial admin password setup.
4. Navigate to the WhatsApp pairing section, display the QR code, and scan it from WhatsApp (**Linked Devices** → **Link a Device**).
5. Discover/configure Discord channels and transiently observed Telegram groups with safe aliases, then configure mixed WhatsApp/Discord/Telegram sync sets in the web console.

---

## Volumes & Persistence

Mount a persistent volume to `/data`:

- `/data/whatsapp.db`: Sensitive protocol session store managed by `whatsmeow` (reconnects without re-pairing).
- `/data/sync.db`: Application routing state.

---

## Links & Documentation

- **GitHub Repository**: [github.com/vm75/message-sync](https://github.com/vm75/message-sync)
- **Architecture & Invariants**: [ARCHITECTURE.md](https://github.com/vm75/message-sync/blob/main/ARCHITECTURE.md)
- **GHCR Image Mirror**: `ghcr.io/vm75/message-sync:latest`
