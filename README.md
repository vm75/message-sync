# message-sync

[![Build & Publish](https://img.shields.io/github/actions/workflow/status/vm75/message-sync/release-images.yml?branch=main&label=build&style=flat-square&logo=githubactions)](https://github.com/vm75/message-sync/actions)
[![Go Version](https://img.shields.io/badge/go-1.25%2B-00ADD8?style=flat-square&logo=go)](https://go.dev/)
[![Docker Pulls](https://img.shields.io/docker/pulls/vm75/message-sync?style=flat-square&logo=docker)](https://hub.docker.com/r/vm75/message-sync)
[![Docker Image Size](https://img.shields.io/docker/image-size/vm75/message-sync/latest?style=flat-square&logo=docker)](https://hub.docker.com/r/vm75/message-sync)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg?style=flat-square)](LICENSE)
[![Platforms](https://img.shields.io/badge/platforms-linux%2Famd64%20%7C%20linux%2Farm64-326CE5?style=flat-square&logo=linux)](https://github.com/vm75/message-sync)
[![Privacy](https://img.shields.io/badge/privacy-zero%20PII%2FPHI-success?style=flat-square&logo=shield)](ARCHITECTURE.md#privacy-invariants)
[![Security](https://img.shields.io/badge/container-rootless%20%2F%20non--root-blueviolet?style=flat-square)](Containerfile)

`message-sync` is a simple server to sync messages between multiple messaging channels. End-to-end routing supports WhatsApp groups and configured Discord channels through one transport-neutral canonical router; Discord discovery/UI and richer thread/forum semantics are still being added incrementally.

*Note: This project is inspired by earlier explorations and prototypes in multi-platform message synchronization and bridging.*

## Features

- **Multi-Group Synchronization**: Seamlessly connect multiple WhatsApp groups into unified sync sets.
- **Rich Media Support**: Forwards text, images, videos, audio/voice notes, documents, and stickers.
- **Native WhatsApp Polls**: Syncs polls and aggregates votes across all connected groups.
- **Reactions & Replies**: Preserves clickable native reply structures and message reactions across groups.
- **Message Edits & Deletions**: Automatically propagates edits and deleted/revoked messages.
- **Automated Chat Cleanup**: Optional daily message clearing for connected groups on the sync account to keep device storage lean.
- **Embedded Web UI**: Simple, zero-dependency management console to configure groups and sync sets directly from your browser.
- **Hardened Security**: Runs as a static, non-root binary in read-only containers.

## Privacy Model

`message-sync` is built with a strict privacy-first architecture. It guarantees that no personal data is ever logged or persisted to the application database.

- **Zero PII/PHI**: The application database (`sync.db`) never stores participant phone numbers/JIDs, participant names, message bodies, media, or contact cards. Configured transport endpoint IDs are stored only as the minimum operational addressing needed to reach an endpoint; human-readable remote names are not stored.
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

If your configuration contains Discord endpoints, also configure a Discord bot credential. Use one source only:

```sh
# Environment source (automatically passed by the repository's env_file setup)
echo "DISCORD_BOT_TOKEN=your-bot-token" >> .env

# Or mount a secret file into the container and set its in-container path:
# DISCORD_BOT_TOKEN_FILE=/run/secrets/discord_bot_token
```

The Discord application must have the **Guild Messages** and **Message Content** gateway intents needed for channel message ingestion, plus permission to read/send messages, add reactions, and **Manage Webhooks** in each configured destination channel. `message-sync` finds or creates one bridge-managed incoming webhook per configured Discord channel and reuses it across source users and restarts. Bot tokens and webhook credentials are never stored in `sync.db`.

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
4. Configure your groups and sync sets right in the web console!

## Resetting Admin Password

If you forget the admin password, you can clear it to set up a new one on your next visit:

```sh
docker exec -it message-sync sqlite3 /data/sync.db "UPDATE global_config SET admin_password_hash = '' WHERE id = 1;"
```

## Management API compatibility

The authenticated management API now has transport-neutral endpoint CRUD at `/api/endpoints`. Endpoint records contain only `alias`, `transport`, `remoteId`, and optional `syncSetId`; transport credentials are configured separately and are never accepted by endpoint CRUD.

Existing `/api/groups` routes remain available as WhatsApp-only compatibility wrappers using the existing `jid` payload shape. Sync-set payloads continue to use the `groups` field name for compatibility, but those values are endpoint aliases and may refer to WhatsApp or Discord endpoints.

The Discord adapter now provides bidirectional text/media synchronization plus replies, reactions, edits, and deletes through the same canonical/message-copy model used by WhatsApp. WhatsApp → Discord messages use a single reusable bridge-managed webhook per destination channel: the transient WhatsApp display name is rendered as the webhook APP username, with the HMAC actor ID as fallback, while the message body remains separate. Discord attachment bytes and CDN URLs stay transient and are bounded by the configured media size limit. Discord's incoming-webhook API does not support `message_reference`, so a mapped reply emits a minimal bot-authored native reply marker and sends the actual bridged content under the sender-specific webhook APP identity; if the destination copy is unavailable, the content uses an alias-based textual reply fallback instead. Discovery/UI, threads/forums, polls, and richer Discord format semantics remain follow-up work.

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
- `DOCKERHUB.md` — Information related to the published container images.
- `docs/ASPIRATIONAL_FEATURES.md` — Future features (Discord support, cloud integrations, etc.).
- `AGENTS.md` — Instructions for AI agents and code contributors.

## Disclaimers

- **Non-Affiliation**: This project is an independent open-source tool and is not affiliated, associated, authorized, endorsed by, or in any way officially connected with WhatsApp, Meta Platforms, Inc., or any of their subsidiaries or affiliates.
- **Terms of Service**: Automated interactions and unofficial clients are subject to WhatsApp's Terms of Service. Use this tool responsibly and at your own risk. The maintainers assume no liability for any account restrictions, bans, or service disruptions.

## License

This project is licensed under the [MIT License](LICENSE).
