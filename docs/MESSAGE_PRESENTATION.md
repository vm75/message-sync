# Synchronized message presentation

This document defines the rendered bridge attribution contract. Routing uses
opaque identities and explicit aliases; rendered headers never authorize routing.

## Sender attribution

Every accepted group-message author receives a permanent, globally unique alias
belonging to its `(sync set, HMAC actor)` record. The vocabulary has exactly 256
adjectives and 256 nouns, with a frozen order. The first 65,536 assignments use
two words; ordinal 65,537 starts `Amber Badger 2`. Aliases and ordinals are never
reused, even when their set is deleted. The same account in another set receives
a different alias. Names, phone numbers and provider user identities are not
stored in the alias registry.

Ordinary sender-name precedence remains:

1. normalized provider display name;
2. phone number only when no display name is available;
3. opaque actor ID only when neither is available.

Append the assigned alias in parentheses: `Alice (Amber Badger)`. Never add a
phone beside an available display name. Username-mode and discriminator controls
are removed; HMAC hashing still provides internal canonical actor identities.

The router allocates only for accepted group-message creates, after validity,
configuration, echo and local-prefix checks. Allocation and lookup failures stop
delivery rather than substituting a missing or unrelated author alias. Edits,
reaction fallbacks and other author presentation require an existing assignment.
Structured mentions and private commands never enroll a new actor.

WhatsApp author identities require a verified canonical LID. Provider-owned APIs
resolve PN-form messages to that LID using the existing author hash domain;
hosted LIDs normalize to the same identity. PN-only observations do not consume
an alias. If no LID is available, forwarding fails before canonical/create state
or delivery. Recovery checkpoints remain replayable, subject to provider windows;
there is no guarantee of later recovery and no persistent content queue.

## Message header syntax

Ordinary attribution:

```text
<group>/<name> (<alias>): <message>
```

Where supported, the attribution is bold-italic:

```text
*_developers/Alice (Amber Badger)_*: deployed the fix
```

Child messages retain the same slash-separated context:

```text
*_developers/Backend API/Alice (Amber Badger)_*: deployed the fix
```

On anonymized Discord routes, the assigned alias replaces the name entirely:

```text
developers/Amber Badger: deployed the fix
developers/Backend API/Amber Badger: deployed the fix
```

Keep existing source prefixes in webhook names, poll attribution, media captions
and companions, reply fallbacks and reaction fallbacks. Discord reserves the
complete alias before truncating a long real name/source prefix to its 80-rune
webhook username limit. Polls retain the platform bot author where native APIs
require it; the bridge's visible source attribution includes the alias. Native
poll questions/options render structured mentions using assigned aliases (or
non-identifying text when unassigned) on every route, so result companions retain
the same safe labels after restart. If projection changes an option label, the
source's original option hash still maps to its canonical index; forwarded
WhatsApp votes use the projected label's hash. Both contribute to the same count.

## Child-context storage mode

`childContextDisplayMode` controls label persistence, not header syntax:

- `opaque` keeps live thread/topic labels transient and otherwise uses `thread`
  or `topic`.
- `friendly` may persist bounded normalized labels for restart/replay.

Both use `<group>/<child>/<sender>`. Opaque child IDs and `[contexts ...]`
preambles never enter rendered messages. Switching to opaque clears stored
labels. Group/child labels never select an actor or route.

## Route-aware Discord anonymization

With `anonymizeToDiscord=true`, only Discord destinations use alias-only author
presentation and clear phone fields. Other destinations retain ordinary names
with aliases. Structured verified mentions reuse the mentioned author's existing
set alias. A missing assignment uses safe non-identifying mention text on an
anonymized route; ordinary destinations retain safe normal mention presentation.
No invented HMAC pseudonym or separate mention hash domain is used. Mention
normalizers must match the canonical author identity, including WhatsApp PN/LID
reconciliation. Structured names and native IDs remain transient.

Quote-only mention metadata stays transient through ingress, independently of
the current message body. The router applies the quoted actor's existing set
alias before formatting. Anonymized quoted text strips bridge attribution wrappers
and sanitizes structured mention tokens. Other quoted content containing a slash is unchanged.
Edits follow text-only mention semantics without new native notifications.
Explicit braced references remain literal when native rendering is unavailable;
do not rewrite an authored literal merely because anonymization is enabled.

## Cross-group participant references

Only explicit `@{Amber Badger}` or `@{Amber Badger 2}` addresses are supported.
Comparisons are case-insensitive; stored aliases retain canonical title case.
Each token has exactly one space between its ASCII words and optional canonical
numeric tier. Endpoint prefixes, bare names, discriminators and public `Pn`
handles are not alternate addresses. Native provider `@user` syntax is separate.

Users learn aliases from forwarded author attribution. The registry resolves
only already assigned aliases, and group ingress requires the assignment's set
to equal the source endpoint's set. No directory, picker, read tracking, real-name
search or cross-provider identity guessing is involved.

Every actual destination independently verifies membership before a native tag:

- WhatsApp uses matching LID text and mention metadata from its live target
  roster, including captions and audio/sticker text companions; never a PN.
- Discord verifies current guild/channel access and private-thread membership,
  uses an explicit user allowlist, and may restore native bindings from accepted
  history or a bounded HMAC-matched guild-roster lookup when Server Members
  Intent permits it. No additional privileged gateway intent is added.
- Telegram Bot API verifies membership in the destination chat and emits an
  HTML-escaped inline mention. It does not invent extra topic membership rules.

An explicit reference may tag a verified local Discord account even on an
anonymized route; the resulting chip identifies that native account. Every other
sender and ordinary mention anonymization rule remains active.

Unknown, out-of-set or unverifiable references preserve the exact authored token,
including case, braces and spelling. A backslash escapes a reference:
`\@{Amber Badger}` does not request a notification. Repeated references work
independently. Discord/Telegram native-ID caches are transient; cold or denied
lookups remain literal. MTProto and edits do not add native braced-reference
notifications. Database failures are errors, not proof an alias is absent.

## Private relay presentation

The opt-in private relay uses the same assigned set alias, independent of group
anonymization:

```text
DM from Amber Badger
<sanitized private body>
Reply to this message to respond privately.
```

Both parties need existing group-author assignments; `/dm` does not allocate.
The header includes no group prefix, display name, phone, username or native ID.
Reply routing uses the exact structured inbox/message binding, never this text.
Native mentions and embedded bridge references are neutralized. Private edits
and deletes do not change the delivered original. See [private messaging](PRIVATE_MESSAGING.md).
