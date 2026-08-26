# message-sync

[![GitHub Repository](https://img.shields.io/badge/github-vm75%2Fmessage--sync-blue?style=flat-square&logo=github)](https://github.com/vm75/message-sync)
[![Docker Pulls](https://img.shields.io/docker/pulls/vm75/message-sync?style=flat-square&logo=docker)](https://hub.docker.com/r/vm75/message-sync)
[![Docker Image Size](https://img.shields.io/docker/image-size/vm75/message-sync/latest?style=flat-square&logo=docker)](https://hub.docker.com/r/vm75/message-sync)
[![Platforms](https://img.shields.io/badge/platforms-linux%2Famd64%20%7C%20linux%2Farm64-326CE5?style=flat-square&logo=linux)](https://hub.docker.com/r/vm75/message-sync)
[![Go Version](https://img.shields.io/badge/go-1.25%2B-00ADD8?style=flat-square&logo=go)](https://go.dev/)
[![Privacy](https://img.shields.io/badge/privacy-zero%20PII%2FPHI-success?style=flat-square&logo=shield)](https://github.com/vm75/message-sync#privacy-model)
[![Security](https://img.shields.io/badge/container-rootless%20%2F%20non--root-blueviolet?style=flat-square)](https://github.com/vm75/message-sync#rootless-podman)

`message-sync` is a simple server to sync messages between multiple messaging channels. Currently, it supports syncing between multiple WhatsApp groups.

---

## Features

- **Multi-Group Synchronization**: Seamlessly connect multiple WhatsApp groups into unified sync sets.
- **Rich Media Support**: Forwards text, images, videos, audio/voice notes, documents, and stickers.
- **Native WhatsApp Polls**: Syncs polls and aggregates votes across all connected groups.
- **Reactions & Replies**: Preserves clickable native reply structures and message reactions across groups.
- **Message Edits & Deletions**: Automatically propagates edits and deleted/revoked messages.
- **Automated Chat Cleanup**: Optional daily message clearing for connected groups on the sync account to keep device storage lean.
- **Embedded Web UI**: Simple, zero-dependency management console to configure groups and sync sets directly from your browser.
- **Hardened Security**: Runs as a static, non-root binary in read-only containers.

---

## Privacy Model

`message-sync` is built with a strict privacy-first architecture. It guarantees that no personal data is ever logged or persisted to the application database.

- **Zero PII/PHI**: The application database (`sync.db`) never stores phone numbers, WhatsApp JIDs, participant names, message bodies, media, or contact cards.
- **Transient Media**: Media files are only downloaded into memory long enough to forward them to the peer groups, and are never retained on disk.
- **Anonymized Identity**: User identity is represented purely by stable, HMAC-derived hashes or configured group aliases (e.g. `c1g1`).
- **Separation of State**: The WhatsApp protocol state (`whatsapp.db`), which naturally requires some contact metadata for the connection to work, is strictly isolated and never accessed by the application logic or exposed through the API.

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
```

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
5. Configure your groups and sync sets in the web console!

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
