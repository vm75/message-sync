# Implementation Plan: Globally Unique Sync-Set Aliases (256 × 256)

**Repository:** `vm75/message-sync`\
**Implementation branch:** `feature/anonymous-private-dm-relay`\
**Status:** Implemented; automated verification complete\
**Supersedes:** `alias_plan.md` (ordinal-based alias/discriminator replacement)

## Goal

Replace HMAC-derived pseudonym/discriminator addressing with **globally unique, persistent, human-readable aliases** that identify an *actor within a sync set*, not a global person. Use exactly **256 adjectives × 256 nouns = 65,536 unique unsuffixed two-word aliases**. Continue allocation beyond the pool using numeric tiers. Preserve the sole user-facing reference syntax `@{Amber Badger}` across groups and `/dm @{Amber Badger} message` in private chat; **never require a sync-set or endpoint prefix**.

Same underlying platform account may have a different alias in every sync set. An alias resolves globally to one `(sync_set_id, actor_id)` record, but may be used only after membership and destination-specific authorization.

Cross-group addressing and discriminator behavior on this branch are unreleased. Replacing their syntax and presentation is intentional; legacy compatibility is not required. Changes must **not** incidentally erase unrelated persisted configuration or existing production data.

## 0. Contract and design decisions

**Review decisions:** Hash-mode removal includes code, configuration, UI, tests and documentation cleanup as specified in §6. Review point #2 uses the recommended verified-LID-only WhatsApp policy in §4d: preserve stable actor identity without adding persistent PN-to-LID identity merging, accepting the documented availability tradeoff. For points #3 and #4, assume users have already observed the alias before sending a DM or mentioning it; do not add discovery tracking or a directory. These assumptions do not replace membership and destination authorization checks.

| Concern | Decision |
|---|---|
| Identity scope | `(sync_set_id, actor_id)`. A platform actor in two sets has two records and two aliases. No cross-platform person linking. |
| Alias uniqueness | Instance-wide, case-insensitive, enforced in SQL. Never reuse aliases or ordinals. |
| Allocation | One durable instance-wide, monotonic `int64` ordinal, transactionally allocated, not reset by sync-set/endpoint deletion. |
| Vocabulary | Exactly 256 unique adjectives and 256 unique nouns; frozen lists and a fixed bijective slot permutation. 65,536 pairs. |
| Overflow | Tier suffix beginning with `2` at ordinal 65,537. |
| Display, anonymize=false | `Alice (Amber Badger)`; include source endpoint where existing attribution format requires it: `group/Alice (Amber Badger)`. |
| Display, anonymize=true | `Amber Badger`; existing endpoint prefix retained only where attribution uses it: `group/Amber Badger`. |
| Username modes | Remove `usernameMode`, including `hash` and the redundant `push_name` selector. Route-aware anonymization alone determines name-plus-alias versus alias-only display; HMAC actor IDs remain internal identities. |
| Alias discovery | Users address aliases already observed in forwarded author attribution. No alias directory, read-receipt tracking, DM bootstrap or mention-only enrollment is required. |
| Addressing | `@{Amber Badger}` or `@{Amber Badger 2}` only. No `@{set/alias}` and no `@{endpoint/P1}`. |
| Internal `Pn` ordinals | Remain endpoint-local for membership/address bookkeeping; not a user-facing address. |
| Direct message | `/dm @{Alias} text`; reply to relayed message to continue, preserving current private relay TTL, authorization and cleanup rules. |
| Discriminator | Remove settings, API/UI, transport fields, rendering and obsolete tests. No compatibility shim. |
| Failures | Fail closed on required author allocation, identity normalization, lookup, authorization and ambiguous inbox selection. Never silently substitute an unrelated pseudonym for an assigned alias. Unassigned ordinary structured mentions use the sanitized fallback below. |

**Examples:**

| Sync set | Account | Alias |
|---|---|---|
| Set A | WhatsApp account X | Amber Badger |
| Set A | Discord account Y | Azure Falcon |
| Set B | WhatsApp account X | Rust Ocean |
| Set B | Telegram account Z | Silver Otter |

Both `Amber Badger` and `Rust Ocean` resolve to one unique record each. They are **not** evidence that these records represent the same real person.

**Discovery precondition:** users learn an alias from an already-forwarded author message before using it in a braced reference or `/dm`. The relay requires existing author alias assignments for both parties in the owning set so it can address the recipient and render the sender header. It does not enroll a private-only or mention-only actor. Missing assignments produce a generic failure. Observing an alias is a user-workflow assumption, not an authorization signal: do not track who has read a header, and retain every live membership/inbox check.

