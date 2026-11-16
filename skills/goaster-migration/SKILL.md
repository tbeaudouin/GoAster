# GoAster central migration

## Scope and first response

This bundled skill supports only creating one **new Hetzner Cloud destination**
through the existing source-side `goaster_migration` workflow and its
GoAsterWeb counterpart. The destination must be newly created by that
workflow; a pre-existing Hetzner server or a destination at any other provider
is outside scope. This restriction concerns the destination; discovering the
source provider is not a prerequisite.

For a request to start a new migration, establish the requested new Hetzner
server type/size and optional locations. If the request is ambiguous, ask for
the requested type/size and any location preference; no location preference
is valid and means any eligible location. These questions do not apply to
status or stop requests for an existing monitor. Never offer an existing-server
versus new-server choice or ask for an existing destination's IP or SSH
credentials. If the user wants a pre-existing or non-Hetzner destination,
politely explain that this bundled skill cannot do it and that they must design
their own separate skill. Do not attempt a manual SSH fallback or adapt this
workflow.

Before any billable work, present indicative pricing when available and obtain
explicit approval in a private conversation for the exact type, location
policy, estimate/currency (or pricing unavailability), and one create. A public
estimate is informational, not a quote or price cap; provider charges are
uncapped. Do not start billable work without that approval.

Migration execution and scheduler resumption are enabled. Ordinary explicit approval in a private conversation is required before a billable operation; there is no separate approval record.

Enabled means enabled in this build; it does not establish which versions are
installed or running, or that live integration has been verified. Migration
requires a counterpart GoAsterWeb version implementing the machine API with
valid Redis configuration. Exactly-once creation and Redis durability are not
guaranteed or proven.

Use the source-side `goaster_migration` tool to inspect one proposed migration
against the existing GoAsterWeb service. The GoAster source implementation owns
the bounded HTTPS requests, local login-key and owner-secret generation,
30-minute availability monitor, and protected Telegram handoff. GoAsterWeb
owns the provider create sequence, callback secret, Redis handoff, destination
installation, host-key pinning, setup, and fresh OpenAI/Telegram authorization.

Use the migration tool's exact action names: `start` to begin the approved
monitor/create workflow, `status` to inspect that monitor, and `stop` to stop
it. The tool exposes no `estimate`, `quote`, or `availability` action. Use
this bundled skill and ordinary public sources for indicative pricing and
server-type/location discovery; those conversations are informational only.
Do not use the generic `cron` tool for migration availability, monitor
resumption, or provisioning; the migration flow must use
`goaster_migration` and its separate migration scheduler/store.

Do not add a migration daemon, provider-create script, Telegram command,
destination SSH step, or background-command workaround. There is no new
database or second attempt ledger. The existing scheduler file is only the non-secret monitor
schedule; it is not billing or provisioning history.

## Fixed service and v1 contract

The configured public origin is the fixed HTTPS origin
`https://goasterweb-thomas.fly.dev` with no trailing slash. Resolve these
paths against that origin:

```text
POST /migration/v1/machines/availability
POST /migration/v1/machines/create
```

Requests are bounded JSON with exact lower-case fields, no query string, no
browser `Origin` header, and `Content-Type: application/json`. The source and
diagnostic paths never log request bodies, public keys, owner digests, provider
tokens, owner links, or private keys.

Availability and the single create request use the same full immutable terms, plus a request-scoped `hetzner_token`. Terms are a fresh 64-character lower-case hexadecimal `operation_id`; a fresh 32-byte unpadded base64url `operation_capability`; the exact requested `server_type`; `locations` (empty means any eligible location, while a non-empty list is a strict allow-list); `image: ubuntu-24.04`; `architecture: arm` for a lowercase `cax` followed by one or more digits and `architecture: x86` for every other validated server type; `enable_ipv4: true`; `enable_ipv6: false`; `networks: []`; `billing_terms: provider-charges-uncapped-v1`; `login_public_key` (generated Ed25519 OpenSSH `ssh-ed25519` public key); and a 64-character lower-case hexadecimal SHA-256 `owner_secret_digest` for the local 32-byte owner-link secret. The Web architecture values are exactly `x86` and `arm`. The immutable terms are identical across both requests. There is no price cap, target price, quote field, or silent substitution.

