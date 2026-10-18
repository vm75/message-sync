# message-sync

[![CI](https://img.shields.io/github/actions/workflow/status/vm75/message-sync/ci.yml?branch=main&label=CI&style=flat-square&logo=githubactions)](https://github.com/vm75/message-sync/actions/workflows/ci.yml)
[![Release images](https://img.shields.io/github/actions/workflow/status/vm75/message-sync/release-images.yml?label=release%20images&style=flat-square&logo=githubactions)](https://github.com/vm75/message-sync/actions/workflows/release-images.yml)
[![Docker pulls](https://img.shields.io/docker/pulls/vm75/message-sync?style=flat-square&logo=docker)](https://hub.docker.com/r/vm75/message-sync)
[![Go 1.25+](https://img.shields.io/badge/go-1.25%2B-00ADD8?style=flat-square&logo=go)](https://go.dev/)
[![MIT license](https://img.shields.io/badge/license-MIT-yellow.svg?style=flat-square)](LICENSE)

`message-sync` connects WhatsApp groups, Discord channels, and Telegram groups
so people can talk across platforms. It is self-hosted and managed through a
Web UI, with support for multiple accounts and bots. Run it with Docker, rootful
Podman or **rootless Podman**. Release images are available from
[Docker Hub](https://hub.docker.com/r/vm75/message-sync) and
[GHCR](https://github.com/vm75/message-sync/pkgs/container/message-sync).

## Key features

- **Multi-account synchronization:** connect WhatsApp groups and community
  subgroups, Discord channels, threads and forums, and Telegram groups,
  supergroups and topics across multiple accounts or bots.
- **Message lifecycle:** forward text, images, video, voice notes, audio,
  documents and stickers, plus replies, emoji reactions, edits and deletes.
  Provider limits and text fallbacks apply.
- **Polls and live results:** native polls where supported, aggregate result
  companions, and on-demand summaries via built-in `/summary` or additional
  configured triggers. Encrypted poll presentation survives restarts outside the routing database.
- **Permanent participant aliases:** unique aliases such as `Amber Badger`
  identify authors within each sync set, survive restarts and are never reused.
  `@{Amber Badger}` references use verified destination membership for native tags.
- **Directional anonymity:** independently anonymize messages sent from
  (**Outgoing**) or delivered to (**Incoming**) each conversation, on every transport.
- **Private DM relay:** optional `/dm @{Amber Badger} message` delivery between
  verified members on WhatsApp, Discord bots and Telegram Bot API, with native
  replies and 24-hour reply mappings. Global and per-set enablement are required.
- **Local-only messages:** built-in `/local` and additional configured prefixes
  such as `!local` keep messages and their later changes out of synchronization.
- **Per-set settings:** control media limits, recovery, retention, polls and
  thread/topic label storage independently, with live reload and no restart.
- **Reliable delivery:** ordered forwarding, independent destination queues,
  bounded retries and recovery, and delivery health monitoring. Uncertain creates
  are not blindly retried.
- **Web UI and REST API:** pair accounts, discover conversations, edit sync sets
  and connection names, and manage administrators/operators, invitations and sessions.
- **Privacy boundaries:** routing state and logs exclude message content, real
  participant identities and credentials. Media remains transient; sensitive
  credentials and private routing tokens are encrypted in separate control storage.
- **Membership review (experimental):** per-set intake forms, email verification,
  private evidence, optional advisory AI analysis with training disabled, human
  review and fulfillment.
- **WhatsApp cleanup:** configurable scheduled group-chat cleanup, plus
  feature-owned private-relay cleanup using only **Delete for Me**.
- **Hardened containers:** non-root images for `linux/amd64` and `linux/arm64`,
  a read-only root filesystem, dropped capabilities and no privileged mode.
  Run with Docker, rootful Podman or **rootless Podman**.

## Quick start

You need Docker Compose or Podman Compose and access to at least two conversations
you want to connect. Create a private environment file and data directory:

```sh
mkdir -p data
printf 'IDENTITY_SECRET=%s\nDATA_DIR=/data\nPORT=8080\nLOG_LEVEL=info\n' "$(openssl rand -hex 32)" > .env
chmod 600 .env
```

Save this as `compose.yml`. It uses the published image; no source checkout or
local build is needed. You can replace `latest` with an exact released version.

```yaml
services:
  message-sync:
    image: docker.io/vm75/message-sync:latest
    env_file: .env
    ports:
      - "8080:8080"
    volumes:
      - ./data:/data:Z
    read_only: true
    cap_drop: [ALL]
    security_opt: ["no-new-privileges:true"]
    tmpfs:
      - /tmp:rw,noexec,nosuid,nodev,size=64m
    restart: unless-stopped
```

Start it with:

```sh
docker compose up -d
```

Or use `podman compose up -d` as your regular user for rootless Podman.
See [rootless Podman](#rootless-podman) for bind-mount permissions and the
[container guide](DOCKERHUB.md) for image tags and other deployment options.

Open `http://localhost:8080` and create the first administrator account.
Then:

1. Add a **connection** for each provider account or bot.
2. Discover and select conversations, giving each a safe **endpoint alias**.
3. Put two or more endpoints in a **sync set**. Messages fan out to the other
   conversations in that set.

Keep `IDENTITY_SECRET` stable and at least 32 bytes long. It protects internal
identities and stored credentials. Back up your data directory before upgrades.

## Rootless Podman

Run as your regular user, without `sudo`. For a newly created `data` directory,
set ownership inside Podman’s user namespace before starting:

```sh
podman unshare chown 1000:1000 data
podman compose up -d
```

The image runs as UID/GID 1000. Existing data must also be writable by that user
inside Podman’s namespace. No privileged mode, host namespaces or extra
capabilities are needed. The Compose file includes SELinux relabeling (`:Z`).
The quick start uses the published image with this same Compose configuration.
See the [container guide](DOCKERHUB.md#rootless-podman) for deployment details.

## Connect your accounts

| Provider | Setup |
|---|---|
| WhatsApp | Add a connection and scan its QR code under **Linked Devices → Link a Device**. |
| Discord | Add a bot token with **Message Content** intent enabled. Managed webhooks require **Manage Webhooks**; an existing webhook plus channel ID supports one channel without that permission. |
| Telegram | Use a BotFather token (**Bot API**) or a phone account (**MTProto**). Bot API discovery depends on observed activity; MTProto supports joined-group discovery and bounded history. |

Enter provider credentials in the Web UI, not `.env`. See
[Discord setup](docs/CONFIGURATION.md#discord-connection-setup) for permissions
and webhook choices, and [Telegram setup](docs/TELEGRAM.md) for bot/phone differences.
Telegram broadcast channels are unsupported. The optional private relay uses
Telegram Bot API; MTProto personal inboxes are never handled.

## Choose how messages appear

Each accepted group author gets a permanent alias within the sync set. Ordinary
attribution looks like `group/Alice (Amber Badger)`; anonymized attribution looks
like `group/Amber Badger`. Aliases survive restarts and are never reused.

In a conversation row, beside Move/Rename/Remove:

- **Anonymize outgoing** hides names on messages sent from that conversation.
- **Anonymize incoming** hides names on messages delivered to that conversation.

Enable either or both, then Save. These controls work on every transport.

To reference someone in another conversation in the same set, write
`Hi @{Amber Badger}` using the alias from their forwarded messages. Native mentions
require verified destination membership; otherwise the exact token stays visible.
Use `\@{Amber Badger}` to avoid requesting a notification. See
[message presentation](docs/MESSAGE_PRESENTATION.md) for the full rules.

## Commands

Send `/help` in a configured group for the command list and that sync set’s
additional local-only prefixes and poll triggers. The help reply stays in the
source conversation. In an enabled private relay, `/help` lists additional values
only for sets where your current membership is verified.

| Command | Use |
|---|---|
| `/help` | List commands and authorized configured prefixes/triggers. |
| `/local <message>` | Keep a group message and its later changes local. |
| `/summary` | Reply to a synchronized poll for live aggregated results. |
| `/dm @{Amber Badger} <message>` | Send privately through an enabled relay. |
| `/start` | Telegram private-chat help and bot reachability. |

`/local` and `/summary` are built in and cannot be edited or removed. Settings
add extra prefixes and triggers; they do not replace these commands. Continue
private conversations with native **Reply**, not a separate reply command.

## Private DM relay

The relay is **off by default**. An administrator must enable both
**Settings → Allow private DM relays** and the matching switch under
**Sync Sets → Edit settings**.

In a private chat with the owning WhatsApp account, Discord bot or Telegram
Bot API bot, send:

```text
/dm @{Amber Badger} Can you help with this?
```

Both people need existing group-author aliases and currently verified membership
in the alias’s sync set. Telegram recipients must first contact the same bot with
`/start`. Use native **Reply** on a relayed message to respond.

Text only, up to 1600 UTF-8 bytes per body; reply mappings expire after 24 hours.
Private edits/deletes do not propagate. WhatsApp cleanup uses only **Delete for
Me** on feature-owned sync-account copies; recipient copies may remain. Discord
and Telegram Bot API do not offer equivalent self-only cleanup. MTProto personal
inboxes are unmanaged. See [private messaging](docs/PRIVATE_MESSAGING.md) for reachability and privacy rules.

## Configure and operate

Open a sync set’s **Edit settings** to adjust media, recovery, retention, polls,
local-only prefixes, cleanup, and membership review. Changes reload without a
restart. WhatsApp device names are edited under **Connections → Device name**.

Only deployment settings belong in the environment. The
[configuration guide](docs/CONFIGURATION.md) lists environment variables, setting
names/defaults, API update behavior, and upgrade notes.

## Privacy model

Routing state and logs exclude message content, real participant identities and
credentials. Media is held only long enough to forward. Sensitive account,
protocol and control data live separately in the data directory. Optional
`friendly` thread/topic display stores labels for restart presentation; the
default `opaque` mode keeps them transient. See the
[privacy boundaries](ARCHITECTURE.md#persistence-and-trust-boundaries).

Provider limits still apply: recovery cannot restore every missed event, native
replies and mentions may fall back to text, and uncertain sends are not blindly
retried. WhatsApp author identity must resolve to a verified canonical LID;
unresolved messages fail safely and later recovery depends on provider windows.

## Development

Source builds require Go 1.25 or newer. When running outside a container, set
`DATA_DIR` to a writable local path such as `./data` in `.env`.

```sh
make run       # load .env and start the service
make fmt
make test
make vet
make build     # bin/message-sync
./bin/message-sync version
DATA_DIR=./data ./bin/message-sync validate-config
```

## More documentation

- [Configuration and Discord setup](docs/CONFIGURATION.md)
- [Container deployment](DOCKERHUB.md)
- [Telegram integrations](docs/TELEGRAM.md)
- [Message presentation and references](docs/MESSAGE_PRESENTATION.md)
- [Private messaging](docs/PRIVATE_MESSAGING.md)
- [Architecture](ARCHITECTURE.md) and [feature comparison](docs/FEATURE_COMPARISON.md)
- [Automated tests](TESTING.md) and [manual provider checks](docs/TESTING_GUIDE.md)
- [Contributor guide](AGENTS.md), [releasing](RELEASING.md), and [changelog](CHANGELOG.md)

## License and disclaimer

[MIT](LICENSE). This independent project is not affiliated with WhatsApp, Meta,
Discord, or Telegram. Provider terms and account restrictions may apply.