## 1. Schema and configuration persistence

### 1a. Global allocator and actor registry

Implement in `internal/store/schema.sql` and idempotent store initialization:

```sql
CREATE TABLE IF NOT EXISTS alias_sequence (
    singleton    INTEGER PRIMARY KEY CHECK (singleton = 1),
    next_ordinal INTEGER NOT NULL CHECK (next_ordinal >= 1),
    exhausted    BOOLEAN NOT NULL DEFAULT 0 CHECK (exhausted IN (0, 1))
);
INSERT INTO alias_sequence (singleton, next_ordinal)
VALUES (1, 1) ON CONFLICT(singleton) DO NOTHING;

CREATE TABLE IF NOT EXISTS sync_set_actors (
    sync_set_id TEXT NOT NULL REFERENCES sync_sets(id) ON DELETE CASCADE,
    actor_id    TEXT NOT NULL,
    ordinal     INTEGER NOT NULL UNIQUE CHECK (ordinal > 0),
    alias       TEXT NOT NULL COLLATE NOCASE UNIQUE,
    PRIMARY KEY (sync_set_id, actor_id)
);
```

- An index for `(sync_set_id, actor_id)` is provided by the primary key; the unique `alias` index supports global case-insensitive lookup. `ordinal` is globally unique, **not** unique merely within a set.
- Persist the **resolved alias string**, not only the ordinal. Do **not** use a generated SQL column requiring a custom SQLite function, or scan all actors to resolve aliases.
- `alias_sequence` must not reference `sync_sets`, `endpoints`, or any row with a cascading delete. Never decrement or reset it, including after sync-set deletion. The counter retains issued ordinal history without retaining deleted actors or their addresses.
- Deleting a complete sync set may cascade-delete its actor rows by deliberate user action. Their ordinals remain consumed permanently; the original aliases will never be assigned again.
- Keep existing `participant_addresses.endpoint_id NOT NULL ... ON DELETE CASCADE`; **reject the old Phase 1b `ON DELETE SET NULL` proposal** because it loses the endpoint partition key and conflicts with the primary key. Endpoint deletion must not delete `sync_set_actors` records for a still-existing set.
- Preserve unrelated persisted metadata, DM state, and endpoint settings. Use the repository's schema/versioning practices for any changes to already-deployed tables; no compatibility layer is needed for the unreleased alias syntax.

### 1b. Fix `config.Save()` before enabling the registry (critical)

`internal/config/config.go` currently runs `DELETE FROM endpoints; DELETE FROM sync_sets;` and recreates them during a save. The proposed registry references `sync_sets(id) ON DELETE CASCADE`, so that flow would delete aliases whenever settings are saved.

Replace whole-table rewrites with **transactional, differential upserts/deletes**:

1. Preserve identical sync-set IDs and endpoint aliases as the same physical SQL rows on ordinary saves.
2. Update changed settings and endpoint bindings in place where safe; delete only intentionally removed entities.
3. Handle endpoint rebinds/removals by invalidating stale endpoint-bound observation/DM state as appropriate, without deleting the sync-set alias registry.
4. Preserve and test child-scope labels and all other dependent state as required by their existing ownership rules.
5. Verify foreign-key behavior under `PRAGMA foreign_keys=ON`. Never work around cascades by disabling foreign keys.
6. Settings-only saves must leave `sync_set_actors` and `alias_sequence` bit-for-bit stable.

### 1c. Transactional allocation

`ObserveSyncSetActor(ctx, setID, actorID)` must be one logically atomic operation:

1. Validate nonempty, canonical inputs and ensure `setID` exists.
2. Look for an existing `(setID, actorID)` record; return its stored alias unchanged.
3. In a serialized write transaction, recheck existence, consume `next_ordinal`, derive the candidate alias and insert the actor row; advance the counter in the same transaction.
4. Commit only when all steps succeed. On uniqueness/busy contention, retry the **whole** transaction within a bounded context; if another writer inserted this actor, return that row.
5. The last positive `int64` ordinal sets `exhausted` without overflowing `next_ordinal`; subsequent allocations fail permanently. Expose precise errors on exhausted `int64`, database failures and inconsistent stored data. No fallback to an HMAC name, empty alias or `Member`.

