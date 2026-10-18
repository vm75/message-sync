# Telegram integrations

A Telegram connection uses exactly one integration method: **Bot API** or **Phone / MTProto**. Both appear to the router as the canonical `telegram` transport. There is no automatic fallback between methods, and an endpoint always names one `connection_id`, so it can use only that connection's integration method.

## Choosing a method

The opt-in [private DM relay](PRIVATE_MESSAGING.md) supports Bot API only.
Users must first privately contact the same bot with `/start`, which returns
`/help`. Native Reply on an active relayed bot message routes the next turn;
unattached private text receives help. Verified operational inbox tokens are
encrypted in `control.db` for 24 hours and renewed on private contact. Expired
reachability requires contacting the bot again. Current group membership must
be verifiable on every hop; blocked DMs fail privately with no group fallback.
Private edits are ignored. Bot API `deleteMessage` is never used for private
cleanup because it deletes the participant's copy too. Reply metadata expires
even when provider messages remain. MTProto personal inboxes are unsupported,
not auto-answered, and never touched by private cleanup.

| Capability | Bot API | Phone / MTProto |
|---|---|---|
| Authentication | BotFather token | Telegram API ID/hash + phone login code + optional 2FA |
| Group discovery | Observed groups only | Complete joined supported groups/supergroups |
| Forum-topic discovery | Observed topics | Complete forum-topic enumeration |
| Bot Privacy Mode | Applicable; may restrict ordinary group updates | Not applicable |
| Bounded history recovery | No arbitrary Telegram history reads | Yes, through the normal recovery coordinator |
| Text/media/replies/topics | Yes | Yes |
| Reactions/edits/deletes | Yes | Yes |
| Native polls + aggregate live results | Yes | Yes |
| Private DMs | Not supported | Not supported |
| Broadcast channels | Not supported by current endpoint policy | Not supported by current endpoint policy |

Use **Bot API** when a bot identity and observation-based discovery are sufficient. Use **Phone / MTProto** when the deployment needs complete joined-group/topic discovery or bounded recovery of missed ordinary messages. Telegram account automation can be subject to Telegram terms and anti-abuse controls; use an account and API application appropriate for the deployment.

## Bot API setup

1. Create a bot with the official **@BotFather** and obtain its token.
2. In **Connections**, create a Telegram connection and choose **Bot API**.
3. Paste the token once. The server encrypts it in `control.db`; read APIs never return it.
4. Add the bot to intended groups/supergroups. Bot Privacy Mode and administrator visibility determine which ordinary group updates are observable.
5. Send a group message, then use **Discover**. Discovery only lists chats the bot has actually observed.

If ordinary group messages are missing, review Bot Privacy Mode and bot administrator permissions. This guidance is intentionally not shown for Phone / MTProto connections.

## Phone / MTProto setup

Create a Telegram API application through Telegram's official application-development portal and obtain an **API ID** and **API hash**. Then:

1. In **Connections**, create a Telegram connection and choose **Phone / MTProto**.
2. Enter API ID, API hash, and the account phone number. These values are sent only to the authenticated local management API.
3. Request the login code and enter the one-time code delivered by Telegram.
4. If Telegram reports that two-step verification is required, enter the account's 2FA password.
5. When the connection state is **connected**, use **Discover All** to enumerate joined supported groups. Forum groups expose a **Topics** action.
6. The **Manage Login** dialog can log the Telegram session out or run an explicitly bounded history backfill for a configured endpoint.

After startup, a configured and authenticated MTProto endpoint reports **ready** in Delivery Health. A connected account without configured Telegram endpoints has no endpoint readiness entries.

A logged-out MTProto connection retains its encrypted API application configuration/phone so a new code can be requested, but the reusable Telegram session, peer/access-hash cache, and poll-correlation cache are cleared.

## Security model

MTProto session material is stored only in the connection's encrypted credential blob in mode-`0600` `control.db`, using the same credential cipher derived from `IDENTITY_SECRET`. The reusable gotd session, API hash, phone number, peer access hashes, and operational poll correlation are inside that encrypted boundary.

