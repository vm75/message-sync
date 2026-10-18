# Configuration

Deployment settings come from the environment; routing and feature settings are configured through the authenticated Web UI or REST API.

## Environment variables

| Variable | Required | Default | Purpose |
|---|---:|---|---|
| `IDENTITY_SECRET` | yes | none | HMAC identity and credential-encryption root secret; minimum 32 bytes. |
| `DATA_DIR` | no | `/data` | Directory containing all SQLite state and private evidence. |
| `PORT` | no | `8080` | HTTP port when `API_ADDR` is unset. |
| `API_ADDR` | no | derived from `PORT` | Full HTTP listen address, mainly useful for direct source runs and tests. |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, or `error`. |
| `WHATSAPP_DEVICE_NAME` | no | `message-sync` | Companion device name shown in WhatsApp Linked Devices. |
| `VERIFICATION_MAIL_API_KEY` | no | none | Enables Resend-compatible email delivery for experimental membership verification with `VERIFICATION_MAIL_FROM`. |
| `VERIFICATION_MAIL_FROM` | no | none | Sender used by optional experimental membership email delivery. |
| `VERIFICATION_PUBLIC_BASE_URL` | no | request origin | External base URL used in experimental membership links behind a proxy. |
| `OPENROUTER_API_KEY` | no | none | Enables advisory image evidence analysis only when training is explicitly disabled. |
| `OPENROUTER_ALLOW_TRAINING` | no | unset | Must equal `false` for OpenRouter analysis to run. |
| `OPENROUTER_MODEL` | no | `openrouter/free` | Model used by optional advisory analysis. |
| `MESSAGE_SYNC_DATA_DIR` | no | `./data` | Host-side `/data` bind source used by `compose.yml`; it is not read by the service. |

Discord bot tokens, optional explicit Discord webhook URLs, Telegram Bot API tokens, and Telegram MTProto application/session state are configured dynamically and encrypted with AES-256-GCM in `control.db`; they never belong in `.env`.

## Built-in commands

`/local <message>` keeps group traffic local; `/summary` requests live aggregated
results when replying to a synchronized poll. These commands are not stored as
editable settings and cannot be removed. Clearing additional prefixes leaves
`/local` active. An empty aggregation setting retains the existing
`aggregate-response` default as well as `/summary`.

`/help` lists `/help`, `/local`, `/summary`, private `/dm`, Telegram `/start`, and
additional configured values. Group help is scoped to its sync set and stays
local, including its echoes and later changes. Private help includes configured
values only for the requesting inbox’s currently verified, relay-enabled sets.
Existing private relay switches and rate limits still apply.

## Runtime settings

Each conversation row has **Anonymize incoming** and **Anonymize outgoing** checkboxes alongside move/rename/remove. Outgoing applies to all forwarded messages sent from that conversation; incoming applies to all messages delivered to it. Enable either or both, then Save. These are endpoint policies, independent of the authenticated account used by other conversations. The endpoint REST API accepts `anonymizeIncoming` and `anonymizeOutgoing`; omitted values on edits preserve the existing policy. Existing Discord-only policies are upgraded to Incoming on the affected Discord endpoints. The former `anonymizeToDiscord` sync-set field remains a deprecated compatibility input for older clients.

Routing and feature settings belong to each sync set. Open a sync set and choose **Edit settings** to open its settings modal, or use `PUT /api/sync-sets/{id}` with a nested `settings` object. Omitted fields retain their current values, including nested fields. Changes reload without restarting the service. Save is enabled only while the draft differs from the saved settings; saving and failure feedback appears beside the button. Saving or reverting a change disables Save again.

| Setting inside `settings` | New-set default | Description |
|---|---|---|
| `membershipReviewEnabled` | `false` | Enable the experimental membership review UI for this set, shared across browsers. |
| `privateDMRelayEnabled` | `false` | Permit private relays within this set only when the global administrator switch is also enabled. |
| `media.enabled`, `media.maxSizeMB` | `true`, `100` | Forward media with an in-memory size limit in MB; provider limits still apply. |
| `recovery.enabled`, `recovery.maxAgeHours`, `recovery.maxMessagesPerGroup` | `true`, `24`, `200` | Automatic recovery bounds for conversations in this set; provider availability and limits still apply. |
| `storage.messageRetentionDays` | `90` | Retain content-free routing metadata for the configured days. |
| `polls.aggregationTrigger` | `aggregate-response` | Additional space-separated poll summary triggers; built-in `/summary` is always available. |
| `localPrefix` | `""` | Additional space-separated local-only prefixes; built-in `/local` is always available. |
| `whatsappCleanup.enabled`, `whatsappCleanup.retentionDays` | `false`, `30` | Scheduled WhatsApp AppState cleanup for this set's chats. |
| `childContextDisplayMode` | `opaque` | `opaque` uses transient child labels; `friendly` also persists observed thread/topic labels for restart/replay. |