Choose an SQLite transaction strategy that actually serializes writers in the project's Go driver; test parallel callers and multiple store connections. Do not rely solely on `SELECT MAX(ordinal)+1` or process-local mutexes. No allocation should be committed for rejected, unknown, or spoofed identities.

## 2. Identity package: 256 adjectives × 256 nouns

### 2a. Freeze a 65,536-name vocabulary

In `internal/identity` add or replace the alias vocabulary with:

- **Exactly 256 distinct adjectives** and **256 distinct nouns**, including the existing words where suitable.
- Words are ASCII alphabetic, Title Case, reasonably short and pronounceable, with no whitespace, digits, punctuation, slurs, hateful terms, impersonation of service/system actors, or confusing command-like words.
- Each list must be case-insensitively unique; entries from one list may also occur in the other without pair collisions.
- Freeze contents **and index order** before shipping. Add tests asserting counts, uniqueness, grammar, maximum displayed length and a fixed vocabulary checksum. A future change requires an explicitly versioned plan, because persisted aliases must remain unambiguous.
- Do not retain the 64×64 legacy word generation merely for pseudonym compatibility. Retire dead discriminator/pseudonym APIs after removing callers.

### 2b. Deterministic bijective mapping

Use this explicit, frozen mapping for ordinal `n >= 1`:

```go
const adjectiveCount = 256
const nounCount = 256
const aliasPoolSize = adjectiveCount * nounCount // 65536

zero := n - 1
slot := uint16(zero % aliasPoolSize)
slot ^= slot >> 7
slot *= 0x9e37 // intentional uint16 wrap; odd multiplier
slot ^= slot >> 9
slot *= 0x85eb // intentional uint16 wrap; odd multiplier
slot ^= slot >> 8
adjIndex := slot / nounCount
nounIndex := slot % nounCount
tier := zero/aliasPoolSize + 1
alias := adjectives[adjIndex] + " " + nouns[nounIndex]
if tier > 1 {
    alias += " " + strconv.FormatInt(tier, 10)
}
```

| Ordinal | Alias shape |
|---|---|
| 1 | `Amber Badger` |
| 2 | `Pale Poppy` |
| 3 | `Jolly Coconut` |
| 256 | `Everglade Narwhal` |
| 257 | `Grand Larch` |
| 65,536 | `Star Toucan` |
| 65,537 | `Amber Badger 2` |
| 131,073 | `Amber Badger 3` |

Therefore **65,536 distinct aliases have exactly two words**; all subsequent allocations have an unambiguous integer suffix. No random collision estimates apply: allocation is mathematically collision-free when the vocabulary is unique and the mapping remains fixed. Persist strings as an additional safeguard. Reject ordinals outside the positive `int64` range.

**Deterministic scrambling:** XOR shifts and odd multiplication modulo 65,536 are invertible; their composition visits every pair once per tier while spreading successive allocations across both word lists. Freeze the constants, operation order and 16-bit arithmetic alongside the vocabulary. This hides the obvious visual allocation sequence, not the ordinal from someone who knows the mapping; it is not cryptographic secrecy. New databases select the scrambled mapping. An installation-level `alias_order` record preserves original sequential assignments and their allocation namespace across upgrades without rewriting issued addresses.

### 2c. Alias parsing and formatting

- Canonical stored aliases have one space between words, Title Case, and an optional suffix `2` or greater.
- Accept surrounding whitespace only where the command parser already permits it; do not loosen token boundaries or interpret arbitrary plain-text mentions as trusted identities.
- Parse `@{Adjective Noun}` and `@{Adjective Noun N}`; validate the token as a whole and resolve through the DB. Case-insensitive comparisons are acceptable because SQL enforces uniqueness under the same rule.
- Do not allow `/`, endpoint prefixes, nested braces, newlines, extra words, or very long suffixes. Set explicit parser limits and use robust Unicode/UTF-8 validation. Unknown tokens remain literal in group messages; direct `/dm` fails generically.

## 3. Store interfaces and lookup semantics

Add these operations (names may follow existing Go conventions):

| Operation | Contract |
|---|---|
| `ObserveSyncSetActor(ctx, setID, actorID) (AliasRecord, error)` | Atomic, idempotent allocation; returns stored ordinal and alias. |
| `AliasByActor(ctx, setID, actorID) (string, error)` | Fetch existing assignment, without implicitly allocating an unverified actor. |
| `ActorByAlias(ctx, alias) (AliasRecord, error)` | **Global** indexed lookup returning `{SyncSetID, ActorID, Ordinal, Alias}`. |
| `ActorInSyncSetByAlias(ctx, setID, alias)` | Scoped wrapper: global lookup then require `record.SyncSetID == setID`. |
| `ActorEndpoints(ctx, setID, actorID)` | List observed endpoint memberships **within that set** for discovery only, not as authorization proof. |

