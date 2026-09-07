# message-sync

[![Release images](https://img.shields.io/github/actions/workflow/status/vm75/message-sync/release-images.yml?branch=main&label=release%20images&style=flat-square&logo=githubactions)](https://github.com/vm75/message-sync/actions/workflows/release-images.yml)
[![Docker pulls](https://img.shields.io/docker/pulls/vm75/message-sync?style=flat-square&logo=docker)](https://hub.docker.com/r/vm75/message-sync)
[![Go 1.25+](https://img.shields.io/badge/go-1.25%2B-00ADD8?style=flat-square&logo=go)](https://go.dev/)
[![MIT license](https://img.shields.io/badge/license-MIT-yellow.svg?style=flat-square)](LICENSE)

`message-sync` is a self-hosted, privacy-first service that synchronizes configured WhatsApp groups, Discord channels, and Telegram groups or supergroups. Multiple accounts and bots can participate in alias-based sync sets while a single transport-neutral router owns message identity, fan-out, and lifecycle behavior.

## Capabilities

- Bidirectional text and transient media forwarding across mixed-transport sync sets.
- Best-effort native replies and reactions, plus edit and delete propagation.
- Native polls where destination semantics are representable, deterministic text fallback otherwise, aggregate-only live-result companions, and an on-demand aggregate summary via the `aggregate-response` reply trigger.
- Discord thread/forum and Telegram topic lineage without making child conversations configurable endpoints.
- Independent ordered delivery lanes, bounded retry and recovery, restart-safe pending work, and ambiguity-safe create handling.
- Dynamic WhatsApp, Discord, and Telegram connections managed through an authenticated embedded Web UI.
- Optional source-local message prefix and WhatsApp chat cleanup.
- Multi-user administration and human-authorized membership intake/review in a separate sensitive control plane.
- Static non-root container suitable for Docker, rootful Podman, and rootless Podman.

Provider APIs impose some limits: recovery cannot reconstruct every offline lifecycle event, Telegram cannot provide complete live results for arbitrary human-created polls, and a provider create with an ambiguous outcome requires replay or operator reconciliation instead of a blind retry. See [Architecture](ARCHITECTURE.md) for the precise boundaries.

## Privacy model

The core routing boundary is deliberately content-free at rest:

- `/data/sync.db` and application logs contain no message bodies, media, participant identities, human-readable provider names, credentials, or raw provider objects/errors.
- User identity crossing a transport boundary is HMAC-derived from `IDENTITY_SECRET`; display names used for attribution remain transient.
- Message media is held only long enough to forward and is not persisted by the router.
- `/data/whatsapp/<connection-id>.db` is isolated sensitive whatsmeow protocol state and is never queried for application features.
- `/data/control.db` is the explicit sensitive exception for accounts, sessions, audit records, encrypted Discord/Telegram credentials, and membership verification.
- Membership evidence is stored privately under `/data/membership-evidence/`, removed on final approve/reject decisions, and subject to bounded cleanup.

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

Create a bot with BotFather, disable Bot Privacy Mode, add it to target groups, and make it an administrator when per-user reaction updates are required. Disable anonymous reactions in those groups. Telegram discovery is observation-based, so send a message after adding the bot before refreshing discovered chats. Broadcast channels are not supported.

See the [manual testing guide](docs/TESTING_GUIDE.md) for detailed provider setup and end-to-end checks.

## Configuration

Deployment settings come from the environment; routing and feature settings are stored in `sync.db` and edited through the authenticated UI/API.

| Variable | Required | Default | Purpose |
|---|---:|---|---|
| `IDENTITY_SECRET` | yes | none | HMAC identity and credential-encryption root secret; minimum 32 bytes. |
| `DATA_DIR` | no | `/data` | Directory containing all SQLite state and private evidence. |
| `PORT` | no | `8080` | HTTP port when `API_ADDR` is unset. |
| `API_ADDR` | no | derived from `PORT` | Full HTTP listen address, mainly useful for direct source runs and tests. |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, or `error`. |
| `WHATSAPP_DEVICE_NAME` | no | `message-sync` | Companion device name shown in WhatsApp Linked Devices. |
| `VERIFICATION_MAIL_API_KEY` | no | none | Enables Resend-compatible membership email delivery with `VERIFICATION_MAIL_FROM`. |
| `VERIFICATION_MAIL_FROM` | no | none | Sender used by optional membership email delivery. |
| `VERIFICATION_PUBLIC_BASE_URL` | no | request origin | External base URL used in membership links behind a proxy. |
| `OPENROUTER_API_KEY` | no | none | Enables advisory image evidence analysis only when training is explicitly disabled. |
| `OPENROUTER_ALLOW_TRAINING` | no | unset | Must equal `false` for OpenRouter analysis to run. |
| `OPENROUTER_MODEL` | no | `openrouter/free` | Model used by optional advisory analysis. |
| `MESSAGE_SYNC_DATA_DIR` | no | `./data` | Host-side `/data` bind source used by `compose.yml`; it is not read by the service. |

Discord and Telegram tokens are configured dynamically, encrypted with AES-256-GCM in `control.db`, and never belong in `.env`.

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
- [Container images](DOCKERHUB.md)
- [Feature comparison](docs/FEATURE_COMPARISON.md)
- [Contributor and coding-agent guide](AGENTS.md)
- [Release history](CHANGELOG.md)

## Disclaimer

This independent project is not affiliated with WhatsApp, Meta, Discord, or Telegram. Automated and unofficial-client use may be subject to provider terms and account restrictions; operate it at your own risk.

## License

[MIT](LICENSE)
