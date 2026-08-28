# Telegram Support Implementation Progress

> **Temporary implementation tracker.**
>
> This file exists only while Telegram Transport Adapter support is being implemented on `agent/telegram-support`.
> The agent completing the final ticket **must delete this file before closing the final issue**.

## Non-negotiable constraints

- All Telegram implementation work happens on branch `agent/telegram-support`.
- This branch was created from the completed `agent/discord-support` implementation and must preserve its WhatsApp + Discord behavior.
- Do **not** update `VERSION` or any release version metadata.
- Preserve the strict zero-PII/PHI privacy model.
- Endpoint aliases remain the application/router identity.
- Telegram, Discord, and WhatsApp message IDs are remote-copy IDs only; never canonical IDs.
- Telegram bot credentials come from environment variables or secret mounts, never `sync.db`.
- Telegram human-readable chat/user metadata must remain transient.
- Every ticket owner must update this file when work starts and when work completes.
- Before closing a ticket, the agent must:
  1. update the row below with final status and notes;
  2. add a final GitHub issue comment summarizing implementation and tests;
  3. close the GitHub issue as completed.
- Implement one ticket per agent run. After completing the selected ticket, stop.

## Ticket selection rule

When an agent is asked to implement the next Telegram ticket:

1. read this file;
2. select the **first ticket in implementation order whose status is not Complete**;
3. verify every listed dependency is Complete;
4. mark only that ticket `In progress`;
5. implement it fully;
6. verify all acceptance criteria;
7. mark it `Complete`;
8. update/close the issue;
9. stop.

Do not skip ahead because a later ticket looks easier.

## Implementation sequence