- Distinguish not found from DB failure. Never interpret a database error as an unresolvable token and silently emit a different sender identity.
- Alias lookup must not allocate.
- Allocate lazily only for trusted, currently validated group-message authors; do not populate aliases from private commands, structured mentions or every historic `participant_addresses` row. Braced references and DM use existing assignments only.
- `participant_addresses` stays endpoint-scoped. Keep internal `Pn` values if required by existing logic, but remove them from the reference API/UI.
- Audit all reads/writes of the registry for SQL parameterization, transaction safety, case-insensitive uniqueness, and permanent nonreuse.

## 4. Router: ingest, presentation, reference resolution

### 4a. Trusted actor observation

For each **accepted group-message create**, determine the owning sync set and normalize its authoritative actor ID. Allocate the alias before producing outbound presentation; other presentation paths look up existing assignments. Never silently drop `ObserveParticipant` or alias-allocation errors while forwarding an unattributed message.

- Author: allocate once per `(set, actor)` on an accepted group-message create after inbound validity, endpoint routing and echo/drop checks; retain the existing endpoint participant observation and adapter-native binding. Presentation-dependent mutations use existing assignments.
- Structured mentions: use an existing assignment only for a platform-verified native user identity. Assume addressable participants have already authored a forwarded message exposing their alias; do not create alias or endpoint-observation records merely because someone mentions them.
- WhatsApp: reconcile PN/LID native mention identities with the **same canonical hashed actor ID** used for author messages. Do not create a new actor using raw JIDs or a separate `mention:` hash domain.
- Discord/Telegram: use existing canonical native-user hashing domains consistently between authors and verified mentions.
- Freeform text such as `@Alice` does not authorize alias creation. Unsupported/unverifiable or not-yet-assigned structured mentions keep the existing safe non-anonymized rendering; anonymized destinations use sanitized non-identifying text without an invented alias. This fallback does not relax the mandatory assigned alias for forwarded authors.
- Check reactions, edits, replies/quotes, polls, media captions and synthetic attribution paths: all verified actor references must use the existing set-specific alias or fail safely.

### 4b. Sender data and rendering

Update `transport.Sender` to carry `Alias`; remove `Discriminator`. The presentation layer must obtain the alias **once** for the message's owning set and propagate it consistently to all target adapters.

- With anonymization off: `Alice (Amber Badger)`.
- With anonymization on: `Amber Badger`.
- Keep an endpoint label where existing attribution contract requires it, e.g. `source-group/Amber Badger: ...`. This endpoint label is a *display context*, never part of the address syntax.
- Do not leak a real display name when anonymization is enabled; preserve the alias even when ordinary names are truncated.
- For verified structured mentions with an existing assignment, display the same alias already shown for that account as an author. Do not allocate during rendering. Preserve quote-only mention metadata through ingress. Native poll question/option mentions use route-neutral assigned aliases (or non-identifying fallback) before encrypted poll presentation is retained, so result companions remain safe after restart.
- Remove the `usernameMode` branch entirely. There is no `hash` mode or configurable `push_name` mode: non-anonymized sender names retain the current display-name, then phone, then opaque-ID fallback precedence; anonymized destinations use only the assigned alias and clear phone numbers. Opaque IDs remain a last-resort non-anonymized fallback, never a selectable display mode.

### 4c. Cross-group `@{alias}` references

Change `internal/router/references.go` to recognize only the new token grammar. `CrossReference` should be minimal:

```go
type CrossReference struct {
    Token   string // exact source token
    ActorID string // canonical opaque ID
    Alias   string // canonical assigned alias
    Name    string // optional transient display name; not persisted as identity
}
```

Do **not** use `CrossReference.Endpoints []EndpointID` as a native-mention authorization target. The actual outgoing target endpoint and child scope must be supplied by the outgoing message context.

Group-message resolution:

1. Parse candidate alias token.
2. Resolve its unique `(setID, actorID)` globally, or use a scoped SQL lookup.
3. Require `record.SyncSetID == originatingMessage.SyncSetID`.
4. Pass actor identity and canonical alias to each outgoing adapter.
5. For each actual target, verify current membership/permission in **that specific target** before emitting a native mention. Historical observed endpoints are candidate identity hints only.
6. If out of scope, missing, or unverified: preserve an appropriately escaped **literal alias reference** rather than turning it into a mention or exposing private identity. If the DB failed, stop or retry as appropriate; don't treat infrastructure failure as proof of absence.