For example, `{"settings":{"media":{"maxSizeMB":50}}}` updates one set while preserving its other settings. Upgrade initialization copies existing routing policies into each existing sync set once. Subsequent changes are independent. Retention follows the original sync set of each canonical message, even after an endpoint moves; orphan history and suppression markers expire after 90 days.

The WhatsApp linked-device name belongs to its connection, shared by every set using that connection. Set `whatsappDeviceName` when creating a WhatsApp connection, or edit **Connections → Device name** (`PUT /api/connections/{id}`). It defaults to `message-sync` for new connections and applies when linking the device. Existing connections import their former global name once. User accounts and admin security stay global. `/api/config` exposes only the administrator's default-off `privateDMRelayEnabled` safety switch; other runtime settings belong to sync sets.

## Membership review

Enable **Membership review** in a sync set’s settings. Its instructions, evidence
guidance and custom fields belong to that set. The Membership selector switches
between enabled sets. This controls review visibility; pipeline enablement and
in-flight applications keep their existing behavior. See the
[manual guide](TESTING_GUIDE.md#8-membership-verification-testing-experimental).

## Upgrades

Back up your data directory and read the [release notes](../CHANGELOG.md) before
upgrading. Narrow, idempotent initialization migrations preserve supported
settings and state; there is no general versioned migration framework.

## Discord connection setup

Discord always uses a **bot token** for inbound Gateway events and native Discord operations. Enable the privileged **Message Content** intent in the Discord Developer Portal, install the bot in the target server, and grant the permissions required by the features you use. At minimum for normal bidirectional message sync, grant **View Channel**, **Read Message History**, and **Send Messages**; grant **Add Reactions** for reaction sync and the appropriate thread/poll permissions for those features.

For native cross-group mentions immediately after restart, optionally enable
**Server Members Intent** in the Developer Portal. It permits an on-demand
guild-roster lookup when the temporary native-ID cache is empty. Live or
recovered participant messages also populate this cache.

`message-sync` supports two outbound webhook setups. Choose one when creating the Discord connection.

### Option A — managed webhook (default)

Use this when you want the simplest setup or one Discord bot connection to serve multiple channels.

1. Create the Discord application and bot, enable **Message Content** intent, and copy the bot token.
2. Install the bot in the server with **View Channel**, **Read Message History**, **Send Messages**, **Manage Webhooks**, plus any reaction/thread/poll permissions you need.
3. In `message-sync`, create a Discord connection and choose **Managed by message-sync**.
4. Paste the bot token once.
5. Use **Discover** to add one or more Discord channels as endpoints.

For every configured channel, `message-sync` finds or creates one bridge-owned webhook and keeps the webhook credential only inside the Discord adapter process. The bot needs **Manage Webhooks** because it provisions and repairs those webhooks.

### Option B — existing webhook + channel ID

Use this when you prefer to create the webhook yourself and do **not** want to grant the bot **Manage Webhooks**.

1. Create the Discord application and bot, enable **Message Content** intent, and copy the bot token.
2. Install the bot in the server with **View Channel**, **Read Message History**, **Send Messages**, plus any reaction/thread/poll permissions you need. **Manage Webhooks is not required.**
3. In Discord, open the target channel's **Edit Channel → Integrations → Webhooks**, create a webhook, and copy its webhook URL.
4. Enable Discord Developer Mode if necessary, then copy the same channel's numeric **Channel ID**.
5. In `message-sync`, create a Discord connection and choose **Use existing webhook + channel ID**.
6. Paste the **bot token**, **webhook URL**, and matching **channel ID**.
7. Use **Discover**; this connection exposes only the configured channel, which you can then add as an endpoint.

The bot token is still required in this mode. The bot listens for Discord → WhatsApp/Telegram messages, edits, reactions, polls, threads, and history events; the supplied webhook handles WhatsApp/Telegram → Discord message delivery and webhook-owned edits/deletes. Messages sent by that webhook are recognized as bridge-owned and ignored on ingress to prevent loops.

An explicit-webhook Discord connection is intentionally bound to **one Discord channel**. Use managed-webhook mode when one connection must serve multiple Discord channels. The bot token and explicit webhook URL are encrypted in `control.db` and are never returned by connection read APIs.

| Discord requirement | Managed webhook | Existing webhook + channel ID |
|---|---:|---:|
| Bot application + bot token | Required | Required |
| Message Content intent | Required for ordinary message ingress | Required for ordinary message ingress |
| View Channel / Read Message History | Required | Required |
| Send Messages | Required for native bot operations | Required for native bot operations |
| Add Reactions | Required only for reaction sync | Required only for reaction sync |
| Manage Webhooks | **Required** | **Not required** |
| Webhook creation | Automatic | Manual |
| Channel ID entry | Discovered | Entered during connection setup |
| Multiple Discord channels per connection | Yes | No — one configured channel |

For Telegram setup and integration differences, see [Telegram integrations](TELEGRAM.md).
For DM enablement and limits, see [private messaging](PRIVATE_MESSAGING.md).
