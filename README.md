# message-sync

Privacy-first message synchronization service implemented in Go. The MVP uses `tulir/whatsmeow` for WhatsApp transport, JSON configuration, SQLite runtime state, and a transport-neutral canonical message model that can support Discord and other adapters after MVP.

## Status

`message-sync` is fully implemented and operational: strict JSON config validation, HMAC identity, PII/PHI-free `sync.db` state, whatsmeow session lifecycle with QR pairing and automatic reconnect, all-to-all text and media synchronization, native reactions and clickable replies, edits and deletes with canonical tombstones, bounded offline recovery, 90-day retention pruning, and rootless container deployment.

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

The WhatsApp adapter explicitly keeps whatsmeow decrypted-event and retry plaintext persistence disabled and supplies no whatsmeow/sqlstore logger, so raw protocol objects and identifiers do not enter application logs through the library. The adapter drops DMs and unconfigured groups before normalization. Application logs may contain only configured aliases, normalized event kinds, connection state, and stable error classes.

The first-login QR is sensitive transient pairing material shown directly as terminal UI. Do not copy, persist, or upload the QR. Once pairing succeeds, the QR flow is not started on normal restarts.

Media is handled transiently and discarded after fan-out. Raw events, JIDs, phone numbers, push names, message bodies/captions, and media must never enter application logs.

`IDENTITY_SECRET` is required, stable across restarts, and supplied through environment/container secret handling rather than database tables.

## Configuration

Configuration is stored in SQLite (`sync.db`) and managed programmatically or via the built-in REST API / Web UI.

```sh
cp .env.example .env
openssl rand -hex 32
```

Put the generated secret in `.env` as `IDENTITY_SECRET=...`.

Group aliases are application-safe endpoint IDs and must match `[A-Za-z0-9][A-Za-z0-9_-]{0,63}`. Do not use a phone number, JID, person name, or group subject as an alias. Every configured group must belong to exactly one sync set.

`usernameMode` supports:

- `push_name`: use transient `<alias>/<push name>` when available, with HMAC ID fallback;
- `hash`: always use `<alias>/u_xxxxxxxxxx`.

### REST API
 
The daemon provides a local HTTP server on port 8080 (configurable via `API_ADDR`):
 
- `GET /health`: Returns `{"status":"ok"}` with `200 OK` (public).
- `GET /api/auth/status`: Returns whether admin password setup is complete (`{"isSetup": false}` or `true`).
- `POST /api/auth/setup`: Sets initial admin password, hashes with bcrypt into `sync.db`, and returns a session token and cookie.
- `POST /api/auth/login`: Authenticates password and returns a session token and cookie.
- `POST /api/auth/logout`: Clears the session cookie.
- `GET /api/whatsapp/status`: Returns the current WhatsApp client connection state (`"unpaired"`, `"pairing"`, `"connected"`, `"disconnected"`).
- `POST /api/whatsapp/pair`: Initiates or retrieves an active WhatsApp QR pairing session.
- `DELETE /api/whatsapp/pair`: Cancels an in-progress WhatsApp pairing session.
- Protected `/api/*` endpoints require `Authorization: Bearer <token>` or `session` cookie.

## Rootless Podman

Build:

```sh
podman build -f Containerfile -t message-sync:dev .
```

For first login, run attached so the QR can be scanned from the terminal:

```sh
podman compose run --rm message-sync run
```

In WhatsApp, open **Linked devices**, choose **Link a device**, and scan the terminal QR. After the pairing-complete message appears, stop the attached process with Ctrl-C. Do not capture or upload the QR output.

Start normally after pairing:

```sh
podman compose up -d
```

The persisted `/data/whatsapp.db` in the Compose volume reconnects the linked session without another QR on a normal restart.

The runtime user is UID/GID 10001, all capabilities are dropped in Compose, `no-new-privileges` is enabled, the root filesystem is read-only, and only `/data` is writable persistently.

Check the development version:

```sh
podman run --rm message-sync:dev version
```

It prints `development` until the first MVP release.


## Local development

Target Go toolchain: Go 1.26, with module compatibility at Go 1.25 because current whatsmeow requires Go 1.25 or newer. `sync.db` and `whatsapp.db` use the CGO-free `modernc.org/sqlite` driver so the runtime image can remain a static `CGO_ENABLED=0` build.

```sh
make fmt
make test
make vet
make build
```

Validate configuration:

```sh
DATA_DIR=./data go run ./cmd/message-sync validate-config
```

Run attached for first pairing or local event inspection:

```sh
IDENTITY_SECRET="$(openssl rand -hex 32)" DATA_DIR=./data go run ./cmd/message-sync run
```

Use a stable `IDENTITY_SECRET` for any real deployment; the one-liner above is only convenient for isolated local development.

## Restart contract

1. `whatsapp.db` restores the linked WhatsApp session; normal restarts do not require another QR scan.
2. Configured group messages are normalized through the safe adapter boundary after reconnect.
3. `sync.db` restores canonical message/copy and reaction state.
4. Recent offline/history events are replayed through the same router.
5. Persisted message-copy rows make fan-out idempotent so only missing destination copies are retried.
6. Mappings expire under the configured retention policy (default 90 days), after which very old replies/reactions/edits/deletes may fall back or no longer propagate.

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
- `docs/POST_MVP.md` — features from the previous implementation intentionally deferred until after MVP.
- `AGENTS.md` — contributor/AI-agent operating instructions.

## Post-MVP

The core is intentionally designed so Discord, multiple WhatsApp accounts, richer WhatsApp types, management UI, membership workflows, cloud storage, provider integrations, and historical import can be added without making any one transport the canonical identity. See `docs/POST_MVP.md`.