Resolve references independently for each destination; a member of one Discord server/channel is not automatically mentionable in another.

### 4d. Selected WhatsApp PN/LID policy

**Implemented recommendation for review point #2:** require a verified canonical LID before assigning a persistent WhatsApp author alias. Do not allocate an alias from the current PN-only fallback. This avoids introducing a permanent identity-merging table or changing an existing HMAC actor ID when the provider later supplies its LID.

- Resolve and normalize LIDs through the existing adapter/provider APIs, including hosted-LID normalization. Derive the actor using the existing author hashing domain, consistently for authors, verified mentions and private-inbox checks; never query protocol SQLite directly.
- If an author arrives with only a PN and no verified LID can be obtained, return a safe classified failure before committing canonical/create-delivery state or sending to any destination. Do not issue a PN-based alias or advance a recovery checkpoint past payload-dependent work that remains unsafe to forget. Use existing recovery mechanisms; do not persist message content or introduce a new payload queue.
- A structured mention without a verified LID cannot select or create an alias; use the sanitized mention fallback. Private requests without a verified binding to the existing LID actor fail generically.
- With this policy, an initial PN-only observation consumes no alias ordinal; after the adapter can verify the LID, a replay/accepted author create receives its first assignment. Subsequent PN-form messages resolve to that same LID actor and alias, including after restart.
- **Tradeoff:** forwarding waits or fails when LID metadata is unavailable, and recovery remains limited to each provider's supported window. Do not promise guaranteed later delivery. This availability tradeoff is part of the selected policy; document it in user-facing setup and presentation guidance.

## 5. Transport adapters: actual-destination verification

### Discord

- Update webhook username and text attribution for name-plus-alias or alias-only rendering.
- Current code uses `maxWebhookUsernameRunes = 80` (not 32). Truncate the *real name/prefix first* so the alias remains intact; respect actual Discord limits and sanitize allowed characters.
- `nativeCrossMention` must verify the referenced Discord user in the actual outbound guild/channel and, where applicable, private thread and effective posting context. Cached member IDs may help identify a candidate but are not proof of current permission.
- Preserve existing webhook, embed, poll, and quote presentation behavior subject to new alias formatting.

### WhatsApp

- Use verified PN/LID canonical actor IDs consistently on ingest, observed membership and structured mentions.
- Native mention metadata may be emitted **only** when the addressed actor is proven present in the actual target group and the appropriate canonical mention identifier is available.
- Fallback text retains the alias, with and without anonymization. Verify quotes, edits, captions, media and participant-name formatting.

### Telegram

- Bot API `GetChatMember`/supported target-specific checks determine whether a native mention is safe for the actual destination chat. Do not infer topic/thread membership beyond what Telegram can verify; use text fallback when uncertain.
- Preserve private-chat `/start` reachability requirement and existing MTProto exclusions.
- Keep attribution consistent in topics, replies and captions.

### Shared reference formatting

Update `transport.ReferenceDisplayLabel` and related helpers to use canonical alias. With anonymization disabled, an explicitly permitted real name may accompany the alias; the alias is always present. Audit all adapters for old `Handle`, `Pseudonym`, `Endpoint`, `Discriminator` assumptions.

## 6. Remove discriminator and username-mode features completely

- Delete `ShowDiscriminator`, `CodeLength`, discriminator defaults, validation, JSON/YAML/API fields, global and per-sync-set settings controls and any dead crypto/presentation helpers.
- Remove `UsernameMode`, `UsernameModeHash`, `UsernameModePushName`, enum validation/defaults, `usernameMode` JSON/API/UI controls, router branches and mode-dependent adapter behavior. Remove the `Identity` configuration wrapper if it has no remaining fields. Keep `IDENTITY_SECRET`, HMAC hashing, actor IDs and route-aware `anonymizeToDiscord` behavior.
- Remove mode parameters/fields from adapter options, normalizer constructors, app/connection wiring, router settings/helpers and safe log fields; simplify callers and tests instead of retaining constant `push_name` plumbing.
- Existing `settings_json` may contain `usernameMode`; stop reading/importing it and remove only that obsolete key during narrowly scoped idempotent initialization or settings saves. Preserve every unrelated setting and alias assignment. Keep a legacy `global_config.username_mode` column inert if dropping it is unsafe; do not re-import it into active settings or reset tables.
- Reject removed `usernameMode` fields in strict API requests; cover old stored values being ignored and other settings surviving restart. Remove enum/hash-rendering tests while retaining independent attribution, anonymization, identity stability and configuration-reload coverage.
- Update `internal/config/sync_set_settings.go`, `internal/config/config.go`, `internal/api/sync_sets.go`, web UI and all call sites.
- Remove obsolete test fixtures and `router/discriminator_test.go` if it exclusively exercises the retired feature; migrate tests that cover independent anonymization behavior.
- If an old persisted DB column cannot safely be removed in place, make it inert and document its later cleanup; **do not** delete other configuration records to tidy up obsolete schema.

