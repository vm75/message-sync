# message-sync

[![GitHub Repository](https://img.shields.io/badge/github-vm75%2Fmessage--sync-blue?style=flat-square&logo=github)](https://github.com/vm75/message-sync)
[![Docker Pulls](https://img.shields.io/docker/pulls/vm75/message-sync?style=flat-square&logo=docker)](https://hub.docker.com/r/vm75/message-sync)
[![Docker Image Size](https://img.shields.io/docker/image-size/vm75/message-sync/latest?style=flat-square&logo=docker)](https://hub.docker.com/r/vm75/message-sync)
[![Platforms](https://img.shields.io/badge/platforms-linux%2Famd64%20%7C%20linux%2Farm64-326CE5?style=flat-square&logo=linux)](https://hub.docker.com/r/vm75/message-sync)
[![Go Version](https://img.shields.io/badge/go-1.25%2B-00ADD8?style=flat-square&logo=go)](https://go.dev/)
[![Privacy](https://img.shields.io/badge/privacy-zero%20PII%2FPHI-success?style=flat-square&logo=shield)](https://github.com/vm75/message-sync#privacy-model)
[![Security](https://img.shields.io/badge/container-rootless%20%2F%20non--root-blueviolet?style=flat-square)](https://github.com/vm75/message-sync#rootless-podman)

`message-sync` connects WhatsApp groups, Discord channels, and Telegram groups
so people can talk across platforms. It is self-hosted and managed through a
Web UI, with support for multiple accounts and bots. Run it with Docker, rootful
Podman or **rootless Podman**.

The release workflow publishes the same multi-architecture `message-sync` image to:

- Docker Hub: [`docker.io/vm75/message-sync`](https://hub.docker.com/r/vm75/message-sync)
- GitHub Container Registry: `ghcr.io/vm75/message-sync`

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
inboxes are unmanaged. See [private messaging](https://github.com/vm75/message-sync/blob/main/docs/PRIVATE_MESSAGING.md) for reachability and privacy rules.

## Configure conversations

Use **Edit settings** on each sync set for media, recovery, retention, polls,
local-only prefixes, thread/topic labels, cleanup and membership-review visibility.
Changes reload without restarting. Edit WhatsApp linked-device names under
**Connections → Device name**.

Ordinary attribution is `group/Alice (Amber Badger)`; anonymized attribution is
`group/Amber Badger`. Enable **Anonymize outgoing** for messages sent from a
conversation and **Anonymize incoming** for deliveries there, beside its
Move/Rename/Remove controls. Both may be enabled together. Existing Discord-only
policies migrate to Incoming. WhatsApp author identity requires a verified LID;
unresolved messages fail safely and later recovery depends on provider windows.
Aliases do not provide absolute anonymity or end-to-end encryption.

See the [configuration guide](https://github.com/vm75/message-sync/blob/main/docs/CONFIGURATION.md)
for settings, defaults and provider setup.

## Tags and platforms

- A release tag `vX.Y.Z` must exactly match `VERSION=X.Y.Z` in the tagged commit.
- The validated version is published as the immutable exact image tag in both registries.
- Stable releases also move `latest`; pre-releases such as `v0.3.0-rc.1` never move `latest`.
- Release images target `linux/amd64` and `linux/arm64`.

Development builds use the `development` version string and are not published by the release workflow. Exact release tags/images are never rebuilt from another commit; fix a bad release with a new version.

## Quick start

Create a private environment file and data directory:

```sh
mkdir -p data
printf 'IDENTITY_SECRET=%s\nDATA_DIR=/data\nPORT=8080\nLOG_LEVEL=info\n' "$(openssl rand -hex 32)" > .env
chmod 600 .env
```

Run the current published release:

```sh
docker pull docker.io/vm75/message-sync:latest
docker run -d \
  --name message-sync \
  --read-only \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=64m \
  --env-file .env \
  -p 8080:8080 \
  -v "$PWD/data:/data" \
  docker.io/vm75/message-sync:latest
```

On SELinux systems, add `:Z` to the `/data` bind mount. The image already runs as the non-root `message-sync` user (UID/GID 1000); ensure the mounted directory is writable by that user in your container runtime's user namespace.

Open `http://localhost:8080`, create the first administrator, then add connections, endpoints, and sync sets in the Web UI.

## Compose

The repository's [`compose.yml`](https://github.com/vm75/message-sync/blob/main/compose.yml) builds `message-sync:dev` locally. To use the published image, remove its `build` block and set:

```yaml
services:
  message-sync:
    image: docker.io/vm75/message-sync:latest
```

Keep the repository's read-only root filesystem, dropped capabilities, `/tmp` tmpfs, environment, port, and `/data` volume settings. Then run:

```sh
docker compose up -d
```

Podman users can use `podman compose up -d` with the same file.

## Rootless Podman

The image supports Docker, rootful Podman and **rootless Podman**. Run Podman as
an ordinary user, without `sudo`, privileged mode, host namespaces or extra
capabilities. With the published-image Compose configuration above and a newly
created `data` directory:

```sh
podman unshare chown 1000:1000 data
podman compose up -d
```

The image runs as UID/GID 1000. Ensure the `data` bind mount is writable by that
user inside Podman’s user namespace; host ownership alone may not match. On
SELinux systems use `:Z`, as the repository’s Compose file does. Keep the
read-only root filesystem, dropped capabilities and bounded `/tmp` tmpfs.

## Runtime interface

The image entry point is `/usr/local/bin/message-sync`; its default command is `run`.

| Variable | Required | Default | Purpose |
|---|---:|---|---|
| `IDENTITY_SECRET` | yes | none | Identity-HMAC and credential-encryption root secret; minimum 32 bytes. |
| `DATA_DIR` | no | `/data` | Persistent state and private evidence directory. |
| `PORT` | no | `8080` | HTTP listen port when `API_ADDR` is unset. |
| `API_ADDR` | no | derived from `PORT` | Complete HTTP listen address. |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, or `error`. |
| `WHATSAPP_DEVICE_NAME` | no | `message-sync` | Companion device name shown in WhatsApp Linked Devices. |
| `VERIFICATION_MAIL_API_KEY` | no | none | Enables Resend-compatible email delivery for experimental membership verification with `VERIFICATION_MAIL_FROM`. |
| `VERIFICATION_MAIL_FROM` | no | none | Sender used by optional experimental membership email delivery. |
| `VERIFICATION_PUBLIC_BASE_URL` | no | request origin | External base URL used in experimental membership links behind a proxy. |
| `OPENROUTER_API_KEY` | no | none | Enables advisory image evidence analysis only when training is explicitly disabled. |
| `OPENROUTER_ALLOW_TRAINING` | no | unset | Must equal `false` for OpenRouter analysis to run. |
| `OPENROUTER_MODEL` | no | `openrouter/free` | Model used by optional advisory analysis. |

Discord and Telegram bot tokens are pasted into the authenticated Web UI and encrypted in `control.db`; do not place them in container environment variables.

The service exposes HTTP on the configured port and requires one persistent writable mount at `/data`. That mount contains:

- `sync.db` — PII-free routing/configuration state;
- `control.db` — sensitive accounts, sessions, encrypted credentials, audit, and experimental membership state;
- `whatsapp/<connection-id>.db` — isolated sensitive whatsmeow protocol state;
- `membership-evidence/` — short-lived private membership evidence when the experimental verification feature is used.

Do not publish, inspect as application data, or expose these files through another service. Back up `/data` before upgrades; the project does not have a general versioned migration framework, though schema changes may use narrowly scoped idempotent initialization migrations with explicit upgrade tests.

## Hardening

The final image contains the compiled binary on Alpine and runs as UID/GID 1000. It needs no privileged mode, host networking, host PID namespace, or added Linux capabilities. `/data` is the only persistent writable location; use a read-only root filesystem and the bounded `/tmp` tmpfs shown above.

## Version and configuration checks

```sh
docker run --rm docker.io/vm75/message-sync:latest version
docker run --rm \
  --env-file .env \
  -v "$PWD/data:/data" \
  docker.io/vm75/message-sync:latest validate-config
```

`validate-config` requires an existing `sync.db` with a complete valid configuration (at least two endpoints in one or more sync sets).

## Publishing

`.github/workflows/release-images.yml` runs only for pushed release tags matching `v*.*.*`. A normal push to `main`, including a commit that changes `VERSION`, does not publish anything. The workflow validates the tag against `VERSION`, runs `go test ./...` and `go vet ./...`, verifies the release-mode binary version, and only then publishes the multi-architecture images.

Stable versions publish both the exact version and `latest` to Docker Hub and GHCR. Pre-release versions publish only their exact version. The workflow refuses to overwrite an existing exact-version image, verifies the published GHCR image reports the same version, and creates the matching GitHub Release only after publishing succeeds.

The GitHub repository must provide `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` secrets. GHCR authentication uses the workflow `GITHUB_TOKEN`; the publish job receives only `contents: read` and `packages: write`, while the final GitHub Release job receives `contents: write`. No registry credentials belong in the repository.

Maintainers should follow [RELEASING.md](https://github.com/vm75/message-sync/blob/main/RELEASING.md); do not create release tags by editing this document or by rebuilding an old exact version.
