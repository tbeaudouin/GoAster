# GoAster central migration

Migration execution and scheduler resumption are enabled. Ordinary explicit approval in a private conversation is required before a billable operation; there is no separate approval record.

Enabled means enabled in this build; it does not establish which versions are
installed or running, or that live integration has been verified. Migration
requires a counterpart GoAsterWeb version implementing the machine API with
valid Redis configuration. Exactly-once creation and Redis durability are not
guaranteed or proven.

GoAster implements the source side of the versioned central migration machine
API. It uses the existing scheduler and paired Telegram delivery path; it does
not add a migration database, a second attempt ledger, a provisioning daemon,
or a provider-create helper.

The source workflow is deliberately small:

1. The owner asks in a private conversation for an exact Hetzner server type
   and optional strict location preference.
2. The bundled migration skill uses a current, read-only public price estimate
   and ordinary public type/location discovery to present indicative terms.
3. Before any billable operation, the owner gives ordinary explicit approval
   for the exact terms in that private conversation. This is a conversational
   workflow requirement, not a trusted cryptographic binding.
4. The approved `goaster_migration` `start` action checks authoritative central
   availability immediately and schedules the same check every 30 minutes while
   no eligible location exists.
5. GoAster generates the login key and owner-link secret before its first check.
   When availability is returned, it disables and verifies the exact monitor and sends
   exactly one creation request.
6. GoAster stores the private key and protected handoff in a private
   `~/.goaster/tmp/migration-*` task directory and sends the setup link plus
   private key directly to the locally paired Telegram user. The model sees
   only safe status and provider identity fields.

Migration scheduler state resides in a private directory outside the model-writable artefacts workspace.

The task directory and handoff are intentionally retained for the owner’s
manual cleanup. There is no automatic cleanup implementation.

## Local-only resource-term binding

The source now has a local `ResourceTerms` v1 representation for the complete
non-price terms that can be carried safely without changing the current Web
contract: server type ID/name, image ID/name, architecture, network IDs and
public-network settings, explicit location policy, local operation identity,
and an acknowledgement of exactly one uncapped billable create. A canonical
JSON form sorts only the set-like network and location lists; an unkeyed
SHA-256 digest covers every included non-price term. Price is intentionally
not represented or bound here, and the digest is not authentication,
authorization, or owner approval.

This local digest does not establish who produced the terms or that an owner
approved them. The separate local utility is not used as the machine API binding; live GoAsterWeb behavior remains unverified.

This representation is intentionally not added to the current v1 Web request
payloads, scheduler messages, or model-facing tool schema. It remains a local
terms utility and does not start a monitor or make a Web/provider request by
itself.

## Central machine API

The fixed public origin is `https://goasterweb-thomas.fly.dev` with no trailing
slash. The source uses only:

```text
POST https://goasterweb-thomas.fly.dev/migration/v1/machines/availability
POST https://goasterweb-thomas.fly.dev/migration/v1/machines/create
```

Every request is bounded `application/json`, uses exact lower-case field names,
has no query string, and has no browser `Origin` header. GoAster never logs a
request body, public key, owner digest, provider token, owner URL, or private
key. The `hetzner_token` is read locally and placed only in the individual
request.

Availability sends the full immutable operation terms and request-scoped token defined in the [machine API contract](central-machine-api-contract.md). These include the operation identity and capability, fixed machine and billing terms, generated login public key, and `owner_secret_digest`, the lower-case SHA-256 hex digest of the owner-link secret. The Web architecture values are exactly `x86` and `arm`: GoAster derives `arm` for a lowercase `cax` followed by one or more digits and keeps `x86` for every other validated server type.

`locations` may be absent or empty. In that case GoAsterWeb considers all
eligible locations for the exact `server_type`. Otherwise it is a strict
allow-list and is never widened by the source. The v1 response contains only
`version`, `server_type`, and eligible `locations`; it never contains a price.
GoAsterWeb does not call Hetzner pricing for this endpoint.

The final availability check is internal to the approved `start`/monitor
workflow. Its source-side result adds `location_policy` (`any_eligible` when
the request omits locations, otherwise `allowlist`) and the exact
`requested_locations` when present. These fields are presentation metadata;
they are not accepted from or sent to GoAsterWeb, and the result contains no
price, cap, approval, or credential fields. The model-facing tool exposes only
`start`, `status`, and `stop`.

