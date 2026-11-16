# Central migration machine API

This is a `v1` contract between the source-side migration workflow and
GoAsterWeb. Source-side approval is conversational; this offline contract
does not prove an approved real-world owner identity. A capability
authenticates one source operation and binds its immutable terms.

Central creation is available whenever public `web` has the existing valid
`UPSTASH_REDIS_REST_URL` and `UPSTASH_REDIS_REST_TOKEN` configuration. Missing
or malformed configuration leaves the machine API unavailable. Startup only
validates configuration shape; it does not prove Redis connectivity or
provider durability.

## Version and transport

The API is:

- `POST /migration/v1/machines/availability`
- `POST /migration/v1/machines/create`
- `POST /migration/v1/machines/status`

Requests must use the configured public HTTPS origin, have no query string or
browser `Origin` header, and use `Content-Type: application/json`. Bodies are
bounded and use exact lower-case JSON field names; unknown and repeated
top-level fields are rejected. Request bodies are not logged.

The request-scoped `hetzner_token` is used only in memory for the read-only
availability check or the one creation sequence. It is never put in Redis, a
response, a URL, a log, or callback user-data. The SSH private key is never
accepted by these routes.

## Immutable requested terms

The source generates a fresh 64-hex-character `operation_id`, a fresh 32-byte
unpadded-base64url `operation_capability`, an Ed25519 login key pair, and a
32-byte owner-link secret after its conversational approval step. It sends the
public login key and only the lower-case SHA-256 digest of the owner-link
secret. The following requested terms are bound to the operation:

```json
{
  "operation_id": "<64 lowercase hex characters>",
  "operation_capability": "<32-byte unpadded base64url value>",
  "server_type": "cx22",
  "locations": ["fsn1", "nbg1"],
  "image": "ubuntu-24.04",
  "architecture": "x86",
  "enable_ipv4": true,
  "enable_ipv6": false,
  "networks": [],
  "billing_terms": "provider-charges-uncapped-v1",
  "login_public_key": "ssh-ed25519 AAAA...",
  "owner_secret_digest": "<64 lowercase hex characters>"
}
```

The current destination flow accepts only Ubuntu 24.04, public IPv4, disabled
IPv6, and no additional private networks. The Web architecture values are
exactly `x86` and `arm`: the source sends `arm` for a lowercase `cax` followed
by one or more digits, and `x86` for every other validated server type.
Unsupported terms are rejected; no image, architecture, network, server type, or
location is silently substituted. There is no `price_cap`, target price, quote,
or price-based eligibility field. `billing_terms` explicitly binds that the
provider may charge without a price cap.

An omitted or empty `locations` list means no location preference. A non-empty
list is a strict allow-list. Location order is canonicalized for the binding;
the provider selection is deterministic by location name and then ID, with no
price ranking.

## Availability

Availability uses the full immutable terms above plus `hetzner_token`. It reads
the exact server type's current location availability and deprecation fields,
checks the exact architecture and image, and returns every eligible location:

```json
{
  "version": "v1",
  "server_type": "cx22",
  "locations": ["fsn1", "nbg1"]
}
```

The read is only an indicator. A final provider read is performed immediately
before the durable create claim, because availability can change before
allocation. The availability call also stores a non-secret, terms-bound
`checked` record; a create request without that record is rejected. That record
is initialized only before handoff registration. If the create ledger is
missing after registration, the atomic prepare checks the unchanged legacy
admission reservation for the operation and returns a conflict instead of
recreating it. If Redis silently loses or rolls back both witnesses, the
application cannot distinguish a fresh operation from lost state; that
accepted residual risk means this API does not promise exact-once provider
creation.

## Create claim and mutation sequence

The separate Redis key
`goaster:migration:create:{goaster:migration:v1}:<operation_id>` is non-expiring and stores only
digests, requested terms digest, provider resource IDs, selected location, safe
server result, and state. It never stores the provider token, private key,
owner-link secret, or callback secret. One atomic Lua transition moves it through:

`checked → key_dispatching → key_registered → server_dispatching → server_created → created`

The irreversible intent is persisted before SSH-key registration and again
before server creation. The selected server type/image/location IDs are stored
in the claim and passed explicitly to the provider. While the intact ledger is
present, a repeated request with the same capability and terms returns the
stored `created` result without provider calls. Altered terms or
public-key/owner-digest bindings conflict. Any in-flight, missing, malformed,
or uncertain current state fails closed and does not retry either provider
mutation. Silent Redis loss or rollback can erase or resurrect a valid ledger
and permit duplicate provider creation; that residual risk is accepted rather
than hidden behind an exact-once claim.

The create request contains the immutable terms above and `hetzner_token`:

```json
{
  "operation_id": "<same operation id>",
  "operation_capability": "<same capability>",
  "server_type": "cx22",
  "locations": ["fsn1", "nbg1"],
  "image": "ubuntu-24.04",
  "architecture": "x86",
  "enable_ipv4": true,
  "enable_ipv6": false,
  "networks": [],
  "billing_terms": "provider-charges-uncapped-v1",
  "login_public_key": "ssh-ed25519 AAAA...",
  "owner_secret_digest": "<same digest>",
  "hetzner_token": "<request-scoped token>"
}
```

The successful response contains no credential:

```json
{
  "version": "v1",
  "state": "created",
  "attempt": "<operation id>",
  "server_label": "goaster-migration-<operation prefix>",
  "server_id": 123456,
  "ip": "<public-ip>",
  "owner_url": "https://web.example/migration#attempt=<operation id>",
  "expires": 1800000000
}
```

The source appends `&owner=<owner-link-secret>` locally. GoAsterWeb generates
the callback secret, uses it only in callback-only cloud-init, and retains only
its digest in the create ledger. The existing owner flow independently checks
the exact provider server, uploaded private-key/public-key match, pinned host
key, and destination setup gates.

## Status and uncertainty

`status` accepts only `operation_id` and `operation_capability`. It is
read-only, returns safe ledger state and known IDs, and never triggers a
provider mutation. It never returns a capability, owner secret, token, private
key, callback secret, or owner fragment secret.

A provider timeout, lost response, missing server details, ledger write failure,
callback/bind failure, or any unexpected state returns a manual-reconciliation
error for that operation. The same `operation_id` and capability must never be
replayed: inspect the token-scoped Hetzner project and the owner status page
before doing anything further on that operation, and use `status` only for
read-only reconciliation. This same-operation check is distinct from a fresh
source operation. After one interactive private owner confirmation using the
duplicate/extra-charge warning, GoAster may issue a separately confirmed fresh
source operation with a new operation ID and capability, carrying the exact
retained prior reference through the source-only `retry_attempt`; an account or
status-page check is optional for that fresh operation and is not a gate.
Unavailable or corrupt retained history, an in-flight execution, an
unconfirmed stop, or unresolved authentication remains a blocker. GoAsterWeb
never automatically retries a create, adopts an untracked server, deletes a
key/server, or creates a replacement.

The ledger's atomic EVAL behavior is covered by local Redis fixtures. Offline
tests cannot prove managed Redis durability, no-eviction policy, backup restore
fencing, provider API behavior, proxy request-log redaction, conversational
source approval, or live billing semantics. The accepted Redis durability
limitation is a residual operational risk, not an application guarantee or an
activation assertion.
