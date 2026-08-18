# message-sync

[![GitHub Repository](https://img.shields.io/badge/github-vm75%2Fmessage--sync-blue?style=flat-square&logo=github)](https://github.com/vm75/message-sync)
[![Docker Pulls](https://img.shields.io/docker/pulls/vm75/message-sync?style=flat-square&logo=docker)](https://hub.docker.com/r/vm75/message-sync)
[![Docker Image Size](https://img.shields.io/docker/image-size/vm75/message-sync/latest?style=flat-square&logo=docker)](https://hub.docker.com/r/vm75/message-sync)
[![Platforms](https://img.shields.io/badge/platforms-linux%2Famd64%20%7C%20linux%2Farm64-326CE5?style=flat-square&logo=linux)](https://hub.docker.com/r/vm75/message-sync)
[![Go Version](https://img.shields.io/badge/go-1.25%2B-00ADD8?style=flat-square&logo=go)](https://go.dev/)
[![Privacy](https://img.shields.io/badge/privacy-zero%20PII%2FPHI-success?style=flat-square&logo=shield)](https://github.com/vm75/message-sync#privacy-model)
[![Security](https://img.shields.io/badge/container-rootless%20%2F%20non--root-blueviolet?style=flat-square)](https://github.com/vm75/message-sync#rootless-podman)

`message-sync` is a privacy-first message synchronization service written in Go. It connects multiple WhatsApp groups into unified all-to-all synchronization sets with strict privacy guarantees, ephemeral media forwarding, and an embedded management Web UI and REST API.

---

## Key Features

- **Privacy-First Architecture**: Application persistence (`sync.db`) contains zero PII/PHI. Never logs or stores phone numbers, JIDs, message bodies, media, or contact cards.
- **Full WhatsApp Group Synchronization**: Synchronizes text, media (images, videos, audio/voice notes, documents, stickers), native WhatsApp polls, reactions, edits, and deletes.
- **Cross-Group Poll Vote Aggregation**: Tracks poll votes across synchronized groups and provides aggregated summaries upon request (`aggregate-response`).
- **Replies & Reactions**: Preserves clickable native reply structures across groups with automatic attribution fallbacks when needed.
- **Transient Media**: Media is downloaded into memory only long enough to forward to peer groups and is never retained on disk.
- **Embedded Web UI & REST API**: Includes a zero-dependency dark-mode management console served directly on port `8080`.
- **Hardened Container**: Built as a static, non-root binary (UID `10001`), supporting read-only filesystems, zero capabilities, and rootless Podman/Docker.
- **Multi-Architecture**: Official multi-arch images for `linux/amd64` and `linux/arm64`.

---

## Quick Start

### 1. Prepare Environment

Generate a 32-byte secret for HMAC identity derivation:

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
    user: "10001:10001"
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

### 3. Initial Pairing

#### Option A: Via Embedded Web UI (Recommended)

1. Start the container:
   ```sh
   docker compose up -d
   ```
2. Open `http://localhost:8080` in your browser.
3. Complete initial admin password setup.
4. Navigate to the WhatsApp pairing section, display the QR code, and scan it from WhatsApp (**Linked Devices** &rarr; **Link a Device**).
5. Configure your groups and sync sets in the web console.

#### Option B: Via Terminal CLI

Run the container interactively to scan the pairing QR directly from the terminal:

```sh
docker compose run --rm message-sync run
```

Scan the terminal QR with WhatsApp (**Linked Devices** &rarr; **Link a Device**). After pairing completes, stop with `Ctrl-C` and start in background:

```sh
docker compose up -d
```

---

## Environment Variables

| Variable | Description | Required | Default |
|---|---|---|---|
| `IDENTITY_SECRET` | 32-byte hex secret used for stable HMAC actor ID derivation | Yes | *(None)* |
| `DATA_DIR` | Directory where `/data/sync.db` and `/data/whatsapp.db` are stored | No | `/data` |
| `PORT` | Listen port mapped for Web UI and REST API in compose and native | No | `8080` |
| `API_ADDR` | Listen address/port (`[host]:port`) for advanced binds | No | `:${PORT}` |

---

## Volumes & Persistence

Mount a persistent volume to `/data`:

- `/data/whatsapp.db`: Sensitive protocol session store managed by `whatsmeow` (reconnects without re-pairing).
- `/data/sync.db`: Application routing state (safe canonical IDs, HMAC identifiers, tombstone records, and retention cursors).

---

## Links & Documentation

- **GitHub Repository**: [github.com/vm75/message-sync](https://github.com/vm75/message-sync)
- **Architecture & Invariants**: [ARCHITECTURE.md](https://github.com/vm75/message-sync/blob/main/ARCHITECTURE.md)
- **GHCR Image Mirror**: `ghcr.io/vm75/message-sync:latest`
