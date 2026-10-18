# Private DM relay

Private messaging is optional and disabled by default. An administrator must
enable **Settings → Allow private DM relays** and **Sync Sets → Edit settings →
Enable private DM relay for this sync set**. Both switches are required. The
existing administrator-only `GET /api/config` and `PUT /api/config` expose only
`{"privateDMRelayEnabled":false}`. Per-set enablement uses the same field inside
`settings` on `/api/sync-sets/{id}`. Historical global runtime settings are not
accepted by `/api/config`.

In a private chat with the owning WhatsApp sync account, Discord bot, or
Telegram Bot API bot, send:

```text
/dm @{Amber Badger} Can you help with this?
```

Use the alias already seen in forwarded author attribution. It resolves globally
to exactly one `(sync set, actor)` assignment; case-insensitive spelling and
numeric tiers are supported. No endpoint or sync-set prefix is needed. Both
parties must already have group-author alias assignments in the owning set;
private commands and structured mentions do not enroll participants. Discovery
is a workflow assumption, not a read-receipt database or proof of authorization.
The sender's private inbox must match the owning transport/connection and a
currently verified member of an endpoint in that set. All membership/inbox
checks run again on every hop.

Only the addressed person's private inbox receives the message. It has a
source header `DM from <group>/<name> (<alias>)`, body, and native-Reply instruction.
The attribution is bold-italic; missing names, source Outgoing anonymity or
destination Incoming anonymity use `DM from <group>/<alias>`. Names are transient and never stored. Responses
natively reference the sender’s preceding message when available. To answer,
use the platform's native **Reply** on the bot's relayed message. Plain text
after a conversation does not imply a current conversation. Multiple
conversations remain separate through their exact structured reply IDs.

`/help` explains usage and limitations. Telegram `/start` returns that same
help. Ordinary text without an active native reply, unknown commands,
attachments, malformed commands, and replies to help/status/expired messages
receive private help. `/dm` takes priority over an existing reply context.
Commands are case sensitive. Input is at most 4096 UTF-8 bytes and relayed
body text at most 1600 UTF-8 bytes, preserving newlines. V1 supports text only.
Native mentions and embedded participant references are neutralized, and
Discord/Telegram link previews are disabled. No enrollment, contact sharing,
approvals, `/reply`, or participant web inbox exists.

On a WhatsApp sync account, unrelated noncommand personal chats from unobserved
participants are ignored and never added to cleanup. Ordinary inputs from
eligible observed participants, explicit bot commands and recorded native
relay replies are the feature's private inputs.

Group `/dm` messages keep ordinary group synchronization and local-prefix
rules. Private edits, deletes, revokes and reactions are ignored. Editing or
unsending a source DM does not alter the recipient's delivered message.
The recipient may retain that original indefinitely.

## Reachability and failure

Delivery uses a verified owning connection. Candidate recipient endpoints must
belong to the alias's own set. Equivalent routes to the same verified inbox are
deduplicated, using a stable sorted endpoint. Multiple distinct eligible inboxes
fail generically rather than selecting an account. Missing assignments, unknown
aliases and unauthorized/unreachable recipients receive the same initial failure.
WhatsApp new destinations require a verified PN from the current group roster;
a group-observed LID alone cannot initiate a relay. Native private replies can
use an already proven private inbox with matching current group membership.
Provider-proven operational PN/LID forms bind to the existing canonical LID
actor and private chat identity. If a private command or native reply omits its
LID and the protocol mapping cache is cold, the WhatsApp adapter may resolve
its explicit PN/LID pair from current, DM-enabled group rosters within the
bounded ingress lookup. Unknown or conflicting pairs remain unavailable.
A PN fallback never creates a new participant
alias. The roster PN needed to initiate delivery remains an adapter-owned send
address, separate from the assigned alias.
Discord sends use the bot account, with explicit empty allowed mentions, never
a channel webhook. Discord DM gateway intent is requested only on connections
with an enabled relay set; changing that gate reconnects the Discord adapter.

Telegram users must contact the same Bot API bot first, normally with `/start`.
Verified operational Bot API inboxes are encrypted for 24 hours and renewed on
private contact, including after restart. Once that record expires, contacting
the bot again refreshes reachability. The bot must be able to verify current
group membership. Telegram MTProto personal inboxes are unsupported and are
never automatically answered or cleaned. MTProto group behavior continues.

Unknown identity, stale/unverifiable membership, removed/reassigned endpoints,
disabled gates, missing connections or blocked DMs fail closed. Fixed limits
allow 10 private inputs per sender/connection/minute, 50 inputs globally per
minute and 100 relay deliveries per sync set/minute. Excess input traffic is
silently discarded.
Creates are attempted once. Ambiguous delivery is reported as unconfirmed and
is never automatically retried. Provider acceptance does not prove reading or
receipt. A crash between remote acceptance and local mapping persistence can
leave a delivered message without a reply route; no message queue is stored.

## Expiry, privacy and local housekeeping

Each bot-sent relay has a 24-hour reply mapping, measured from its send. The
lookup binds owning connection, exact private chat token and outbound bot
message ID. Only the recorded addressee may reply. Endpoint bindings also
prevent a reused alias from authorizing an old conversation in a new group.
Mappings survive restart while membership remains verifiable.

`sync.db` receives no private messages, canonical copies, native private inbox
identities or private history. Mode-0600 `control.db` stores encrypted,
content-free reply mappings, cleanup keys and verified Telegram operational
inboxes, plus keyed input-deduplication digests. Encryption and lookup keys have
a separate domain derived from `IDENTITY_SECRET`. No DM body/media is persisted,
logged, queued, exposed through admin JSON or added to metrics. Telegram inbox
records are capped at 10,000; mappings, input digests and cleanup candidates
each have a hard 100,000-record cap. Mappings and deduplication expire after 24 hours.
Mappings and cleanup tokens also bind an HMAC of the authenticated account, so
re-pairing or replacing a bot behind the same connection ID cannot reuse them.
The operator and hosting providers can technically observe endpoint identities
and transient content. The relay does not provide end-to-end anonymity or E2EE
between the two people. Participants can still identify themselves in text.

Cleanup runs at startup and every minute in batches of at most 100 provider
operations. Expired metadata is removed in bounded SQL batches up to its hard
record caps. Expiry is
independent of provider deletion support. WhatsApp uses only one-message
`deleteMessageForMe` AppState operations on positively classified private
feature messages. Received inputs and help/status copies are eligible after
handling; relayed outgoing copies wait for their 24-hour reply expiry. It never
uses protocol REVOKE, group delete, clearChat, deleteChat, or whole-chat cleanup.
Account-local deletion can depend on provider/linked-device behavior; a live
manual check must confirm it on the installed account. Failure is preferable
to a participant-visible deletion.

Discord normal message deletion and Bot API `deleteMessage` affect the shared
conversation. They are **never called for private cleanup**. Those provider
inboxes can retain messages indefinitely; only application metadata expires.
No bot-local cleanliness guarantee is made for these platforms. WhatsApp cleanup
gets at most three attempts, one hour apart, and expires at most 72 hours after
eligibility. Attempts are durably counted before provider calls so timeouts and
restarts cannot reset that budget. Unavailable/failed local cleanup still permits metadata expiry.
Cleanup events cannot enter group lifecycle routing or resend private messages.

See [manual checks](TESTING_GUIDE.md#private-dm-relay) and the automated
[test gate](../TESTING.md#private-dm-relay-regression-gate).
