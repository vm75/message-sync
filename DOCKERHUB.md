# message-sync

[![GitHub Repository](https://img.shields.io/badge/github-vm75%2Fmessage--sync-blue?style=flat-square&logo=github)](https://github.com/vm75/message-sync)
[![Docker Pulls](https://img.shields.io/docker/pulls/vm75/message-sync?style=flat-square&logo=docker)](https://hub.docker.com/r/vm75/message-sync)
[![Docker Image Size](https://img.shields.io/docker/image-size/vm75/message-sync/latest?style=flat-square&logo=docker)](https://hub.docker.com/r/vm75/message-sync)
[![Platforms](https://img.shields.io/badge/platforms-linux%2Famd64%20%7C%20linux%2Farm64-326CE5?style=flat-square&logo=linux)](https://hub.docker.com/r/vm75/message-sync)
[![Go Version](https://img.shields.io/badge/go-1.25%2B-00ADD8?style=flat-square&logo=go)](https://go.dev/)
[![Privacy](https://img.shields.io/badge/privacy-zero%20PII%2FPHI-success?style=flat-square&logo=shield)](https://github.com/vm75/message-sync#privacy-model)
[![Security](https://img.shields.io/badge/container-rootless%20%2F%20non--root-blueviolet?style=flat-square)](https://github.com/vm75/message-sync#rootless-podman)

The release workflow publishes the same multi-architecture `message-sync` image to:

- Docker Hub: [`docker.io/vm75/message-sync`](https://hub.docker.com/r/vm75/message-sync)
- GitHub Container Registry: `ghcr.io/vm75/message-sync`

Use the [README](README.md) for product setup and provider requirements. This page is limited to image behavior, runtime configuration, and publishing.

## Tags and platforms

- The exact value in [`VERSION`](VERSION) is the immutable release tag.
- `latest` points to the release most recently published by the workflow.
- Release images target `linux/amd64` and `linux/arm64`.

Development builds use the `development` version string and are not published by the release workflow.

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

The repository's [`compose.yml`](compose.yml) builds `message-sync:dev` locally. To use the published image, remove its `build` block and set:

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

Optional membership mail and advisory-analysis variables are documented in the [README configuration table](README.md#configuration). Discord and Telegram bot tokens are pasted into the authenticated Web UI and encrypted in `control.db`; do not place them in image environment variables.

The service exposes HTTP on the configured port and requires one persistent writable mount at `/data`. That mount contains:

- `sync.db` — PII-free routing/configuration state;
- `control.db` — sensitive accounts, sessions, encrypted credentials, audit, and membership state;
- `whatsapp/<connection-id>.db` — isolated sensitive whatsmeow protocol state;
- `membership-evidence/` — short-lived private membership evidence when that feature is used.

Do not publish, inspect as application data, or expose these files through another service. Back up `/data` before upgrades; the current code initializes fresh schemas and does not provide an upgrade migration path for older development databases.

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

`.github/workflows/release-images.yml` runs only when `VERSION` changes on `main`. It validates the version, builds both supported platforms once, and publishes the version and `latest` tags to Docker Hub and GHCR.

The GitHub repository must provide `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` secrets. GHCR authentication uses the workflow `GITHUB_TOKEN` with `contents: read` and `packages: write`; package visibility is managed in repository/package settings. No registry credentials belong in the repository.
