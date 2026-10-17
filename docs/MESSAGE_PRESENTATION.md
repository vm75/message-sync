# Synchronized message presentation

This document defines the user-visible attribution syntax produced by the canonical router. These formats are compatibility invariants: coding agents must not casually normalize delimiters, expose additional identity fields, or change the precedence rules while refactoring routing, LID handling, threads/topics, reactions, polls, or recovery.

## Sender attribution

The opt-in private relay has its own presentation, outside canonical group
headers. It always uses an endpoint-scoped pseudonym, independent of
`anonymizeToDiscord`, `usernameMode` and discriminator display settings:

```text
DM from <source-endpoint>/<endpoint-scoped pseudonym>
<sanitized private body>
Reply to this message to respond privately.
```

The pseudonym includes its deterministic five-character code. No provider
display name, username, phone, native user ID or quoted message content is
added. User-authored native mentions and embedded bridge references are
neutralized; Discord/Bot API previews are disabled. Private Reply uses the
structured provider message ID only, never this header or any text marker.
Private edits/deletes do not change the delivered original. See
[private messaging](PRIVATE_MESSAGING.md) for expiry and cleanup semantics.

With the default `push_name` username mode, sender presentation uses this precedence:

1. normalized provider display name;
2. phone number only when no display name is available;
3. opaque sender ID only when neither is available.

A phone number must **not** be added alongside an available display name. In particular, do not render `phone (name)`.

With `hash` username mode, the opaque HMAC-derived sender ID is used instead of the display name/phone fallback chain.

## Message header syntax

For an ordinary message, the visible attribution header is:

```text
<group-alias>/<name>: <message>
```

The router currently wraps the attribution portion in bold-italic provider markup where supported, for example:

```text
*_<group-alias>/<name>_*: <message>
```

When `childContextDisplayMode=friendly` and a Discord thread/forum-post or Telegram topic label is available, the visible attribution header is:

```text
<group-alias>/<thread-or-topic-label>/<name>: <message>
```

For example:

```text
developers/Backend API/Alice: deployed the fix
community/Travel Plans/Bob: Saturday works for me
```

The `/` separators are intentional and part of the presentation contract. Do not add `thread:` or `topic:` prefixes unless the product contract is deliberately changed together with tests and documentation.

The child label itself is sufficient presentation context; routing still uses opaque child-scope identity and never parses this rendered header. Opaque child IDs or context tokens must never be shown in forwarded messages.

## Child-context storage mode

The current default is `childContextDisplayMode=opaque`. This setting controls **label persistence**, not the visible header shape.

- `opaque`: use a live thread/topic label transiently when available; otherwise show the generic `thread` or `topic` fallback. Do not persist the child name.
- `friendly`: use the same `<group>/<thread-or-topic>/<user>` header and additionally persist bounded observed labels so names can survive restart/replay.

Both modes therefore use the same human-facing syntax and neither mode emits an opaque context preamble. ChildScope IDs remain internal routing metadata only.

## Participant discriminator display (`showDiscriminator`)

Every sender's discriminator (5–10 characters, selected by the sync set's `codeLength`) is derived from the existing HMAC
actor identity, independently of anonymization. It remains stable while the
provider actor identity and `IDENTITY_SECRET` stay unchanged. It does not link
identities across providers or require another identity mapping table.

The per-sync-set `showDiscriminator` setting defaults to `false`. New sets use eight-character codes; existing sets retain five-character codes on upgrade.
It changes new bridge-generated presentation across all destination transports in that set
without changing identity or reference resolution:

| Mode | Hidden (default) | Shown |
|---|---|---|
| Ordinary sender | `group/Alice` | `group/Alice Q7M5K` |
| Anonymized Discord sender | `group/Silent Falcon` | `group/Silent Falcon Q7M5K` |
| Child attribution | `group/Travel/Alice` | `group/Travel/Alice Q7M5K` |
| Hash attribution | `group/u_abcdefghij` | `group/u_abcdefghij Q7M5K` |