| Order | Issue | Scope | Depends on | Status | Implementation notes |
|---|---|---|---|---|---|
| 1 | [#25](https://github.com/vm75/message-sync/issues/25) | Add Telegram as a transport-aware endpoint type | — | Complete | Commits through 0cf0145; added Telegram endpoint validation and schema v12 migration; endpoint CRUD + three-transport sync-set tests added; make fmt/test/vet passed via validated manual run; VERSION unchanged. |
| 2 | [#26](https://github.com/vm75/message-sync/issues/26) | Telegram Bot adapter foundation and long-poll ingress | #25 | Complete | Implementation through ffc3e67; Bot API long polling + privacy-safe ingress normalization added; manual make fmt/test/vet output validated; Bot Privacy Mode/admin visibility documented; app registry wiring remains #27 and outbound lifecycle remains #28; VERSION unchanged. |
| 3 | [#27](https://github.com/vm75/message-sync/issues/27) | Wire Telegram into multi-adapter canonical router lifecycle | #25, #26 | In progress | Implementation through d0aabff: shared registry/event-loop/config-reload wiring plus three-transport and restart-safe retry tests; required make fmt/test/vet manual validation pending; VERSION unchanged. |
| 4 | [#28](https://github.com/vm75/message-sync/issues/28) | Telegram outbound text/media/replies/reactions/edits/deletes | #26, #27 | Not started | |
| 5 | [#29](https://github.com/vm75/message-sync/issues/29) | Telegram chat discovery, runtime status, and admin Web UI | #25, #26, #27 | Not started | |
| 6 | [#30](https://github.com/vm75/message-sync/issues/30) | Forum topics, polls, mentions, migrations, and format fallbacks | #28; #29 before related admin behavior | Not started | |
| 7 | [#31](https://github.com/vm75/message-sync/issues/31) | End-to-end hardening, docs, privacy review, tracker cleanup | #25–#30 | Not started | |

## Target architecture

Telegram must plug into the architecture already established by WhatsApp + Discord:

```text
WhatsApp adapter ----\
Discord adapter ------> canonical router -> adapter registry by endpoint transport
Telegram adapter ----/

configured endpoint
  alias        = safe application identity
  transport    = whatsapp | discord | telegram
  remote_id    = transport-specific operational address
  sync_set_id  = exactly one sync set
```

Examples:

```text
WhatsApp endpoint:
  alias      = family-wa
  transport  = whatsapp
  remote_id  = opaque WhatsApp group JID

Discord endpoint:
  alias      = family-discord
  transport  = discord
  remote_id  = opaque Discord channel ID

Telegram endpoint:
  alias      = family-telegram
  transport  = telegram
  remote_id  = opaque Telegram group/supergroup chat ID
```

The router must not know Telegram protocol details.

## Sender presentation model

Telegram differs from Discord.

Discord can use its bridge-managed webhook to render each cross-platform sender as a distinct APP/webhook username.

Telegram bots cannot impersonate a different user/bot identity per message. Therefore WhatsApp/Discord -> Telegram must use transient textual attribution:

```text
Vidhya: Hello everyone
```

If a transient display name is unavailable:

```text
u_abcd1234: Hello everyone
```

Rules:
- display names are transient only;
- never persist/log them;
- HMAC actor identity remains the privacy-safe stable identity;
- do not create Telegram users/bots per bridged participant.

Telegram -> Discord should continue using the existing Discord managed-webhook APP rendering with the transient Telegram sender display name when available.

## Detailed implementation plan

### Phase 1 — Transport-aware endpoint support
Issue #25 extends the already-generalized endpoint model with `transport=telegram`.

Expected outcome:
- Telegram chat IDs can be configured as opaque endpoint `remote_id` values;
- mixed WhatsApp + Discord + Telegram sync sets validate and persist;
- no Telegram-specific parallel config schema is created;
- no Telegram chat title/username/member metadata enters SQLite.

### Phase 2 — Telegram Bot API ingress
Issue #26 adds the Telegram adapter using **long polling**.

Long polling is the initial design because it:
- works without public HTTPS ingress;
- preserves simple rootless/self-hosted deployment;
- avoids webhook/TLS/reverse-proxy configuration.

Expected outcome:
- configured Telegram group/supergroup messages enter the same `transport.Incoming` model;
- private chats and unconfigured chats are dropped at the adapter boundary;
- Telegram user IDs are used only transiently for HMAC derivation;
- bridge-bot messages cannot loop;
- raw Telegram updates never enter logs.

### Phase 3 — Multi-adapter application wiring
Issue #27 registers Telegram in the existing adapter registry and application event loop.

Expected outcome:
- WhatsApp, Discord, and Telegram all route through the same canonical worker;
- all destination aliases dispatch through their configured adapter;
- partial fan-out + retry remains idempotent;
- no Telegram message ID becomes canonical identity.

### Phase 4 — Full Telegram lifecycle
Issue #28 implements Telegram outbound text, transient media, replies, reactions, edits, and deletes.

Expected outcome:
- WA <-> Telegram and Discord <-> Telegram synchronization works through canonical copies;
- cross-platform source sender attribution in Telegram uses transient text + HMAC fallback;
- Telegram replies use native reply semantics when copy mapping exists;
- media remains transient;
- Telegram rate limits use bounded retry/backoff;
- platform limitations are deterministic and tested.

### Phase 5 — Runtime status, discovery, and Web UI
Issue #29 adds authenticated Telegram status and **observed-chat discovery**.

Telegram does not provide Discord-style enumeration of all groups. Discovery is therefore based on a bounded in-memory cache of groups/supergroups observed via Bot API updates.

Expected outcome:
- operator adds the bot to a Telegram group;
- an observed group appears transiently in the admin UI after activity;
- operator assigns a safe alias and sync set;
- only the chat ID is persisted;
- title/username metadata remains in memory/UI response only;
- token stays outside browser/database.

### Phase 6 — Telegram-specific semantics
Issue #30 handles forum topics, group -> supergroup migration, polls, mentions, richer media formats, contacts/locations, and service messages.

Expected outcome:
- forum topics flatten to the configured parent chat alias;
- topic IDs do not create dynamic endpoints;
- basic-group -> supergroup migration updates only the operational chat ID while preserving alias/sync-set membership;
- contacts/locations remain unsupported due to the privacy boundary;
- unsupported Telegram events are deterministic and safely ignored/fallback-rendered;
- poll behavior reuses the existing canonical model where practical or uses textual fallback.

### Phase 7 — Hardening, documentation, and cleanup
Issue #31 validates the entire three-transport system.

Expected outcome:
- issues #25–#30 are closed;
- full cross-transport test matrix passes;
- long-poll retry/offset behavior is hardened;
- migration/forum/discovery/privacy behaviors are verified;
- docs accurately describe Telegram setup and limitations;
- `VERSION` is unchanged;
- this progress file is deleted.

## Telegram operational setup assumptions

The initial implementation uses the Telegram **Bot API**, not a Telegram user account/MTProto client.

Operators will generally need to:

1. create a bot with BotFather;
2. configure `TELEGRAM_BOT_TOKEN` or `TELEGRAM_BOT_TOKEN_FILE`;
3. add the bot to the desired group/supergroup;
4. configure sufficient visibility for messages intended to sync;
5. disable Bot Privacy Mode or grant appropriate admin visibility where necessary;
6. generate group activity so the chat appears in transient discovery;
7. assign a safe alias and sync set from the admin UI.

Do not add Telegram user-session/phone-login support as part of these tickets.

## Privacy boundary

Telegram support must not persist or retain in application logs:

- Telegram user IDs;
- usernames;
- first/last/display names;
- chat titles;
- chat usernames;
- invite links;
- forum-topic names;
- message bodies;
- captions;
- source filenames;
- media bytes;
- Telegram file URLs;
- contacts;
- locations;
- raw Bot API updates/responses;
- bot credentials.

Allowed narrowly-scoped state:
- configured safe endpoint alias;
- `transport=telegram`;
- opaque configured Telegram chat ID as endpoint addressing;
- opaque Telegram message IDs in `message_copies`;
- HMAC actor IDs;
- canonical IDs and existing lifecycle state.

## Cross-ticket acceptance gates

Every implementation ticket must satisfy all applicable gates:

```sh
make fmt
make test
make vet
```

If container/runtime/deployment files change:

```sh
podman build -f Containerfile -t message-sync:dev .
podman compose config
```

Additionally:
- preserve all existing WhatsApp functionality;
- preserve all completed Discord functionality, including managed-webhook APP sender rendering;
- no raw Telegram PII/content in `sync.db` or retained logs;
- no Telegram platform ID used as canonical identity;
- no version bump.

## Progress update format

When work starts:

```text
Status: In progress
Notes: Started Telegram long-poll adapter and ingress normalization.
```

When complete:

```text
Status: Complete
Notes: Commit abc123; make fmt/test/vet passed; acceptance criteria verified; issue completion comment posted.
```

Keep notes concise and useful to the next ticket owner.

## Final cleanup requirement

When issue #31 is complete:

1. verify issues #25–#30 are closed as completed;
2. run the full required test matrix;
3. verify `VERSION` is byte-for-byte unchanged from the branch point;
4. post the final test/privacy/documentation summary to issue #31;
5. delete `docs/TELEGRAM_SUPPORT_PROGRESS.md`;
6. commit the deletion;
7. close issue #31 as completed.

Do not leave a replacement Telegram implementation tracker.