One-time login codes, the temporary phone-code hash, and 2FA passwords are **not persisted**. The web UI clears secret input elements after submission. The server sanitizes authentication failures and must not log raw Telegram objects, credentials, phone numbers, message bodies, OTPs, 2FA passwords, session/auth keys, or API hashes. `/data/sync.db` remains the content-free canonical routing store and does not contain MTProto authentication state.

Keep `IDENTITY_SECRET` stable and protect backups of `control.db`. Losing the secret makes encrypted connection credentials unusable; exposing both the secret and `control.db` exposes provider credentials/session state.

## Discovery, topics, and history

The management API returns capabilities with each Telegram connection. The UI uses those capability flags instead of assuming all Telegram connections behave alike:

- `chatDiscovery=observed` vs `full`
- `topicDiscovery=observed` vs `full`
- `historyRecovery=false` vs `true`
- `privacyModeStatus=true` only where Bot Privacy Mode is relevant
- `polls=true` when native canonical poll support is available

MTProto history is always bounded by maximum event count and maximum age and is normalized through the existing `transport.RecoverySource` coordinator. It is not an account-export facility. Live ingress and recovered events share connection-scoped recovery stream keys/cursors, so overlap is idempotent.

MTProto poll updates marked as minimal are refreshed through Telegram before replacing canonical endpoint totals. Outbound poll correlation uses the poll ID and answer keys returned by Telegram rather than client-generated request values. Bridge-created MTProto polls are non-anonymous; their per-voter updates are normalized directly to canonical option indexes and an HMAC actor identity, including vote removal, without retaining Telegram voter IDs. MTProto text, media captions, and live-result edits use native Telegram entities so bridge attribution and aggregate headings/options retain the same bold/italic presentation as Bot API delivery.

## Endpoint exclusivity

An endpoint stores a transport, one `connection_id`, and one provider remote ID. The server rejects a connection whose transport does not match the endpoint transport. Changing a Telegram connection from Bot API to MTProto (or the reverse) in place is deliberately rejected: create another connection, validate/discover the target there, and explicitly reassign the endpoint.

This keeps bot/user sessions, peer caches, poll provider references, recovery cursors, and loop-prevention state connection-scoped and prevents silent fallback between Telegram integration methods.

## Troubleshooting

- **Bot discovery is empty:** make sure the bot is a group member and has received a group update; review Bot Privacy Mode/admin visibility.
- **MTProto discovery is empty:** confirm the session state is `connected` and the phone account has joined supported groups/supergroups.
- **Login code rejected:** request a fresh code; codes and temporary code hashes are intentionally not persisted across process restarts.
- **2FA requested:** submit the Telegram two-step-verification password in **Manage Login**. It is used transiently only.
- **MTProto send after restart cannot resolve a group:** run discovery once if the account has never observed/discovered that peer; resolved peer/access-hash metadata is thereafter kept inside encrypted connection state.
- **Backfill unavailable:** only Phone / MTProto advertises arbitrary history recovery, and the connection must be authenticated with a configured endpoint and explicit bounds.
- **Endpoint cannot be created/reassigned:** confirm its transport matches the selected connection and that the target is a supported group.

Automatic recovery uses the endpoint’s sync-set `recovery.enabled`,
`recovery.maxAgeHours`, and `recovery.maxMessagesPerGroup` settings. Bot API
availability remains limited by Telegram's retained update queue. MTProto
history is capped at 5,000 messages per request; manual backfill uses its
explicit request bounds. Media and alias presentation policies also belong
to the endpoint's sync set, even when one connection serves several sets.


Cross-group references use assigned `@{Amber Badger}` aliases within the source
sync set. Bot API native rendering requires current destination-chat membership
and an available transient native identity binding; otherwise the exact authored
token stays literal. MTProto and edits do not add native reference notifications.
Private Bot API `/dm @{Amber Badger} text` requires existing author aliases,
current set membership and prior private contact with the owning bot (`/start`).
The DM header shows only the sender's assigned set alias. MTProto personal
inboxes remain unmanaged. See [private messaging](PRIVATE_MESSAGING.md).