## 7. Private DM relay: global lookup, scoped authorization

### 7a. Command

Only require:

```text
/dm @{Amber Badger} Hello
/dm @{Amber Badger 2} Hello
```

No source-set selector, endpoint prefix, new discovery commands or interactive chooser. Replies to delivered messages continue a conversation via the existing native reply ID/TTL mapping. `/help` remains supported; other unsupported commands/non-reply text receive the help message under current relay rules.

### 7b. Initial DM authorization and recipient routing

`internal/privatedm/relay.go` currently derives the owning set from `CrossReference.Endpoint`; instead:

1. Parse and globally resolve alias to **one** `(syncSetID, recipientActorID)` pair.
2. Verify private DM relay is enabled for that sync set.
3. Verify the sender's authoritative private-inbox identity and current membership in **an endpoint belonging to that recipient's set**, with matching transport and bot connection. Require the sender's existing group-author alias assignment in that set for the DM header; do not allocate from a private command. Historical observation alone is insufficient for current membership.
4. Reject self-messaging when the canonical private actor/inbox equals the recipient, according to existing rules.
5. Find the recipient's eligible endpoints **within the same set** from existing author observations; independently verify live membership, bot connection, recipient's private inbox and DM deliverability. No special mention-only enrollment/discovery path is needed under the observed-alias precondition. Telegram recipients must have started the bot.
6. Deduplicate equivalent routes reaching the **same verified inbox**; choose a deterministic stable endpoint among truly equivalent routes. If there are multiple distinct eligible inboxes, **fail closed** rather than guessing or choosing lexicographically first.
7. Bind both parties to their verified endpoint/connection/inbox and store existing conversation mapping; revalidate on every reply as today.
8. For missing, unauthorized, ambiguous or unavailable recipients, return the same generic failure, avoiding alias-existence and cross-set membership leaks.

Do not create a cross-set address book. Lookup is global, but permission is still set-local.

### 7c. DM privacy and lifecycle

- Use `DM from group/name (alias)` with bold-italic attribution, or `DM from group/alias` when the name is unavailable or Discord anonymization applies. Use the existing stored alias in `mapping.SyncSetID`; missing assignments fail generically without private enrollment. Keep display names transient. Encrypt the author message routing token with the reply mapping so responses reference the preceding author message.
- Preserve text-only scope, 1,600-byte body cap, rate limits, current native reply mapping, 24-hour mapping expiry, anti-spoofing checks and uncertainty-aware delivery failures.
- Preserve WhatsApp **delete-for-me only** semantics on the bot account. Do not claim equivalent self-only deletion for Discord or Telegram Bot API; do not support DM delete/edit propagation.
- Configuration updates and restarts must not silently invalidate aliases or broaden private-message permissions. Disablement/revocation fails closed.

## 8. Documentation updates (same change set)

| File | Required update |
|---|---|
| `README.md` | One alias syntax; 256×256 capacity; examples with and without anonymization; remove username-mode configuration. |
| `ARCHITECTURE.md` | Global allocator/registry, scoped identity and authorization, transaction ownership, native mention checks. |
| `AGENTS.md` | Nonreuse, fail-closed, source and destination authorization, config-save invariant. |
| `CHANGELOG.md` | Breaking syntax/presentation change and removal of discriminator and username-mode settings. |
| `docs/MESSAGE_PRESENTATION.md` | Definitive author, mention, reply, caption and webhook alias contract. |
| `docs/PRIVATE_MESSAGING.md` | `/dm @{alias}` only, observed author-alias precondition for both participants, sender-set verification, reachable-inbox uniqueness, reply TTL, cleanup constraints. |
| `DOCKERHUB.md` | Remove obsolete discriminator/username-mode configuration and old addressing examples. |
| `TESTING.md` | Update alias setup, retired discriminator/username-mode checks and expected rendering. |
| `docs/TESTING_GUIDE.md` | Update cross-group references, anonymization and DM scenarios. |
| `docs/FEATURE_COMPARISON.md` | Remove obsolete/discriminator syntax where present. |
| `docs/TELEGRAM.md` | Update Bot API/MTProto mention and DM examples where present. |

