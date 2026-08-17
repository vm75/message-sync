# message-sync

Privacy-first message synchronization service implemented in Go. The MVP uses `tulir/whatsmeow` for WhatsApp transport, JSON configuration, SQLite runtime state, and a transport-neutral canonical message model that can support Discord and other adapters after MVP.

## Status

Phase 0 of the Go MVP is implemented: strict JSON config loading/defaults/validation, HMAC identity, the PII/PHI-free `sync.db` schema and repositories, privacy-safe structured error logging, and the rootless container baseline are in place. WhatsApp connectivity begins in Phase 1.

There is deliberately **no `VERSION` file** during MVP development. Builds report `development`. Adding or changing `VERSION` on `main` is the sole trigger for the container publication workflow.

## MVP boundary

The MVP will:

- synchronize configured WhatsApp groups in all-to-all sync sets;
- use aliases such as `c1g1` rather than group subjects for provenance;
- prefix forwarded content as `<group-alias>/<username>`;
- use transient push names or HMAC-derived user IDs without persisting participant identity;
- forward text, images, videos, documents, audio/voice messages, stickers, and captions;
- preserve reply relationships where possible and use an attribution fallback otherwise;
- propagate reaction add/change/remove events;
- propagate edits and deletes/revokes where WhatsApp permits it;
- persist canonical message-copy relationships so restarts and partial fan-out are idempotent;
- perform bounded best-effort recovery after downtime;
- store no application message content, media, participant JIDs, phone numbers, or push names.

Discord is not part of the MVP, but the canonical model is intentionally transport-neutral.

## Privacy model

Two SQLite databases have different trust boundaries:

- `/data/whatsapp.db` is owned by `whatsmeow`. It contains linked-device/protocol state and may contain WhatsApp identifiers/contact metadata required by the protocol library. Treat it as sensitive protocol state.
- `/data/sync.db` is owned by message-sync. It must remain PII/PHI-free and stores only canonical IDs, endpoint aliases, opaque remote message IDs, HMAC actor IDs, emoji reaction state, timestamps, and recovery cursors.

The WhatsApp adapter must explicitly keep whatsmeow decrypted-event and retry plaintext persistence disabled. Media is handled transiently and discarded after fan-out. Raw events, JIDs, phone numbers, push names, message bodies/captions, and media must never enter application logs.

`IDENTITY_SECRET` is required, stable across restarts, and supplied through environment/container secret handling rather than `config.json`.

## Configuration

```sh
cp config.example.json config.json
cp .env.example .env
openssl rand -hex 32
```

Put the generated secret in `.env` as `IDENTITY_SECRET=...`, then edit `config.json` with your WhatsApp group JIDs and aliases.

Group aliases are application-safe endpoint IDs and must match `[A-Za-z0-9][A-Za-z0-9_-]{0,63}`. Do not use a phone number, JID, person name, or group subject as an alias. Every configured group must belong to exactly one MVP sync set.

`identity.usernameMode` supports:

- `push_name`: use transient `<alias>/<push name>` when available, with HMAC ID fallback;
- `hash`: always use `<alias>/u_xxxxxxxxxx`.

For MVP, a group may belong to only one sync set.

## Rootless Podman

Build and run:

```sh
podman build -f Containerfile -t message-sync:dev .
podman compose up -d
```

The runtime user is UID/GID 10001, all capabilities are dropped in Compose, `no-new-privileges` is enabled, the root filesystem is read-only, and only `/data` is writable persistently.

Check the development version:

```sh
podman run --rm message-sync:dev version
```

It prints `development` until the first MVP release.

> Phase 0 validates configuration, derives the HMAC identity boundary, creates/migrates `/data/sync.db`, and stays running. It deliberately does not create `/data/whatsapp.db` or connect to WhatsApp until Phase 1.

## Local development

Target Go toolchain: Go 1.26, with module compatibility at Go 1.25 because current whatsmeow requires Go 1.25 or newer. `sync.db` uses the CGO-free `modernc.org/sqlite` driver so the runtime image can remain a static `CGO_ENABLED=0` build.

```sh
make fmt
make test
make vet
make build
```

Validate configuration:

```sh
CONFIG_PATH=./config.json go run ./cmd/message-sync validate-config
```

Run:

```sh
IDENTITY_SECRET="$(openssl rand -hex 32)" CONFIG_PATH=./config.json DATA_DIR=./data go run ./cmd/message-sync run
```

## Restart contract

After MVP implementation:

1. `whatsapp.db` restores the linked WhatsApp session; normal restarts do not require another QR scan.
2. `sync.db` restores canonical message/copy and reaction state.
3. recent offline/history events are replayed through the same router;
4. persisted message-copy rows make fan-out idempotent so only missing destination copies are retried;
5. mappings expire under the configured retention policy, after which very old replies/reactions/edits/deletes may fall back or no longer propagate.

The default planned mapping retention is 90 days.

## Versioning and images

No `VERSION` file exists until MVP release readiness. Normal commits and merges therefore do **not** invoke image publication.

After all MVP acceptance criteria pass, create `VERSION`, for example:

```text
0.1.0
```

A push that changes `VERSION` on `main` builds `linux/amd64` and `linux/arm64` images and publishes:

- `ghcr.io/vm75/message-sync:<version>` and `latest`;
- `docker.io/<DOCKERHUB_USERNAME>/message-sync:<version>` and `latest`.

Docker Hub publication requires `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` repository secrets. GHCR uses `GITHUB_TOKEN` with package write permission.

## Documentation

- `ARCHITECTURE.md` — architecture, data boundaries, event flows, restart semantics.
- `docs/MVP_IMPLEMENTATION_PLAN.md` — detailed phased implementation plan and acceptance criteria.
- `docs/POST_MVP.md` — features from the previous implementation intentionally deferred until after MVP.
- `AGENTS.md` — contributor/AI-agent operating instructions.

## Post-MVP

The core is intentionally designed so Discord, multiple WhatsApp accounts, richer WhatsApp types, management UI, membership workflows, cloud storage, provider integrations, and historical import can be added without making any one transport the canonical identity. See `docs/POST_MVP.md`.