The suffix applies to bridge sender headers, webhook names, media attribution,
polls, edits, reaction fallbacks, and generated anonymized mention names. It is
appended once. Discord webhook truncation reserves space for the suffix.
Provider account names, native mention-chip labels, authored message bodies and
literal cross-group tokens are not renamed or stripped. Existing messages are
not rewritten when the setting changes; subsequent creates/edits use the current
setting. Discriminator addressing remains enabled with either display value.
With display hidden, use a previously known code or enable display to learn it.
Using the same code with real and anonymized names makes those presentations
linkable to someone who sees both.

## One-way Discord Anonymization (`anonymizeToDiscord`)

When a sync set configures `anonymizeToDiscord=true`, messages forwarded to Discord endpoints are subject to route-aware one-way pseudonymization:

1. **Sender name**: In `push_name` mode, the `<name>` attribution component and Discord webhook display name are replaced with a deterministic pseudonym derived from the sender's HMAC actor ID (e.g. `Silent Falcon`, or `Silent Falcon Q7M5K` when discriminator display is enabled).
2. **Phone numbers**: Fallback phone numbers are stripped completely for all Discord-bound senders regardless of `usernameMode`; phone numbers never appear in attribution headers or sender fields forwarded to Discord.
3. **Structured mentions**: Mentions in Discord-bound creates **and edits** have their display names replaced with deterministic pseudonyms (e.g. `@Amber Otter`, with ` 2PF3R` appended when discriminator display is enabled). Telegram and Discord ingress keep user mentions as privacy-safe HMAC actor tokens plus transient display metadata so normal destinations can still render the usual name while anonymized Discord can substitute a pseudonym. Provider-native mention IDs that still require derivation are keyed under the source transport namespace. Anonymization-enabled routing requires the identity hasher.
4. **Reply quotes**: Quoted text headers produced by this bridge (the exact `*_<label>_*: <body>` format) are stripped before being forwarded to Discord; other message content that happens to contain `/` is left untouched.
5. **Reactions**: Reaction fallback text sent to Discord uses the sender's deterministic pseudonym rather than their real name or phone number.
6. **Multi-destination isolation**: The anonymization is applied strictly per-destination. Other destinations in the same sync set (such as WhatsApp groups or Telegram chats) continue to receive standard attribution and real push names in `push_name` mode.
7. **Hash mode**: If the service is configured with `usernameMode=hash`, opaque HMAC actor IDs (`u_...`) are used for message body attribution (as in non-anonymized hash mode), but `PhoneNumber` is still cleared before the message is forwarded to Discord.

## Do-not-regress checklist

Changes touching sender identity, WhatsApp PN/LID resolution, Discord threads/forum posts, Telegram topics, reactions, polls, edits, replies, or routing presentation must preserve these rules unless a product change explicitly says otherwise:

- ordinary attribution: `<group-alias>/<name>`;
- child attribution: `<group-alias>/<thread-or-topic-label>/<name>`;
- `/` separates the group alias, child label, and sender;
- display name wins over phone number in `push_name` mode;
- phone number is only a fallback when display name is absent;
- hash mode continues to use the opaque sender ID;
- `anonymizeToDiscord` pseudonyms apply only to Discord destinations and preserve normal push names for other transports;
- rendered presentation is never parsed as routing identity.

Automated tests should assert the exact rendered forms whenever presentation logic is changed.


## Cross-group participant references

The bridge recognizes explicit `@{endpoint-alias/label}` references in user-authored
message bodies, such as `Hi @{group2/P1}`, `Hi @{group2/Alice Smith}`,
`Hi @{group2/Silent Falcon Q7M5K}`, or `Hi @{group2/Q7M5K}`. The braced syntax, not a bare `@name`,
identifies bridge addressing without colliding with provider-native mentions. Prefix the token with a backslash to display it literally without a
notification attempt. Native `@someone` behavior is unchanged.

The address is made of a configured endpoint alias and a durable per-endpoint
participant handle, allocated when a participant's HMAC actor identity is first
observed on an accepted inbound event. It is **not** a username, a globally
unique person name, or a group-member list position. `group1/P1` and
`group2/P1` are distinct addresses. Group alias renames cascade through
the registry; the old textual address does not automatically redirect.