Creation sends those same immutable terms and token.

The private key and owner secret never cross this request. GoAsterWeb owns the
exact provider create sequence, Ubuntu 24.04 image, SSH registration, callback
secret, and Redis handoff. Its successful response has `version`, `state`,
`attempt`, `server_label`, `server_id`, `ip`, an owner URL containing only the
attempt fragment, and `expires`. GoAster appends `&owner=<owner-secret>` to
that fragment only for the protected Telegram handoff.

## Public discovery, monitor, and uncertainty

The bundled migration skill handles indicative public Hetzner pricing,
ordinary server-type discovery, ordinary location discovery, and the
private-conversation guidance. Public pricing is best-effort and informational:
include current public gross estimates and currency when available, or state
that pricing is unavailable. An omitted or empty location list means any
compatible location; a non-empty list is strict. Public discovery may omit a
location and never widens a non-empty allow-list. It does not authorize or
block the request and is not sent to or returned by GoAsterWeb. There is no
model-facing `estimate`, `quote`, or `availability` action.

Present the exact server type, strict locations, indicative currency,
per-location estimate when available, and one-create statement to the owner
in a private conversation. Before any billable operation, obtain ordinary
explicit approval for those terms (or the fact that pricing is unavailable)
and exactly one create. Do not treat a group conversation as sufficient.

Do not invent or require a special command, exact reply, button, code,
callback, or dedicated approval record. Ordinary conversational approval
expresses user intent but does not provide a trusted cryptographic binding
between the user, terms, and provider request. The model-facing operation
does not accept an approval flag, quoted price, currency, monitor ID, chat ID,
or private material. The runtime relies on this private-conversation workflow
and does not add a separate approval mechanism.

The first availability request runs immediately. If its `locations` result is
empty, the existing scheduler stores one non-secret 30-minute monitor. If a
location becomes available, GoAster claims and verifies that exact job is
disabled before calling creation. No HTTP retry, provider delete, or automatic
rollback exists. A definitively not-created terminal result can be followed by
an ordinary explicit `start` after its configuration cause is fixed; that start
gets a fresh operation and monitor record while retaining the old history. A
timeout, lost response, missing details, malformed response, or
Redis/provider binding uncertainty is a manual-reconciliation condition. After
that review, an uncertain result may be followed only by one explicit `start`
with `retry_attempt` equal to the exact prior monitor ID and the owner’s
duplicate-server confirmation. The old attempt is never re-enabled, and a
confirmed-created result is never retryable.

## Protected handoff and ownership boundary

The resolved GoAster temporary root is:

```text
<real user home>/.goaster/tmp
```

Each migration uses a fresh mode-0700 task child. The Ed25519 private key and
handoff are exclusive mode-0600 files and are synced before use. The task
directory remains until the owner manually removes it after delivery or
abandonment. It is never copied into the repository, a scheduler message,
model context, log, or shared temporary directory.

The handoff is sent as a document to the positive user ID already stored in
protected Telegram pairing state. No model-selected chat or group is used.
The monitor persists the paired user at start and requires the same pairing at execution, before creation, and before delivery. A changed or unavailable pairing requires manual follow-up. Legacy jobs without a recipient require manual review. If the Telegram delivery itself fails, the monitor remains stopped
and the protected handoff is left for manual inspection; it is not resent
automatically.

The owner then uses the normal GoAsterWeb migration page for exact provider
verification, private-key upload/match, callback host-key pinning, destination
setup, and new OpenAI/Telegram authorization. This repository does not deploy,
SSH to, install, configure, start, stop, delete, or cut over either host and
does not modify GoAsterWeb or Redis.

## Offline validation

The migration tests use only synthetic HTTP servers and local temporary
directories. They do not contact GoAsterWeb, Hetzner, Telegram, OpenAI, or
Redis.

```sh
go test ./internal/migration ./internal/chat ./internal/telegram ./cmd/goaster-agent
go test ./...
go vet ./...
sh packaging/test-skill-assets.sh
git diff --check
```
