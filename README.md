# message-sync

[![Build & Publish](https://img.shields.io/github/actions/workflow/status/vm75/message-sync/release-images.yml?branch=main&label=build&style=flat-square&logo=githubactions)](https://github.com/vm75/message-sync/actions)
[![Go Version](https://img.shields.io/badge/go-1.25%2B-00ADD8?style=flat-square&logo=go)](https://go.dev/)
[![Docker Pulls](https://img.shields.io/docker/pulls/vm75/message-sync?style=flat-square&logo=docker)](https://hub.docker.com/r/vm75/message-sync)
[![Docker Image Size](https://img.shields.io/docker/image-size/vm75/message-sync/latest?style=flat-square&logo=docker)](https://hub.docker.com/r/vm75/message-sync)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg?style=flat-square)](LICENSE)
[![Platforms](https://img.shields.io/badge/platforms-linux%2Famd64%20%7C%20linux%2Farm64-326CE5?style=flat-square&logo=linux)](https://github.com/vm75/message-sync)
[![Privacy](https://img.shields.io/badge/privacy-zero%20PII%2FPHI-success?style=flat-square&logo=shield)](ARCHITECTURE.md#privacy-invariants)
[![Security](https://img.shields.io/badge/container-rootless%20%2F%20non--root-blueviolet?style=flat-square)](Containerfile)

`message-sync` is a simple server to sync messages between multiple messaging channels. End-to-end routing supports WhatsApp groups, configured Discord channels, and configured Telegram groups/supergroups through one transport-neutral canonical router. The authenticated Web UI provides Discord channel discovery plus transient Telegram observed-chat discovery without persisting human-readable remote names.


## Features

- **Mixed-Transport Synchronization**: Connect WhatsApp groups, configured Discord channels, and configured Telegram groups/supergroups in the same alias-based sync sets.
- **Rich Media Support**: Forwards text, images, videos, audio/voice notes, documents, and stickers.
- **Native Polls & Live Results**: Uses native WhatsApp, Discord, and Telegram polls where representable; every poll endpoint gets one bridge-owned editable companion with aggregate option counts only. Unsupported representations use deterministic text fallback.
- **Reactions & Replies**: Preserves clickable native reply structures and message reactions across groups.
- **Message Edits & Deletions**: Automatically propagates edits and deleted/revoked messages.
- **Reliable Delivery**: Each destination has an independent ordered queue with bounded retries, restart-safe pending work, and lifecycle ordering, so a slow or unavailable provider does not block healthy destinations. Ambiguous provider creates are never blindly retried; source recovery remains replayable until payload-dependent work is safe to forget.
- **Bounded Recovery**: WhatsApp HistorySync snapshots are replayed oldest-first through the same checkpointed routing path as live messages; recovery is bounded and does not promise offline delete/reaction reconstruction.
- **Automated Chat Cleanup**: Optional daily message clearing for connected groups on the sync account to keep device storage lean.
- **Source-local Messages**: An optional global prefix keeps newly-created matching messages in their source group, channel, thread, or topic; suppression stores no message content and is restart-safe.
- **Embedded Web UI**: Simple, zero-dependency management console to inspect WhatsApp/Discord/Telegram status, discover Discord channels and transiently observed Telegram groups, configure transport-neutral endpoint aliases, and manage mixed sync sets directly from your browser.
- **Hardened Security**: Runs as a static, non-root binary in read-only containers.

Polls retain their native UI on destinations that support the source semantics. A destination that cannot represent the question, options, answer mode, duration, or media combination receives deterministic text instead of silently losing poll semantics. The bridge-owned live-result companion is aggregate-only: it shows option counts, never voter names or per-voter choices, and is updated through the normal delivery lanes. Its heading is bold-italic with transport-native markup, followed by the question and options. Deleting a poll also deletes its live-result companion on every endpoint. `aggregate-response` remains supported as an exact reply to a poll copy; the trigger message is suppressed and the reply produces a manual aggregate summary using the same canonical counts. Bot-created Telegram polls provide absolute result snapshots. Human-created Telegram source polls can be mirrored but have a partial/unavailable live source contribution because the Bot API does not expose the required ongoing voter/result lifecycle; MTProto is intentionally not used.

Flattened thread/topic headers are presentation-only HMAC tokens and have no routing authority; native reply lineage and persisted opaque scope metadata determine return placement. If a scoped child destination is unavailable, delivery fails safely rather than silently falling back to the parent or General chat.

Remote providers do not all offer deterministic client-assigned create IDs. When a provider response is ambiguous, the bridge preserves content-free retry state and requires provider recovery or operator reconciliation rather than claiming exactly-once remote creation. WhatsApp bridge-generated lifecycle echoes use bounded in-memory suppression markers; genuine unmatched linked-device `FromSelf` mutations remain routable.

## Privacy Model

`message-sync` is built with a strict privacy-first architecture. It guarantees that no personal data is ever logged or persisted to the application database.

- **Zero PII/PHI**: The application database (`sync.db`) never stores participant phone numbers/JIDs, Discord or Telegram user names/IDs, guild/channel/chat names, message bodies, media, source filenames, CDN/file URLs, or contact cards. Configured transport endpoint IDs are stored only as the minimum operational addressing needed to reach an endpoint; human-readable remote names are not stored.
- **Transient Media**: Media files are only downloaded into memory long enough to forward them to the peer groups, and are never retained on disk.
- **Anonymized Identity**: User identity is represented purely by stable, HMAC-derived hashes or configured group aliases (e.g. `c1g1`).
- **Separation of State**: WhatsApp protocol state (`/data/whatsapp/<connection-id>.db`), which naturally requires some contact metadata for the connection to work, is strictly isolated per connection (directory mode 0700, file mode 0600) and never accessed by application logic or exposed through the API.
- **Control-plane separation**: Multi-user account and membership-verification data is kept in the separate sensitive `/data/control.db`; it is not copied into the PII-free routing database (`sync.db`) or application logs.
- **Membership intake**: Administrators can bind a public, high-entropy verification pipeline to a configured WhatsApp or Discord endpoint. Public applications validate transport identity and work email, optionally accept bounded PDF/image evidence under `/data`, and create pending email-verification requests without exposing endpoint addressing or applicant records.
- **Optional email verification**: `VERIFICATION_MAIL_API_KEY` and `VERIFICATION_MAIL_FROM` enable Resend-compatible challenge delivery. Missing mail configuration is non-fatal; deterministic checks remain advisory.
- **Optional AI review**: `OPENROUTER_API_KEY` plus `OPENROUTER_ALLOW_TRAINING=false` enables bounded asynchronous image assessment (default model `openrouter/free`). Results are advisory only; malformed, unavailable, or privacy-ineligible analysis falls back to human review.
- **Membership fulfillment**: Approved requests use narrow WhatsApp/Discord administration boundaries for direct addition or role assignment. WhatsApp invite fallback remains pending until membership is confirmed and the old invite is rotated; provider failures expose only safe status classes.
- **Membership review**: Authenticated admins and operators can filter and review requests, inspect protected evidence, and make audited approve/reject/needs-review decisions. Concurrent stale decisions return a conflict; AI remains advisory.
- **Control-plane retention**: Expired sessions/challenges/invites and terminal membership requests are pruned in bounded batches; terminal evidence is removed from the private evidence directory after 30 days. Routing state is not affected.

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

If you want Discord channel discovery or your configuration contains Discord endpoints, also configure a Discord bot credential. Use one source only:

```sh
# Environment source (automatically passed by the repository's env_file setup)
echo "DISCORD_BOT_TOKEN=your-bot-token" >> .env

# Or mount a secret file into the container and set its in-container path:
# DISCORD_BOT_TOKEN_FILE=/run/secrets/discord_bot_token
```

The Discord application must enable the privileged **Message Content** intent in the Discord Developer Portal. The bridge requests **Guild Messages** and **Guild Message Reactions** gateway intents at runtime. In each channel you want to bridge, grant the bot **View Channel**, **Read Message History**, **Send Messages**, **Add Reactions**, and **Manage Webhooks**. `message-sync` finds or creates one bridge-managed incoming webhook per configured Discord channel and reuses it across source users and restarts.

The Web UI never accepts a Discord token. Configure `DISCORD_BOT_TOKEN` or `DISCORD_BOT_TOKEN_FILE` at deployment time and restart the service. When a token source is configured, the Discord gateway starts even before the first Discord endpoint exists so the authenticated admin UI can discover guild text/announcement channels. Guild and channel display names returned by discovery are transient response/UI data only; only the selected channel ID is persisted as the endpoint's operational `remote_id`. Bot tokens and managed-webhook credentials are never stored in `sync.db`.

If **Manage Webhooks** is missing, Discord ingress/discovery can remain connected but the admin status reports the affected endpoint alias as `missing_permission`; grant **Manage Webhooks** in that destination channel and refresh. Webhook IDs, URLs, and tokens are never exposed by the management API.

The Telegram Bot API adapters use **long polling** and resume their connection-scoped Bot API update streams from the last contiguously accepted update after restart (`telegram:<connection-id>`). Multiple Telegram bots run concurrently through the connection management model, with bot credentials encrypted in the control store using domain-separated keys derived from `IDENTITY_SECRET`. Replayed updates use the normal canonical routing and idempotency path. Telegram only retains updates for a limited provider window, so updates older than that window cannot be recovered.

Create the bot with **BotFather**, add it to each intended Telegram group/supergroup, and ensure it can receive the messages you intend to synchronize. For ordinary group messages, disable **Bot Privacy Mode** through BotFather or grant the bot the administrator visibility required by your deployment. The admin status derives Bot Privacy Mode readiness from Telegram's safe `getMe` capability flag when the probe succeeds; it retains only the boolean state, never the returned bot user object. If the probe is unavailable, the UI falls back to fixed operator guidance.

Telegram discovery is observation-based because the Bot API cannot enumerate every group a bot belongs to. After the bot is added and has suitable visibility, send a message in the target group/supergroup and open **Telegram → Refresh Observed Chats** in the authenticated Web UI. Observed chat titles/usernames live only in a bounded in-memory cache and disappear on process restart; only the selected opaque negative chat ID is persisted as endpoint `remote_id`. The browser never accepts or stores the bot token.

### 2. Start the Server

Create a `compose.yml` file (see [compose.yml](compose.yml) in this repository for an example) and start the server:

```sh
docker compose up -d
```
*(If using Podman, simply replace `docker` with `podman`)*

### 3. Setup and Pairing

1. Open `http://localhost:8080` in your web browser.
2. Complete the first-run setup with an account username and password; this creates the first active administrator.
3. Go to the WhatsApp pairing section, display the QR code, and scan it from WhatsApp on your phone (**Linked Devices** → **Link a Device**). The companion device identifies as `message-sync` by default; this can be customized in the web console Settings page or overridden via `WHATSAPP_DEVICE_NAME` before linking.
4. Configure WhatsApp, Discord, and Telegram endpoints and sync sets in the web console. For Telegram, send a group message first so the Bot API observation cache can discover the chat.

The authenticated Settings page includes an optional Local-only message prefix and customizable WhatsApp Companion Device Name.
The local prefix is exact and case-sensitive (including leading whitespace rules); an empty
value disables it. Matching new messages stay in their source conversation,
including a Discord thread or Telegram topic.

## Management API & Administration

The authenticated management API uses per-user accounts and server-side sessions stored in the sensitive `control.db`. First-run `POST /api/auth/setup` accepts `username` and `password` and creates the first admin; `POST /api/auth/login` accepts the same fields. Sessions are revocable and deactivation-aware. Admins can invite either role, deactivate/reactivate accounts, generate one-time reset tokens, and inspect fixed-field audit events; operators retain day-to-day operational access but cannot administer accounts. Password changes revoke the user's other sessions. The API also provides transport-neutral endpoint CRUD at `/api/endpoints`. Endpoint records contain only `alias`, `transport`, `remoteId`, and optional `syncSetId`; transport credentials are configured separately and are never accepted by endpoint CRUD. The configuration model accepts `whatsapp`, `discord`, and `telegram`; Telegram endpoint `remoteId` values are negative Bot API group/supergroup chat IDs. Runtime configuration reload updates routing targets across all active transports without restart.

Admins create verification pipelines in the Web UI or through `/api/verification/pipelines`, selecting a configured endpoint alias and, for Discord, a role ID. The public pipeline link accepts a work email, transport identity, optional LinkedIn URL, and bounded PDF/image evidence. Optional email delivery uses `VERIFICATION_MAIL_API_KEY` and `VERIFICATION_MAIL_FROM`; optional OpenRouter review additionally requires `OPENROUTER_ALLOW_TRAINING=false`. Operators and admins review email-verified requests in the Membership view. Decisions are authoritative, AI is advisory, fulfillment retries are explicit/idempotent, and terminal control-plane records/evidence are retained for 30 days before bounded cleanup.

- **Endpoint Management**: `GET`, `POST`, `PUT`, `DELETE` at `/api/endpoints`.
- **Discord Administration**: `GET /api/discord/status` reports safe connection/webhook-readiness state; `GET /api/discord/channels` provides on-demand live discovery of guild text/announcement channels.
- **Telegram Administration**: `GET /api/telegram/status` reports safe token-source/long-poll/endpoint-readiness and Bot Privacy Mode status; `GET /api/telegram/chats` returns the bounded in-memory list of observed groups/supergroups.
- **Delivery Health**: Authenticated `GET /api/delivery/status` reports each configured alias's safe lane state, bounded queue depth, content-free ledger counts, oldest active age in seconds, safe failure class, and transport readiness. The dashboard polls this view while public `/health` remains unchanged.
- **Sync-set API**: Sync-set payloads use the `endpoints` field for endpoint aliases across WhatsApp, Discord, and Telegram. Generic `/api/groups` CRUD routes are not supported; use `/api/endpoints` for configuration and `/api/whatsapp/groups` for joined-group discovery.

Transport credentials, tokens, and raw protocol update payloads are never returned by the API or persisted in `sync.db`.

## Transport Adapters & Semantics

### Discord Transport

The Discord adapter provides bidirectional text/media synchronization plus replies, reactions, edits, deletes, authenticated channel discovery, and safe managed-webhook readiness through the same canonical/message-copy model used by WhatsApp and Telegram:

- **Gateway Bot & Webhooks**: The **gateway bot** owns Discord ingress, discovery, reactions, and connection lifecycle; the **bridge-managed incoming webhook** owns bridged outbound message rendering. Messages toward Discord reuse one managed webhook per destination channel: the sender's transient display name is supplied only as that message's webhook APP username, with the HMAC actor ID as fallback, while the message body remains separate. An APP/webhook username is Discord presentation metadata—not a real Discord user account—and the bridge never creates a Discord account or webhook per participant.
- **Replies**: Discord's incoming-webhook API does not support `message_reference`, so a mapped reply is posted as one sender-attributed webhook message containing a clickable link labelled with the source endpoint and first line of the quoted message, plus the reply content. If the destination copy or transient channel metadata is unavailable, the content uses an alias-based textual reply fallback instead.
- **Thread & Forum Context**: Discord thread/forum messages flatten to the configured parent channel alias but retain opaque canonical child context. Flat destinations receive deterministic non-routing context headers; replies, reactions, edits, deletes, polls, and companions return to the existing thread. No child endpoint or forum post is created.
- **Polls, Stickers & Mentions**: Representable polls use native Discord polls and Discord vote events update aggregate state; unsupported Discord poll limits use deterministic text fallback. Native Discord poll question/options and participant IDs remain transient. Embed/component-only messages, system messages, and native Discord sticker-only events are ignored. WhatsApp stickers sent to Discord are forwarded as transient `sticker.webp` attachments. Discord mentions use transient display text or safe HMAC/generic fallbacks; raw Discord member, role, and channel IDs never cross the adapter boundary.
- **Media**: Discord attachment bytes and CDN URLs stay transient and are bounded by the configured media size limit.
- **Recovery**: At startup and after gateway resume, the adapter reads a bounded oldest-first slice of each configured channel after its accepted snowflake cursor and replays it through the normal router. The status response exposes only safe history readiness classes; Discord cannot reconstruct every offline delete or reaction transition.

### Telegram Transport

The Telegram Bot API adapter provides bidirectional text/media synchronization plus replies, reactions, edits, deletes, and bounded transient chat discovery through the same canonical router loop and message-copy model:

- **Long Polling & Reliability**: The pinned Telegram Bot API client retries transient `getUpdates` failures with bounded backoff (including Telegram `retry_after` responses), while the adapter rejects duplicate or older update IDs within the running process. Shutdown cancels long polling and waits for the polling worker to exit.
- **Sender Attribution**: Cross-transport delivery uses transient sender attribution where Telegram cannot impersonate another platform's participant, while Telegram sender identity remains HMAC-normalized at the adapter boundary.
- **Forum-Topic Context**: Telegram forum-topic messages flatten to the configured parent group/supergroup alias while retaining opaque topic scope in canonical lineage. Scoped creates set `MessageThreadID` for native replies and lifecycle-related sends; topic names are never persisted and no topic endpoint or registry is created.
- **Group to Supergroup Migration**: Basic-group to supergroup migration service messages transactionally replace only that endpoint's opaque `remote_id`, preserving its alias and sync-set membership; migration logs contain only the safe alias and fixed event class.
- **Polls, Mentions & Formats**: Representable polls use Telegram Bot API native polls; unsupported quiz/media/limit cases use deterministic text fallback. Native Telegram poll IDs are opaque lifecycle metadata, and bot-created absolute result snapshots update aggregate counts without storing voters. Human-created source polls retain the Bot API’s degraded live-result limitation; MTProto is not used. Telegram mentions use transient display labels or safe generic/HMAC fallbacks so raw usernames do not cross the adapter boundary.
- **Media Limits**: Hosted Bot API media handling enforces 10 MiB for photos, 50 MiB for general uploads, and Telegram sticker format caps (512 KiB static WebP, 64 KiB TGS, 256 KiB WebM), further bounded by the configured media limit. Over-limit media fails deterministically without requiring a Local Bot API server. Contacts, locations/venues, and unsupported service-only payloads are ignored.

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
- `TESTING.md` — Automated reliability checks and fresh-volume runtime/container smoke tests.
- `docs/TESTING_GUIDE.md` — Complete step-by-step testing guide for WhatsApp, Discord, and Telegram.
- `DOCKERHUB.md` — Information related to the published container images.
- `docs/FEATURE_COMPARISON.md` — Detailed feature comparison and architectural trade-offs.
- `AGENTS.md` — Instructions for AI agents and code contributors.

## Disclaimers

- **Non-Affiliation**: This project is an independent open-source tool and is not affiliated, associated, authorized, endorsed by, or in any way officially connected with WhatsApp, Meta Platforms, Inc., or any of their subsidiaries or affiliates.
- **Terms of Service**: Automated interactions and unofficial clients are subject to WhatsApp's Terms of Service. Use this tool responsibly and at your own risk. The maintainers assume no liability for any account restrictions, bans, or service disruptions.

## License

This project is licensed under the [MIT License](LICENSE).