An absent or empty location list means every eligible location for that exact
type. A non-empty list is never widened. GoAsterWeb returns only `version`,
`server_type`, and eligible `locations`; this endpoint does not return prices
and does not call the Hetzner pricing API.

The final authoritative availability check is internal to the approved
`start`/monitor workflow. It is not a model-facing discovery action. Its
source-side result repeats the exact location policy and requested locations
for presentation, while keeping those fields out of the Web wire contract.
It contains no price cap, approval, credential, or private material.

Creation sends the same immutable operation terms and the request-scoped token. It does not send the
private key or owner secret. The successful response contains the v1 state,
attempt, server label, server ID, public IP, owner URL without the owner
secret, and absolute expiry. The source appends the local owner secret to the
fragment only and never puts it in a query string.

## Indicative pricing, discovery, and ordinary private-conversation approval

Before asking for approval, discover the exact requested server type and
optional locations. Follow `hetzner-auth` and `goaster-env` for authenticated
read-only provider pricing. The credential variable is exactly
`HCLOUD_TOKEN`; the current migration lookup checks protected
`<real user home>/.goaster/env` first, then falls back to process
`HCLOUD_TOKEN`. If protected settings cannot be read, stop rather than
bypassing them. Keep the token in memory only: never expose it in model
context, logs, chat, command-line arguments, new secret files or copies, or
diagnostics. The existing approved protected env file remains a credential
source. The only permitted displayed header schematic is `Authorization: Bearer <token>` with
the literal placeholder, never a real credential.

The read-only provider calls use HTTPS at `https://api.hetzner.cloud`:

```text
GET https://api.hetzner.cloud/v1/pricing
GET https://api.hetzner.cloud/v1/server_types?name=<exact-type>
GET https://api.hetzner.cloud/v1/locations
```

URL-encode the exact type query value. There is no Web quote endpoint and no
pricing call is part of migration execution. Authenticated provider list
prices are distinct from anonymous public website prices; label the source
accurately. If authenticated pricing is unavailable, pricing is best-effort:
use public documentation as fallback and state its source, or say unavailable.
Authentication failure stops auth-dependent work, while informational
pricing may remain best-effort through public documentation. Pricing never
proves project identity; do not invent a project metadata API. Do not claim
the GoAsterWeb availability response contains a price.