Search the whole repository (code, UI, docs, tests, examples) for `discriminator`, `showDiscriminator`, `codeLength`, `usernameMode`, `username_mode`, `UsernameMode`, `hash` display-mode references, old `@{endpoint/...}` and exposed `P1` references. Retain only intentional historical release notes, removed-field rejection tests and inert legacy-column upgrade coverage for retired settings. Preserve valid *internal* `Pn` discussion but remove it as a public address. Avoid claiming absolute anonymity or end-to-end encryption; aliases are linkable **inside their owning set**, and ordinal allocation is instance-wide.

## 9. Test plan and release gates

### 9a. Identity and vocabulary

- Exactly 256 adjectives and 256 nouns, case-insensitive uniqueness, canonical grammar, stable checksum and golden examples.
- Bijection for **all 65,536 unsuffixed slots**, with zero duplicates under SQLite `NOCASE`.
- Boundary ordinals `1`, `255`, `256`, `257`, `65,535`, `65,536`, `65,537`, `131,072`, `131,073`; correct suffixes for tiers `2`, `3`.
- Extreme positive ordinal, invalid/overflow ordinal, unusual case and malformed suffix parsing.
- Same platform actor observed in two sets receives distinct aliases; a repeat observation in one set returns its original alias.

### 9b. Store and configuration

- Concurrent observations (same and different actors), multiple DB connections, retryable SQLITE_BUSY, rollback on failure, idempotent restart and case-insensitive global uniqueness.
- Deleting one endpoint leaves the set's alias assignment unchanged. Deleting a set removes its actor rows but cannot reuse retired ordinals/aliases. New set is not accidentally assigned an old alias.
- Settings-only config save, endpoint edit, anonymization toggle and subsequent restart leave global counter and retained actor mappings unchanged.
- No 90-day pruning can reset the alias namespace; no foreign-key dangling rows after a set/endpoint removal.
- Failed allocation/lookup returns error without anonymous fallback or false success.

### 9c. Router and presentation

- Ingress accepted/rejected/echo cases; accepted authors allocate once, while structured mentions/references reuse an existing author assignment without allocating or creating participant rows.
- A mention before any author assignment follows safe fallback behavior and creates no alias; after the author posts, the same verified actor renders its assigned alias.
- WhatsApp PN/LID canonicalization against real sender actor IDs; Discord/Telegram author-versus-mention identity equality. For the selected LID-only policy, test initial PN-only failure with no ordinal consumed or delivery/checkpoint side effects, subsequent verified LID allocation, later PN-to-LID resolution and stability after restart.
- Anonymization on/off, identical cross-target aliases, Unicode content, special-character escaping, long sender names, quote/reply/edit/reaction/poll/media attribution.
- No selectable hash mode: ordinary display-name/phone/opaque-ID fallback remains intact; anonymized destinations use aliases and expose no real name or phone. Legacy stored `usernameMode=hash` is inert, removed API fields are rejected, and identity hashing remains unchanged.
- Unknown alias in group stays literal; alias belonging to a different set cannot create a native mention or disclose a real name.
- Correctly handle allocation/lookup failure without leaking identities or silently routing incorrectly.

### 9d. Real target authorization

- WhatsApp group roster and canonical LID checks; Discord guild/channel/private-thread checks; Telegram Bot API chat membership/topic limitations.
- Historical membership in another endpoint must never authorize a native mention in the current destination. Route snapshots retain their set namespace, and queued creates recheck current source/target set ownership before provider work; reassignment cannot move stale content or alias references into another set.
- Revoked member, inaccessible scope or unverifiable identity receives safe fallback text, not native mention metadata.

### 9e. Private relay

