# End-to-End Testing Guide (WhatsApp, Discord & Telegram)

This guide provides step-by-step instructions to set up, configure, and test multi-platform message synchronization across **WhatsApp**, **Discord**, and **Telegram** using `message-sync`.

No prior experience with Discord bot setup or the Telegram Bot API is required.

For the local automated gate, including the fake three-transport integration harness, race checks, and fresh-volume smoke tests, see [TESTING.md](../TESTING.md).

---

## Table of Contents

1. [Prerequisites](#1-prerequisites)
2. [Discord Setup (Step-by-Step)](#2-discord-setup-step-by-step)
3. [Telegram Setup (Step-by-Step)](#3-telegram-setup-step-by-step)
4. [WhatsApp Preparation](#4-whatsapp-preparation)
5. [Starting message-sync](#5-starting-message-sync)
6. [Connections, Endpoints & Sync Sets in the Web UI](#6-connections-endpoints--sync-sets-in-the-web-ui)
7. [Testing Checklist & Verification Scenarios](#7-testing-checklist--verification-scenarios)
8. [Membership Verification Testing](#8-membership-verification-testing)
9. [Troubleshooting & Common Pitfalls](#9-troubleshooting--common-pitfalls)

---

## 1. Prerequisites

Before starting, ensure you have:
- A server or local workstation with **Docker** (or **Podman**) and Docker Compose / Podman Compose installed.
- A mobile phone with **WhatsApp** installed and active.
- A **Discord** account (register free at [discord.com](https://discord.com) if needed).
- A **Telegram** account (register free via mobile or desktop app if needed).

---

## 2. Discord Setup (Step-by-Step)

To bridge Discord channels, you will create a Discord server, register a Discord bot application, enable privileged gateway intents, and invite the bot to your server with webhook management permissions.

### Step 2.1: Create a Testing Discord Server
1. Open Discord (desktop app or browser).
2. In the left-hand sidebar, click the **+** (Add a Server) button.
3. Select **Create My Own** &rarr; **For me and my friends**.
4. Name the server (e.g., `Message Sync Test`) and click **Create**.
5. Inside your new server, create a dedicated text channel (or use `#general`), e.g., `#sync-test`.

### Step 2.2: Create a Discord Bot Application
1. Navigate to the [Discord Developer Portal](https://discord.com/developers/applications).
2. Log in with your Discord account credentials.
3. Click the **New Application** button (top right).
4. Enter an application name (e.g., `MessageSync-Bot`), accept the terms, and click **Create**.
5. In the left navigation menu, click **Bot**.
6. Under the **Build-A-Bot** section, locate the **Token** subsection and click **Reset Token** (confirm if prompted).
7. Copy the generated token string immediately and keep it secure. You will paste this token once into the Web UI when creating your Discord connection.
   > [!IMPORTANT]
   > Treat this token like a password. You do not need to put it into environment files; it is pasted once in the Web UI and encrypted immediately with AES-256-GCM.

### Step 2.3: Enable Privileged Gateway Intents
On the same **Bot** page, scroll down to the **Privileged Gateway Intents** section:
1. The bridge requests **Guild Messages** and **Guild Message Reactions** at gateway startup; no separate portal toggle is required for those gateway intents.
2. Toggle **Message Content Intent** to ON.
   > [!IMPORTANT]
   > **Message Content Intent** is mandatory. If this intent is disabled, Discord will hide all message text and attachments from the bridge bot, preventing messages from being relayed.
3. Click **Save Changes** at the bottom of the page.

### Step 2.4: Invite the Bot to Your Discord Server
1. In the left menu of the Developer Portal, click **Installation** (or **OAuth2** &rarr; **URL Generator**).
2. Under **Installation Contexts**, ensure **Guild Install** is selected.
3. Under **Default Install Settings** &rarr; **Scopes**, check `bot`.
4. Under **Bot Permissions**, check the following permissions:
   - **View Channels**
   - **Send Messages**
   - **Manage Webhooks** *(Required: message-sync creates and manages one channel webhook to display the original sender's name and avatar)*
   - **Read Message History**
   - **Add Reactions**
5. Copy the generated **Install Link** (or OAuth2 authorization URL).
6. Paste the URL into your web browser, select your `Message Sync Test` server from the dropdown, click **Continue**, and click **Authorize** (complete the CAPTCHA if prompted).
7. Return to Discord: you should now see your bot listed in the server member list on the right.

### Step 2.5: Verify Channel Permissions
1. In Discord, hover over your test channel (e.g., `#sync-test`) and click the gear icon (**Edit Channel**).
2. Go to **Permissions** &rarr; **Advanced Permissions**.
3. Confirm that the bot's role has green checkmarks (or inherited permissions) for **View Channel**, **Send Messages**, **Read Message History**, **Add Reactions**, and **Manage Webhooks**.

---

## 3. Telegram Setup (Step-by-Step)

The Telegram adapter uses Telegram's Bot API with long polling. You will create a bot with BotFather, disable Bot Privacy Mode so the bot can read group messages, add it to a test group, and send an initial message.

### Step 3.1: Create a Bot with BotFather
1. Open your Telegram app.
2. In the search bar at the top, search for `@BotFather` (ensure it has the official blue verification checkmark).
3. Click **Start** (or send `/start`) to initiate a conversation with BotFather.
4. Send the command:
   ```text
   /newbot
   ```
5. BotFather will prompt: `Alright, a new bot. How are we going to call it? Please choose a name for your bot.`
   - Type a friendly name, e.g., `MessageSync Test Bot`.
6. BotFather will prompt: `Good. Now let's choose a username for your bot. It must end in 'bot'.`
   - Type a unique username ending in `bot`, e.g., `my_msg_sync_test_bot`.
7. BotFather will reply with congratulations and provide your **HTTP API token** in the format:
   ```text
   1234567890:ABCdefGhIJKlmNoPQRsTUVwxyZ_1234567
   ```
8. Copy this token string. You will paste this token once into the Web UI when creating your Telegram connection.

### Step 3.2: Disable Bot Privacy Mode (Mandatory)
By default, Telegram bots operate in **Privacy Mode**, meaning they only receive messages that start with a slash `/` (commands) or that directly mention the bot. To synchronize ordinary chat messages, Privacy Mode must be disabled.

1. In your chat with `@BotFather`, send the command:
   ```text
   /setprivacy
   ```
2. BotFather will display a list of your bots. Select your newly created bot (e.g., `@my_msg_sync_test_bot`).
3. Click or tap **Disable**.
4. BotFather will confirm with:
   ```text
   Success! The new status is: DISABLED.
   ```

### Step 3.3: Create a Telegram Testing Group and Add the Bot
1. In Telegram, create a new group (Menu &rarr; **New Group**).
2. Enter a name for the group, e.g., `Message Sync Test Group`.
3. Add your bot to the group: search for the bot's username (e.g., `@my_msg_sync_test_bot`) and add it as a member.
4. Complete group creation.

### Step 3.4: Send an Initial Message in the Telegram Group
1. Open the newly created group.
2. Type and send an ordinary message, for example: `Init sync test`.
   > [!IMPORTANT]
   > The Telegram Bot API does not provide an endpoint to list groups a bot belongs to. `message-sync` discovers groups dynamically by observing incoming updates in an in-memory cache. Sending at least one message ensures the group appears in the admin console.

---

## 4. WhatsApp Preparation

1. Open WhatsApp on your phone.
2. Create a new test group, e.g., `Message Sync WhatsApp Test`.
3. Keep your phone ready to scan a QR code from **Settings** (or menu) &rarr; **Linked Devices** &rarr; **Link a Device**.

---

## 5. Starting message-sync

### Step 5.1: Create the Working Directory and `.env` File

On your testing host:

```sh
mkdir -p message-sync-test/data
cd message-sync-test
```

Generate a 32-byte hexadecimal identity secret:

```sh
openssl rand -hex 32
```

Create a `.env` file in `message-sync-test/`:

```env
# Required: 32-byte hex secret
IDENTITY_SECRET=your_generated_32_byte_hex_secret_here

DATA_DIR=/data
PORT=8080
LOG_LEVEL=info

# Host data directory
MESSAGE_SYNC_DATA_DIR=./data
```

### Step 5.2: Create `compose.yml`

In the same directory, create `compose.yml`:

```yaml
services:
  message-sync:
    container_name: message-sync
    image: docker.io/vm75/message-sync:latest
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

If building from local source, replace the `image` line with:

```yaml
    build:
      context: .
      dockerfile: Containerfile
```

### Step 5.3: Launch the Container

```sh
docker compose up -d
# Or with Podman:
# podman compose up -d
```

Check the startup logs:

```sh
docker compose logs -f
```

You should see logs indicating that the HTTP API is listening on port 8080. Provider adapters start after their connections are configured and enabled.

---

## 6. Connections, Endpoints & Sync Sets in the Web UI

### Step 6.1: Initial Admin Account Setup
1. Open your browser and navigate to `http://localhost:8080`.
2. You will be prompted to create the first administrator username and password.
3. Enter the account details and complete setup. You will be logged into the management console.

### Step 6.2: Create Platform Connections
1. In the Web UI, navigate to the **Connections** view.
2. **Add WhatsApp Connection**:
   - Click **Add Connection**, select **WhatsApp**, and enter a label (e.g., `wa-primary`).
   - Click **Pair** on the connection card to generate a QR code.
   - On your phone, open WhatsApp &rarr; **Settings** &rarr; **Linked Devices** &rarr; **Link a Device**, and scan the QR code.
   - The status updates to **Connected**. Each WhatsApp connection maintains an isolated protocol store (`/data/whatsapp/<connection-id>.db`).
   - *(Optional Multi-Account)*: Click **Add Connection** to add a second WhatsApp account. Note that pairing operations are serialized to one flow at a time.
3. **Add Discord Bot Connection**:
   - Click **Add Connection**, select **Discord**, enter a label (e.g., `dc-primary`), and paste your bot token once.
   - The token is encrypted immediately with AES-256-GCM using `IDENTITY_SECRET` and saved to `control.db`. It is never stored in `sync.db` or browser storage.
   - The connection card displays Gateway status and Webhook readiness.
   - *(Optional Multi-Bot)*: Add a second Discord connection with another bot token.
4. **Add Telegram Bot Connection**:
   - Click **Add Connection**, select **Telegram**, enter a label (e.g., `tg-primary`), and paste your Bot API token once.
   - The token is encrypted immediately with AES-256-GCM. Long polling initializes with a connection-scoped recovery cursor (`telegram:<connection-id>`).
   - *(Optional Multi-Bot)*: Add a second Telegram connection with another bot token.

### Step 6.3: Scoped Discovery & Endpoint Creation
1. Navigate to the **Endpoints** view (or use **Discover** directly on any connection card).
2. **WhatsApp Endpoint**: Click **Discover** on your WhatsApp connection, locate your test group, enter an alias (e.g., `wa_test`), and click **Add Endpoint**.
3. **Discord Endpoint**: Click **Discover** on your Discord connection, select `#sync-test`, assign an alias (e.g., `dc_test`), and click **Add Endpoint**.
4. **Telegram Endpoint**: Click **Discover** on your Telegram connection to view observed chats, select `Message Sync Test Group`, assign an alias (e.g., `tg_test`), and click **Add Endpoint**.

### Step 6.4: Create the Unified Sync Set
1. Navigate to the **Sync Sets** view.
2. Click **Create Sync Set**.
3. Name the sync set (e.g., `test_sync_set`).
4. Select the configured endpoints:
   - `wa_test` (WhatsApp)
   - `dc_test` (Discord)
   - `tg_test` (Telegram)
5. Save the sync set.

All platforms in the sync set are now synchronized! No server restart is required when adding or reconfiguring connections.

---

## 7. Testing Checklist & Verification Scenarios

Execute the following test scenarios to verify full lifecycle functionality across all three platforms.

### Automated reliability gate

Run the automated checks before manual provider testing:

```sh
make fmt
git diff --check
GOCACHE=/tmp/message-sync-go-cache make test
GOCACHE=/tmp/message-sync-go-cache make vet
GOCACHE=/tmp/message-sync-go-cache go test -race ./internal/integration ./internal/delivery ./internal/recovery ./internal/router ./internal/api ./internal/transport/discord ./internal/transport/telegram ./internal/transport/whatsapp
```

The integration harness uses fake WhatsApp, Discord, and Telegram adapters to verify all-to-all fan-out, slow-destination isolation, transient retry, and ordered create/edit/reaction/delete delivery. The package tests additionally cover queue saturation, restart/replay state, checkpoint gaps and duplicates, provider reconnect/history behavior, configuration reload, managed-webhook replacement, and privacy canaries.

Use a fresh `sync.db` for local verification. The current product has no database migration or backward-compatibility path. Never use or inspect `/data/whatsapp/<connection-id>.db` as application data; it is protocol state owned by whatsmeow.

When validating recovery, keep the provider bounds in mind: Telegram can only replay updates retained by Bot API, Discord recovery is bounded channel history and cannot reconstruct every offline delete or reaction, and WhatsApp recovery depends on bounded protocol HistorySync and does not promise offline lifecycle reconstruction. Recovered events must produce the same user-visible behavior as live events.

In the authenticated Web UI, check **Delivery Health** after inducing a slow or unavailable destination. It should show only endpoint aliases, queue/lane state, bounded counts, safe failure classes, and transport readiness; it must not expose message content, provider IDs, identities, timestamps, credentials, or raw errors.

### Scenario 1: Plain Text Messages
- [ ] **WhatsApp &rarr; Discord & Telegram**: Send `Hello from WhatsApp` in the WhatsApp group.
  - **Verify Discord**: Message appears in `#sync-test` posted by the bridge webhook with the sender's WhatsApp display name as the username.
  - **Verify Telegram**: Message appears in `Message Sync Test Group` with sender attribution `wa_test/<name>: Hello from WhatsApp`.
- [ ] **Discord &rarr; WhatsApp & Telegram**: Send `Hello from Discord` in `#sync-test`.
  - **Verify WhatsApp**: Message arrives with sender attribution `dc_test/<name>: Hello from Discord`.
  - **Verify Telegram**: Message arrives with sender attribution `dc_test/<name>: Hello from Discord`.
- [ ] **Telegram &rarr; WhatsApp & Discord**: Send `Hello from Telegram` in the Telegram group.
  - **Verify WhatsApp**: Message arrives with sender attribution `tg_test/<name>: Hello from Telegram`.
  - **Verify Discord**: Message arrives via webhook under the Telegram sender's display name.

### Scenario 2: Media Synchronization
- [ ] **Photos / Images**: Send an image with a caption from WhatsApp.
  - **Verify Discord**: Image renders directly with the caption.
  - **Verify Telegram**: Photo arrives with the caption intact.
- [ ] **Audio / Voice Notes**: Send a voice message or audio clip from Telegram.
  - **Verify WhatsApp**: Audio is received and playable.
  - **Verify Discord**: Audio file attachment is delivered.
- [ ] **Documents (PDF, TXT)**: Send a document from Discord.
  - **Verify WhatsApp & Telegram**: Document file is forwarded and downloadable.
- [ ] **Stickers**: Send a WhatsApp sticker.
  - **Verify Discord**: Delivered as a clean `sticker.webp` attachment.
  - **Verify Telegram**: Delivered as a Telegram sticker.

### Scenario 3: Replies (Thread Reference)
- [ ] **Reply on WhatsApp**: Right-click/swipe a bridged message in WhatsApp and reply `Replying to this`.
  - **Verify Discord**: Emits one webhook message under the sender's name with a clickable link labelled with the source endpoint and first line of the quoted message, followed by the reply content.
  - **Verify Telegram**: Delivered as a native Telegram quote/reply to the corresponding message.
- [ ] **Reply on Telegram**: Reply to a bridged message in Telegram.
  - **Verify WhatsApp & Discord**: Mapped reply accurately references the original canonical message.

- [ ] **Discord thread/forum post**: Send a message in a configured parent's thread or forum post.
  - **Verify**: WhatsApp/Telegram flat copies show one deterministic `[contexts ...]` header without the raw thread ID; reply from either transport returns to the original thread.
- [ ] **Telegram forum topic**: Send a message in a non-General topic.
  - **Verify**: flat copies show a deterministic privacy-safe context header; a WhatsApp or Discord reply is sent with the original `MessageThreadID`.
- [ ] **Cross-child reply lineage**: Reply to the flattened Discord message from inside a Telegram topic.
  - **Verify**: the canonical lineage retains both endpoint-specific scopes, Telegram remains in its topic, Discord returns to its thread, and WhatsApp receives deterministic presentation only.

### Scenario 4: Emoji Reactions
- [ ] **React in Discord**: Add a `:thumbsup:` reaction to a bridged message in Discord.
  - **Verify WhatsApp**: A thumbs-up reaction appears on the target message.
  - **Verify Telegram**: A thumbs-up reaction is added to the target message in Telegram.
- [ ] **Change / Remove Reaction**: Remove the reaction in Discord.
  - **Verify WhatsApp & Telegram**: Reaction is removed on the remote messages.

### Scenario 5: Message Edits
- [ ] Send a message from Telegram: `Testing an edit`.
- [ ] Edit the message in Telegram to: `Testing an edit (UPDATED)`.
- [ ] **Verify Discord**: The corresponding message text updates to the edited content.
- [ ] **Verify WhatsApp**: The WhatsApp message updates to the edited content.

### Scenario 6: Message Deletions
- [ ] Send a message from Discord: `Temporary message to delete`.
- [ ] Delete the message in Discord.
- [ ] **Verify WhatsApp**: The message is revoked/deleted for everyone.
- [ ] **Verify Telegram**: The message is deleted in the Telegram group.

### Scenario 7: Polls & Vote Aggregation
- [ ] **Create Poll in WhatsApp**: Create a poll with question `Lunch preference?` and options `Pizza`, `Sushi`, `Tacos`.
  - **Verify Discord**: A native Discord poll appears with the same question, options, and single-answer semantics.
  - **Verify Telegram**: A native Telegram poll appears with the same question, options, and single-answer semantics.
  - **Verify All**: Each endpoint has one bridge-owned live-results companion containing aggregate option counts only, with no voter identity.
- [ ] **Vote Aggregation**: Vote in the poll, then reply with the exact text `aggregate-response` to a poll copy.
  - **Verify All**: The trigger message is suppressed and the aggregate-only companion is updated through normal delivery lanes.
- [ ] **Unsupported representation**: Create a poll whose option count or semantics exceed a destination's native limits.
  - **Verify**: Only that destination receives the deterministic text fallback; representable destinations retain native polls.

### Scenario 8: Source-local Messages

- [ ] In authenticated Settings, set a global Local-only message prefix (for example `//local `); leave it empty to disable.
- [ ] Send a new text, captioned media message, and representable poll beginning exactly with the prefix from each configured parent and, where available, a Discord thread/forum post and Telegram topic.
  - **Verify**: each remains only in its source conversation, no destination delivery is queued, and captioned media is not downloaded.
- [ ] Restart/replay the service, edit a suppressed message to remove the prefix, and exercise delete/reaction.
  - **Verify**: the opaque suppression marker still prevents bridging and lifecycle events remain local.
- [ ] Reply normally to a suppressed message.
### Scenario 9: Multi-Connection Mixed Topology & Isolation
- [ ] Configure two WhatsApp connections (`wa-1`, `wa-2`), two Discord connections (`dc-1`, `dc-2`), and two Telegram connections (`tg-1`, `tg-2`).
- [ ] Assign endpoints into two independent sync sets:
  - `set-alpha`: `wa-1-ep`, `dc-1-ep`, `tg-1-ep`
  - `set-beta`: `wa-2-ep`, `dc-2-ep`, `tg-2-ep`
- [ ] **Verify**: Messages sent in `set-alpha` fan out only to endpoints in `set-alpha`. Neither `wa-2`, `dc-2`, nor `tg-2` receives messages from `set-alpha`.

### Scenario 10: Connection-Scoped Discovery
- [ ] Click **Discover** on `dc-1` and verify only guilds/channels accessible to Discord Bot 1 are listed.
- [ ] Click **Discover** on `dc-2` and verify channels accessible to Bot 2 are listed.
- [ ] Click **Discover** on `tg-1` vs `tg-2` and verify observed chats are tracked and presented independently per bot token.

### Scenario 11: Cross-Connection Thread & Topic Lineage
- [ ] Send a message in a Discord thread on connection `dc-1` routed to a Telegram endpoint on connection `tg-1`.
- [ ] Reply from Telegram quoting the bridged message.
- [ ] **Verify**: The reply returns accurately to the originating thread on `dc-1`, with child scopes preserved without creating child endpoint identities.
- [ ] Repeat with Telegram forum topics on `tg-2` routed to Discord on `dc-2`.

### Scenario 12: Connection Lifecycle, Reassignment & Deletion Safety
- [ ] **Disable Connection**: Disable connection `dc-1` in the Web UI.
  - **Verify**: Delivery to `dc-1` pauses with a transient lane failure; healthy lanes for other connections continue processing normally.
- [ ] **Re-enable / Token Replacement**: Click **Edit** on `dc-1`, paste a valid token, and save.
  - **Verify**: Gateway reconnects and pending deliveries resume.
- [ ] **Endpoint Reassignment**: Reassign endpoint `dc-test` from `dc-1` to `dc-2`.
  - **Verify**: Next message to `dc-test` is immediately dispatched through `dc-2` without duplicating canonical copies or changing endpoint alias.
- [ ] **Deletion Protection**: Attempt to delete `dc-2` while `dc-test` is still assigned.
  - **Verify**: The UI / API rejects deletion with `connection has active endpoints`.
  - Delete or reassign `dc-test`, then delete `dc-2`. Deletion succeeds and cleans up credentials.

### Scenario 13: Privacy & Secret Canaries
- [ ] Query `sync.db`: Verify `SELECT * FROM message_copies` and `canonical_messages` contain zero bot tokens, participant phone numbers, push names, or message bodies.
- [ ] Query `control.db`: Verify `transport_connections` stores only AES-256-GCM ciphertext and nonces; no plaintext token appears anywhere in database strings.
- [ ] Check application logs: Verify zero tokens or raw protocol update dumps appear in stdout.

---

## 8. Membership Verification Testing

The current membership workflow supports public applications with a work email, a WhatsApp phone number or Discord user ID, an optional LinkedIn URL, bounded custom fields, and optional PDF/image evidence. Deterministic checks and optional OpenRouter analysis are advisory; only an authenticated human reviewer can approve or reject an application.

Use a dedicated test sync set, an email address you control, test transport identities, and synthetic or thoroughly redacted evidence. Do not upload a real immigration notice, A-number, home address, passport number, Social Security number, or unrelated personal data during routine testing.

### Current verification limits

- There is no document lookup, document ownership check, or case-status validation.
- A LinkedIn URL is checked only for HTTPS and a `linkedin.com` host. The service does not prove account control, identity, or employment history, and it has no LinkedIn OAuth/OIDC integration.
- OpenRouter receives image evidence as an inline image. For PDF evidence, the current analyzer sends only its generic review instruction, so PDFs are human-review evidence rather than meaningful automated input.
- Although `AnalysisInput` has deterministic email-domain and LinkedIn fields, public intake currently passes only evidence type and bytes to the analyzer. Do not expect the OpenRouter result to incorporate those deterministic signals.
- A valid-looking document or URL is never an automatic membership decision.

### Optional integrations

For a complete deployed email-challenge flow, configure both mail variables through the deployment's secret mechanism:

```env
VERIFICATION_MAIL_API_KEY=
VERIFICATION_MAIL_FROM=
VERIFICATION_PUBLIC_BASE_URL=https://your-test-host.example
```

Without a configured mailer, intake still creates a request and a hashed challenge, but the challenge is not delivered; a normal external test cannot advance the request from `pending_email` to `pending_admin`.

To exercise advisory image analysis, provide an OpenRouter key through the deployment's secret mechanism and set:

```env
OPENROUTER_ALLOW_TRAINING=false
OPENROUTER_MODEL=openrouter/free
```

The analyzer is disabled unless `OPENROUTER_ALLOW_TRAINING` is exactly `false`. The default router does not guarantee repeatable image-model selection; use an explicitly chosen model only after verifying that it supports image input and the required privacy posture. Provider failures, malformed output, rate limits, and timeouts must remain safe `failed` or `unavailable` advisory states.

### Configure the test application

1. In **Settings**, enable the browser-local **Membership review** preference so the Membership navigation and sync-set application controls are visible.
2. Create a dedicated sync set with a configured WhatsApp or Discord endpoint.
3. Open that sync set's **Membership Application** controls, require evidence, and configure bounded instructions. For example:

   - Applicant instructions: `Provide your work email, LinkedIn profile, and the requested synthetic supporting document.`
   - Evidence instructions: `Upload a redacted synthetic image. Do not include identifiers or personal details that are not needed for this test.`
   - Reviewer guidance: `Treat automated checks as advisory and make the final decision manually.`

4. Add custom fields in the Web UI or through `PUT /api/sync-sets/{id}/membership`. This request shape matches the current API:

```json
{
  "applicantInstructions": "Provide the requested test details.",
  "evidenceInstructions": "Upload synthetic or redacted evidence only.",
  "reviewerGuidance": "Automated results are advisory.",
  "evidenceRequired": true,
  "customFields": [
    {
      "key": "full_name",
      "label": "Full name",
      "type": "text",
      "required": true,
      "maxLength": 120,
      "position": 0
    },
    {
      "key": "eligibility_document",
      "label": "Supporting document",
      "type": "select",
      "required": true,
      "maxLength": 0,
      "position": 1,
      "options": [
        "Synthetic I-797 Receipt Notice",
        "Synthetic I-797C Receipt Notice"
      ]
    }
  ]
}
```

5. Under **Membership**, create and enable a verification pipeline for that endpoint, then open its public application link in a private browser window.

### Exercise the workflow

1. Submit a work email, the transport identity required by the pipeline, a valid `https://www.linkedin.com/...` URL, all required custom answers, and a synthetic PNG/JPEG under 5 MiB.
2. Confirm that intake returns the generic received state without exposing the request record or endpoint addressing.
3. Complete the emailed challenge and verify that the request moves from `pending_email` to `pending_admin`.
4. Open the request in **Membership** and verify the deterministic result, OpenRouter state/confidence/detail when configured, custom answer snapshot, and protected evidence download.
5. Select **Needs review** and verify that evidence remains available and the request stays pending human action.
6. Make a final **Approve** or **Reject** decision and verify that the evidence reference is cleared and the local evidence file is purged or durably queued for cleanup. The decision must remain human-authorized even when advisory checks report high confidence.
7. For approved WhatsApp requests, verify that fulfillment uses the configured join-approval/invite flow and never directly adds a participant. For Discord, verify only the configured role-assignment boundary.

### Negative and privacy cases

- [ ] Personal/free email domains produce a lower deterministic advisory result; they are not automatically rejected.
- [ ] Non-HTTPS or non-LinkedIn URLs are rejected, while an accepted LinkedIn URL is still displayed only as a syntactic check.
- [ ] Missing required answers, unknown answers, invalid select values, unchecked required checkboxes, and overlong values are rejected.
- [ ] Missing required evidence, files over 5 MiB, and non-PDF/non-image content are rejected.
- [ ] A synthetic image exercises advisory analysis; a PDF remains available to the human reviewer without claiming meaningful automated document analysis.
- [ ] OpenRouter unavailable, rate-limited, timed out, or malformed responses do not block human review or approve a request.
- [ ] Stale concurrent review decisions return a conflict instead of overwriting the first decision.
- [ ] Applicant PII, answers, and evidence remain in `control.db` or the private evidence directory only; `sync.db`, router state, delivery health, and application logs remain free of them.
- [ ] Final approve/reject removes local evidence, while **Needs review** retains it.

---


## 9. Troubleshooting & Common Pitfalls

### Discord Issues

| Symptom | Cause | Solution |
| :--- | :--- | :--- |
| **Discord bot is connected, but messages from Discord never reach WhatsApp or Telegram** | **Message Content Intent** is disabled in the Discord Developer Portal. | Go to [Discord Developer Portal](https://discord.com/developers/applications) &rarr; Application &rarr; **Bot** &rarr; **Privileged Gateway Intents** &rarr; Enable **Message Content Intent** &rarr; Save changes. Restart container if needed. |
| **Discord status shows Webhook Readiness: `missing_permission`** | The bot lacks **Manage Webhooks** permission in the destination channel. | In Discord, open channel settings &rarr; **Permissions** &rarr; grant the bot role **Manage Webhooks**. In Web UI, refresh status. |
| **Messages sent to Discord have generic `@participant` mentions** | Privacy protection prevents leaking raw Discord user or role IDs. | This is intentional: `message-sync` sanitizes mention tokens into safe display labels or `@participant` to prevent unintended pinging. |

### Telegram Issues

| Symptom | Cause | Solution |
| :--- | :--- | :--- |
| **Telegram group does not appear in "Refresh Observed Chats"** | No messages have been sent in the group since the bot was added, or bot has not processed any update yet. | Send an ordinary message (e.g. `test`) in the Telegram group. Then click **Refresh Observed Chats** in the Web UI. |
| **Bot only sees messages starting with `/` (commands), ignoring regular chat messages** | Telegram **Bot Privacy Mode** is enabled. | In Telegram, message `@BotFather`, send `/setprivacy`, choose your bot, and select **Disable**. |
| **Web UI Telegram status says Bot Privacy Mode: `Unknown`** | The runtime `getMe` capability probe failed or could not connect to Telegram Bot API. | Check internet connectivity or firewall rules. The Web UI will still function based on your manual BotFather configuration. |
| **Large video or document fails to forward to Telegram** | Hosted Telegram Bot API limits uploads to 50 MiB (and photos to 10 MiB). | Send files within platform limits or lower/raise the configured **Media Max Size (MB)** in the authenticated Settings view, while staying within Telegram's platform limits. |

### WhatsApp Issues

| Symptom | Cause | Solution |
| :--- | :--- | :--- |
| **QR code times out before scanning** | WhatsApp pairing codes expire after roughly 20-30 seconds. | Click **Start Pairing** again in the Web UI to generate a fresh QR code. |
| **WhatsApp disconnects after restarting host** | Persistent volume `/data` was not retained across container runs. | Verify that your `compose.yml` mounts a persistent host volume to `/data` so `whatsapp/<connection-id>.db` and `sync.db` are preserved. |

### Inspecting Logs Safely
All logs produced by `message-sync` are strictly zero-PII/PHI:
```sh
docker compose logs -f message-sync
```
You can safely inspect event types, endpoint aliases (`wa_test`, `dc_test`, `tg_test`), and canonical message IDs without exposing message bodies, phone numbers, or tokens.
