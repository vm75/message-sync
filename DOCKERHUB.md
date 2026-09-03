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
- **Telegram Bot API Transport**: Long-poll ingress, transient observed-chat discovery, text/transient media, replies/reactions/edits/deletes, safe sender attribution, forum-topic flattening, native representable polls with opaque result correlation, deterministic unsupported-poll/format fallbacks, and group-to-supergroup migration all use the shared canonical/message-copy model.
- **Rich Media Support**: Forwards text, images, videos, audio/voice notes, documents, and stickers.
- **Native WhatsApp Polls**: Syncs polls and aggregates votes across all connected groups.
- **Reactions & Replies**: Preserves clickable native reply structures and message reactions across groups.
- **Message Edits & Deletions**: Automatically propagates edits and deleted/revoked messages.
- **Automated Chat Cleanup**: Optional daily message clearing for connected groups on the sync account to keep device storage lean.
- **Source-local Messages**: Configure one optional global prefix in the authenticated Web UI to keep matching new messages local to their source conversation. Suppression is restart-safe and content-free.
- **Embedded Web UI**: Zero-dependency management console for WhatsApp pairing, Discord status/channel discovery, transient Telegram observed-chat discovery, endpoint aliases, and mixed sync sets.
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
# Optional companion device name shown in WhatsApp Linked Devices (default: message-sync)
WHATSAPP_DEVICE_NAME=
```

### Dynamic connections

Connections are managed dynamically through the authenticated Web UI:

- **WhatsApp**: Add a connection and scan the QR code (**Linked Devices** → **Link a Device**). Multiple independent WhatsApp accounts are supported; each maintains its own isolated database (`/data/whatsapp-<connection-id>.db`).
- **Discord**: Add a connection and paste the bot token once. The token is encrypted immediately with AES-256-GCM using a key derived from `IDENTITY_SECRET` and stored in `control.db`. In the Discord Developer Portal, enable the **Guild Messages** gateway intent and privileged **Message Content** intent. In bridged channels grant the bot **View Channel**, **Read Message History**, **Send Messages**, **Add Reactions**, and **Manage Webhooks**.
- **Telegram**: Add a connection and paste the Bot API token from BotFather once. The token is encrypted immediately with AES-256-GCM and stored in `control.db`. Add the bot to target groups and disable **Bot Privacy Mode** in BotFather.

No container restart is required to add, update, enable, or disable connections. Plaintext credentials are never written to `sync.db`, returned by read APIs, or logged.

### Telegram discovery & topics

Telegram discovery is observation-based because the Bot API cannot enumerate every group a bot belongs to. Send activity in the target group, then use the authenticated Web UI under the specific connection to refresh observed chats and assign a safe endpoint alias. Forum topics flatten to the configured parent alias while preserving opaque topic scope for native replies and lifecycle targeting. Over-limit media fails deterministically.

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
4. Navigate to Connections to add WhatsApp (scan QR), Discord (paste bot token), and Telegram (paste bot token) connections.
5. Discover and configure endpoints under each connection, then organize them into sync sets in the web console.

---

## Volumes & Persistence

Mount a persistent volume to `/data`:

- `/data/whatsapp-<connection-id>.db`: Per-connection sensitive protocol stores managed by `whatsmeow` (reconnect without re-pairing).
- `/data/sync.db`: Application routing state.
- `/data/control.db`: Mode-0600 sensitive account, session, audit, connection, and membership-verification state.
- `/data/membership-evidence/`: Mode-0700 directory for short-lived mode-0600 PDF/image evidence; terminal requests are pruned after 30 days.

Optional membership integrations use deployment-only environment variables
`VERIFICATION_MAIL_API_KEY` and `VERIFICATION_MAIL_FROM` for Resend-compatible
email delivery, and `OPENROUTER_API_KEY` with
`OPENROUTER_ALLOW_TRAINING=false` for advisory image analysis. The daemon and
core routing work when these variables are absent; no paid service is required.

---

## Links & Documentation

- **GitHub Repository**: [github.com/vm75/message-sync](https://github.com/vm75/message-sync)
- **Architecture & Invariants**: [ARCHITECTURE.md](https://github.com/vm75/message-sync/blob/main/ARCHITECTURE.md)
- **Testing Guide**: [docs/TESTING_GUIDE.md](https://github.com/vm75/message-sync/blob/main/docs/TESTING_GUIDE.md)
- **GHCR Image Mirror**: `ghcr.io/vm75/message-sync:latest`