- `/dm @{alias}` across supported networks with existing author aliases/endpoint observations for both participants and a valid sender in the recipient's set; no sync-set selector required. Alias discovery is assumed, not tracked as read receipts or used as proof of membership.
- Same account in multiple sets resolves using its **two different aliases**; no accidental cross-set routing.
- Unknown alias, missing sender assignment, mention-only/unassigned participant, unauthorized sender, removed member, disabled DM, self-message, stale inbox, ambiguous distinct inboxes, and missing Telegram `/start` fail closed with non-disclosing errors. Failed DM requests allocate no aliases.
- Multiple endpoint candidates for the **same** verified private inbox resolve deterministically; distinct eligible inboxes never auto-select.
- Reply-to-chain across both participants, restart, 24-hour expiration, rate limits, exact binding/identity verification on each hop.
- DM header uses sender's assigned set alias; WhatsApp owner-local cleanup preserves recipients' message copies, Discord/Telegram limitations are respected.

### 9f. Repository acceptance criteria

- Run `make fmt`, `make test`, `make vet`, relevant race tests (`go test -race ./...` where feasible), frontend checks/build, and integration scenarios available in the repo.
- No active discriminator or username-mode settings or old public reference formats remain in code/docs/UI/tests; historical notes, removed-field rejection tests and inert legacy-column upgrade coverage are intentional exceptions.
- A settings-only save never deletes sync sets/endpoints and never resets alias assignments.
- Aliases are globally unique, stable across restart, never reused, and strictly authorized to the owning sync set.
- All supported transports use the **actual destination** for native mention membership verification.
- DM has a single address syntax with no additional prefix or selector.

## Implementation order / agent handoff

Work **only** on `feature/anonymous-private-dm-relay`, building on existing changes. Separate commits for easier review, but do not introduce compatibility scaffolding for unreleased features:

1. **P0:** Fix `config.Save()` upserts and verify all dependent state is preserved correctly.
2. **P0:** Add schema, global permanent sequence, atomic registry allocation and 256×256 word lists/golden tests.
3. **P0:** Normalize author and structured-mention actor identities; apply the selected LID-only policy, wire fail-closed author alias allocation and reuse existing aliases for mentions.
4. **P0:** Replace group reference parsing/resolution; enforce source-set and actual target membership.
5. **P0:** Update WhatsApp, Discord and Telegram native/fallback rendering and mention verification.
6. **P0:** Update private DM resolver, sender-set authorization, existing-alias preconditions, unique inbox routing and DM alias header; add no DM/mention-only enrollment.
7. **P1:** Remove discriminator and username modes from settings, UI, API, app/adapter wiring, router rendering and dead code.
8. **P1:** Update all documentation and regression tests; run complete release gates.

Stop and report a blocker rather than weakening membership checks, substituting noncanonical identities, guessing endpoints, or wiping persisted tables. Do not merge the branch or bump the release version without a separate request.

## Explicitly out of scope

- Public searchable participant directory or contact-exchange feature.
- Globally linking the same real-world person across transport accounts or sync sets.
- Prefix-qualified addressing, alternate `/dm` selectors or additional bot commands.
- Supporting edits/deletes of previously relayed private messages.
- New MTProto native-mention capabilities beyond currently supported verification.
- Alias discovery/read-receipt tracking, private-command alias bootstrap or mention-only participant enrollment; participants use aliases already exposed by forwarded author messages.

## Implementation verification

- Frozen-vocabulary and exhaustive pair-uniqueness tests cover all 65,536 unsuffixed aliases, tier boundaries and the maximum ordinal. A full ordering checksum freezes the slot permutation; every pair is checked to repeat with the correct tier suffix.
- Store/config tests cover concurrent allocation, cancellation, rollback, restart, permanent exhaustion, deleted-set nonreuse and preservation across settings/binding changes.
- Router/adapter/private-relay tests cover author-only enrollment, canonical LID requirements, scoped lookup, current destination authorization, reassigned route snapshots, quote/poll mention privacy, all supported private network pairings and ambiguous-inbox rejection. Poll option projection preserves source hash-to-index mappings in the content-free `poll_source_option_hashes` table; original and forwarded votes contribute to the same canonical indexes after restart.
- Passed `make fmt`, `make test`, `make vet`, `make build`, `go test -race ./... -timeout 150s`, JavaScript syntax checks for `app-base.js` and `app.js`, and `git diff --check`. The built binary reports `development`; `VERSION` is unchanged.
- Live provider testing requires configured external accounts and was not performed. Manual scenarios remain documented in `docs/TESTING_GUIDE.md`. No container/runtime packaging changed, so container checks were not required.
- The subsequent fresh-database scrambled-order revision passes `make fmt`, `make test`, `make vet`, `git diff --check` and race tests for `internal/identity` and `internal/store`. The full ordering checksum and exhaustive tier checks lock the new mapping; ordering initialization now preserves original issued mappings and pins the allocation namespace.