On Discord endpoints with `anonymizeToDiscord` enabled, senders retain the
deterministic, word-based aliases (e.g. `group/Rust Ocean`, with
` Q7M5K` shown only when `showDiscriminator=true`)
in `push_name` mode. Stable `Pn` handles remain available as optional
fallback addresses; they must not replace the visible pseudonym. Other
recipients retain normal sender-display presentation. In hash mode, the
opaque actor identity is retained, with the same optional discriminator display.

A reference resolves only for an observed participant in an endpoint within
the sender's sync set. Stable `Pn` handles, discriminators and full deterministic
pseudonyms all work across restarts because they use persisted HMAC actor records, not
personal data. A two-word alias stem (e.g. `Rust Ocean`) is also accepted
only when unique within that group. Discriminators are case-insensitive and
must likewise match exactly one registered actor in that group. A code colliding
with another code, observed name or `Pn` handle stays literal, without provider
lookup overriding an ambiguous local result. Full historical pseudonyms with
the suffix remain valid regardless of the display setting; ordinary real names
remain separate from their optional displayed suffix (name-plus-code is not a
new reference form). Full pseudonyms remain stable when
`IDENTITY_SECRET` and the provider identity stay the same. Real-name lookup
first uses transient observed names. On a cache miss after restart, WhatsApp
and Discord ask the referenced group's adapter for a unique exact name/alias
match among that group's registered HMAC actors. WhatsApp uses its live source
roster and existing provider-owned contact metadata; Discord searches current
guild members and verifies access to the referenced source channel. Readable
names remain transient and native IDs stay inside the adapters. If metadata
is unavailable, the reference stays literal. Ambiguous names or alias stems
never trigger a native tag. The router never parses forwarded
attribution headers or guesses a native identity.

For an authorized reference, each destination independently verifies the
addressed person's native identity and current membership. The group in the
address selects the participant; it does not limit which destination groups
can mention them. WhatsApp requires a known LID and writes both the native
`@<LID user component>` token and matching `MentionedJID` metadata, without
exposing a phone number. This also applies to image/video/document captions
and audio/sticker text companions. Discord uses an observed native ID, current guild
membership and channel access checks, plus private-thread membership where
applicable, and an explicit allowed-user list. An explicit braced reference
may tag a verified local Discord member even on an anonymized route; the chip
identifies that native Discord account. Sender and ordinary provider-native
mention anonymization remain unchanged. Telegram Bot API checks `GetChatMember`
and emits an HTML inline mention only for a current member.

Every other destination keeps the reference exactly as posted. For example,
`Hi @{group2/Alice Smith}` stays `Hi @{group2/Alice Smith}` when Alice is
absent or cannot be safely resolved there. Pseudonym and `Pn` inputs likewise
retain their original spelling and braces. Unknown, ambiguous, unauthorized,
and escaped references never trigger a mention. Anonymized routes also retain
literal user-authored braced references; they do not substitute pseudonyms or
mask names in those literals. Existing sender and structured provider-native
mention anonymization remains unchanged.

Discord and Telegram Bot API hold native IDs only in adapter memory, so a
native identity must be observed by that adapter after restart. Discord
restores this lookup from live messages and accepted channel/thread history
recovery. It can also resolve a cold cache by matching HMAC actors against
paginated live guild identities under a five-second timeout, when Server
Members Intent permits that roster REST lookup. Real-name lookup can instead
rebuild a binding through the referenced group's member search, restricted to
registered HMAC actors and verified source-channel members. Display names
never link identities across providers. Lookup failures preserve literal text. No identities
are linked across providers by matching names. WhatsApp can verify identity
from its live destination roster. Native notification remains best effort.
Telegram MTProto and edits keep literal reference text and do not create new
native mentions. Autocomplete and a participant picker are outside this
experiment's scope.

Incoming native provider mentions follow their existing separate
normalization/presentation path. Cross references are not persisted with
message content or native IDs.

Changing `codeLength` changes the displayed code length without changing actor
identity. Supported historical lengths (5–10 characters), full historical
pseudonyms, unique word stems and `Pn` handles continue to resolve within the
same sync set. Shorter ambiguous codes remain literal, even if a longer code
would distinguish the actors. Display and addressing are independent of
anonymization. Settings are saved through `PUT /api/sync-sets/{id}` under
`settings`, not the retired global runtime API.