Use the in-process HTTP recipe in `hetzner-auth`: load the exact token through
`goaster-env`, set the Bearer header in request memory, GET `/v1/pricing` only
at the fixed HTTPS origin, use a finite timeout and body bound, disable
redirects and verbose tracing, then parse and filter safe fields and discard
the raw body. Never shell-source the env file or pass the token in a curl
argument. See the official [Cloud API documentation](https://docs.hetzner.cloud/reference/cloud)
and [hcloud-go pricing schema](https://github.com/hetznercloud/hcloud-go/blob/main/hcloud/schema/pricing.go).
Match `pricing.server_types[].name` and requested exact locations. Compute
prices use `pricing.server_types[].prices[].location`,
`pricing.server_types[].prices[].price_hourly.gross`,
`pricing.server_types[].prices[].price_hourly.net`,
`pricing.server_types[].prices[].price_monthly.gross`, and
`pricing.server_types[].prices[].price_monthly.net`. Match
`pricing.primary_ips[].type` to `ipv4`; its price fields are
`pricing.primary_ips[].prices[].location`,
`pricing.primary_ips[].prices[].price_hourly.gross`,
`pricing.primary_ips[].prices[].price_hourly.net`,
`pricing.primary_ips[].prices[].price_monthly.gross`, and
`pricing.primary_ips[].prices[].price_monthly.net`. Price amounts are decimal
strings; currency, names and locations are ordinary strings, and server type
ID is int64. Preserve decimal precision, report `pricing.currency` without
assumptions, and distinguish gross (includes VAT) from net (excludes VAT).
Do not convert through binary floats. For an empty location list, summarize
matching per-location prices or a range without narrowing the policy or ranking
locations by price.

The current migration enables IPv4. Present any matching IPv4 estimate
separately from compute; do not describe base compute as all-in. Output only
allowlisted type, location, currency, hourly/monthly compute estimate,
optional hourly/monthly IPv4 estimate, and source timestamp. Never output the
authorization value, raw server listings, provider body, or error text. If
filtering cannot safely yield those fields, report pricing unavailable
without exposing the response. At runtime failure boundaries, emit a loud,
bounded ERROR diagnostic with safe operation and status context as required
by repository rules; never include raw provider bodies or errors. Estimates
are indicative only: do not rank types, apply a price cap, imply availability,
perform mutations, or invoke
migration for a quote.

Include the indicative gross hourly compute estimate and currency when
available, plus the separate IPv4 estimate when available; if pricing is
unavailable, say so plainly. Do not substitute a cheaper type or silently
choose a different location.

An omitted or empty location list means any compatible location. A non-empty
list is a strict allow-list and must not be widened. Public discovery does not
authorize or block the future request, and a public estimate may omit locations
that are not currently listed as available. The final authoritative
availability decision happens only inside the approved `start`/monitor
workflow.

Present the exact type, strict location preference, indicative currency and
per-location estimate when available, and the fact that one provider creation
sequence would occur. The model-facing `start` call accepts only the exact
type and location request; it does not accept an approval flag, quoted price,
currency, monitor ID, chat ID, or any secret.

Before any billable operation, obtain ordinary explicit user approval for the
exact server type, strict location preference, current estimate and currency
(or the fact that pricing is unavailable), and exactly one create in a private
conversation. Do not treat approval in a group conversation as sufficient.
Do not invent or require a special command, exact reply, button, code, callback,
or dedicated approval record. Ordinary conversational approval is a workflow
requirement only; it does not provide a trusted cryptographic binding between
the user, the terms, and the future provider request.

The runtime relies on that private-conversation workflow; it does not accept
or create a separate approval mechanism. Call `start` only after the owner has
approved the exact terms conversationally. The source preflights the protected
paired-Telegram delivery path before the create request.

## Monitor and one create

After the required conversational approval, the source checks availability
immediately. If no eligible
location is returned, it leaves one non-secret scheduler job enabled at
exactly 30-minute intervals. The token is loaded only when a check runs; it is
never placed in the scheduler message. A ready result disables that exact job
and verifies the disabled state before the create request. Availability errors,
monitor state errors, and disablement uncertainty stop the workflow for manual
review.

The create endpoint is called once. GoAsterWeb uses the exact server type,
strict location allow-list, Ubuntu 24.04, the generated public key, and its
existing callback user-data. The source does not retry the HTTP request,
inspect provider pricing during creation, delete infrastructure, or infer
that an absent server makes replay safe. A timeout, lost response, malformed
response, or uncertain provider/Web state disables the monitor and requires
manual inspection of the token-scoped Hetzner project and the GoAsterWeb
owner/status page before any new owner-approved attempt.

## Protected owner handoff

The source generates the Ed25519 login key pair and the 32-byte unpadded
base64url owner secret locally. At the owner’s explicit request, the private
key is kept in a fresh child of the resolved GoAster temporary root:

```text
<real user home>/.goaster/tmp/migration-*/id_ed25519
```

The private-key path is therefore
`<real user home>/.goaster/tmp/migration-*/id_ed25519`.

The temporary root and task directory are real private directories; the task
directory is mode `0700` and key/handoff files are mode `0600`, created
exclusively and synced before use. This migration flow deliberately keeps the
key and handoff in that private task directory until the owner removes them;
there is no automatic cleanup implementation. Do not place them in the
repository, a shared system temporary directory, a scheduler message, logs,
model context, or ordinary chat.

After a successful create, the source builds a protected handoff containing
the setup link with the owner secret, server identity, and private login key.
It sends that document directly as a Telegram document to the already paired
positive user ID loaded from protected local Telegram state. No model-selected
chat ID or group destination is accepted. The private key and setup link must
never appear in model output; the tool returns only safe status and provider
identity fields.

If direct delivery fails, leave the protected handoff for manual inspection,
keep the monitor stopped, and do not resend automatically. The owner removes
the temporary migration directory after delivery or abandonment.

## Scope boundary

The owner uses the normal GoAsterWeb migration page for exact provider
verification, private-key upload/match, callback host-key pinning, destination
setup, and new authorization. This source workflow does not deploy, SSH to,
install, configure, start, stop, delete, or cut over either GoAster host. It
does not modify GoAsterWeb or Redis and does not create a second provisioning
record.
