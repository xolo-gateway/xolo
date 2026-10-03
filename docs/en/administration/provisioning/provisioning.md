# Provisioning API

The dedicated listener exposes the App Covenant `0.1.0-draft.1` manifest and
five complete PUT operations, unit reads, lists and the event feed. It shares transactional stores with the public
server but has its own TLS configuration. The public OpenAI-compatible `/v1/`
proxy is unaffected.

The API also supports optional durable webhooks, identity management, session
revocation, ownership and adoption. The manifest identifies the targeted
contract; it is not a claim of complete conformance.

## Configuration and authority

| Variable | Default | Meaning |
| --- | --- | --- |
| `XOLO_PROVISIONNING_API_ENABLED` | `false` | Enable the separate listener |
| `XOLO_PROVISIONNING_API_ADDRESS` | `:3003` | Listen address |
| `XOLO_PROVISIONNING_API_TLS_CERT_FILE` | — | Server certificate PEM |
| `XOLO_PROVISIONNING_API_TLS_KEY_FILE` | — | Server private key PEM |
| `XOLO_PROVISIONNING_API_TLS_CLIENT_CA_FILE` | — | Trusted client CA bundle PEM |
| `XOLO_PROVISIONNING_API_AUTHORIZED_URIS` | — | Required comma-separated absolute URI allowlist |
| `XOLO_PROVISIONNING_API_RATE_LIMIT` | `10` | Requests per second per authorized URI, positive |
| `XOLO_PROVISIONNING_API_RATE_BURST` | `20` | Burst per URI, positive integer |
| `XOLO_PROVISIONNING_API_SHUTDOWN_TIMEOUT` | `10s` | Graceful shutdown timeout |

TLS 1.3 is required. Client certificates must chain to the configured CA and
contain exactly one URI SAN matching the allowlist exactly. CN, DNS SAN,
bearer tokens, cookies and forwarded certificate headers grant no authority.
An authorized URI controls the whole instance, including suspended resources.
Certificate failures fail the TLS handshake; a request-time rejection returns
`403 client_certificate_rejected`.

Rate budgets are local to each server replica and reset on restart. A rejection
returns `429 rate_limited`, a positive integer `Retry-After` in seconds, and
performs no mutation. Use retry backoff with jitter.

`X-Request-ID` accepts exactly one 32-character lowercase hexadecimal value.
Otherwise the server generates one, discarding the invalid input. Every HTTP
response echoes the selected ID; access logs and mutation audits use it.
It is a correlation identifier, not an idempotency key.

## Common routes

`GET /v1/manifest` returns only `name`, `version` (the Xolo build release) and
`contract_version` (`0.1.0-draft.1`). Read it before writes and after upgrades.

| PUT path | Mutable fields |
| --- | --- |
| `/v1/tenants/{tenantID}` | `slug`, `name`, `status` |
| `/v1/tenants/{tenantID}/domains/{hostname}` | `status` |
| `/v1/tenants/{tenantID}/organizations/{organizationID}` | `slug`, `name`, `status` |
| `/v1/tenants/{tenantID}/members/{memberID}` | `email`, optional `display_name`, `tenant_role`, `status` |
| `/v1/tenants/{tenantID}/organizations/{organizationID}/members/{memberID}` | `role`, `status` |

UUID keys use canonical lowercase syntax. Tenant roles are `owner`/`member`;
membership roles are `owner`/`admin`/`member`. Status is `active`/`suspended`.
Tenant creation remains disabled in single-tenant mode; existing tenants can
still be updated. A tenant account PUT creates no authenticated link, invitation or
membership. Optional declared identity follows the replacement rules below.
Existing platform privileges are preserved,
and provisioning cannot assign configured default-admin emails to other accounts.

PUT requires `application/json` (valid media-type parameters are accepted), one
object, declared fields and no nulls. Common scalar fields are strings; the
optional `identity` extension is an object. All fields except `display_name`
and `identity` are required. Omitting `display_name` clears it; an empty result is omitted.
The entire body, including trailing whitespace, is limited to 1,048,576 bytes.
Invalid types/JSON, unknown fields, invalid UTF-8 and excess bytes return
`400 invalid_json`; absent fields and invalid values return
`400 invalid_representation`. Unsupported media types return `415`.

Normalization follows Go Unicode trimming and lowercase mapping: slug, email
and hostname are trimmed and lowercased; names are trimmed. Roles and statuses
are case-sensitive and trimmed, **except member status which is not trimmed**.
Limits after normalization are 63 bytes for slug, 200 for name/display name,
320 for email and 253 for hostname. Email requires only `@` and no ASCII control
characters. Names also reject ASCII controls. Hostnames are ASCII DNS labels,
without IP literals, ports, trailing dots or the shared application hostname.
There is no NFC, IDNA or provider-specific email rewriting.

All successful common PUTs return `200` and only the normalized representation,
including creation. Identical normalized writes preserve rows, timestamps,
audit and publication records. Changes commit atomically. Tenant/organization
owners are protected against concurrent demotion or suspension:
`409 last_owner`. Parent suspension is allowed and does not rewrite children.
Custom membership roles survive a common role update.

Errors use `{"error":{"code":"...","message":"..."}}`. Missing parents return
`404 parent_not_found`; UUID reassignment and uniqueness conflicts return a
non-disclosing `409 conflict`. Invalid/reserved domains return
`400 invalid_hostname`. Unsupported routes and methods return `404 not_found`.
Technical failures return `500 internal_error` without database diagnostics.

## Reads, conditions and synchronization

Every PUT path also accepts GET, returning the same representation and an
`ETag: W/"u-<unix-microseconds>"` header. The timestamp belongs to the common
representation: login, identity attachment and Xolo-only metadata do not change
it. Timestamp collisions within one microsecond remain possible; ETags are
opaque validators, not revision counters.

PUT accepts `If-Match: *` (existing resource) or a comma-separated list of entity
tags. Comparison ignores `W/`, as explicitly required by this contract.
Validation, comparison and mutation share the transaction lock. A stale
condition fails with `412 precondition_failed`, even for an identical body;
malformed syntax returns `400 invalid_precondition`.

Remove the final key from a unit path to list its collection, including
suspended resources. For example, list memberships at
`/v1/tenants/{tenantID}/organizations/{organizationID}/members`.
Use `limit=1..1000` (default 100) and, after the first page, `cursor`.
The envelope is `{"items":[{"key":{},"representation":{},"etag":"..."}],
"next_cursor":null}`. Continue until `next_cursor` is null, repeating the same
limit. Ordering uses immutable canonical keys with bytewise ASCII comparison.
There is no total count and no cross-page snapshot.

List cursors are HMAC-SHA256 authenticated and bound to the instance,
collection, parents and effective page size. Their lifetime is **24 hours from
the first page**; continuation does not renew it. Tampered or wrong-scope
cursors return `400 invalid_cursor`; expired recognized cursors return
`410 cursor_expired`. Cursor tokens are opaque, not encrypted.

Capture a feed position with `GET /v1/events/cursor`, which returns
`{"cursor":"..."}`, even on an empty feed. Then poll
`GET /v1/events?cursor=...&limit=100`. Responses contain `items`, a nonempty
`next_cursor`, and `has_more`. Event cursors are instance/feed-bound and do not
bind the page size. `has_more=false` means caught up to that response's safe
horizon; polling must continue for subsequent writes.

Events use the closed CloudEvents 1.0 profile: persistent UUID `id`, persistent
instance `source` (`urn:uuid:...`), decimal-string `sequence`, timestamp,
request ID and `data` containing only `resource_type`, `key`, `etag`.
No names, emails, actor identities or internal attributes appear in this feed.
Audit retains the actor and before/after data separately. One effective PUT
produces one event; a no-op or rollback produces none. Local changes preserve
separate status and role facts. Xolo-only changes do not produce common events.

Capture C0 **before** listing all five families. Replay from C0 by GETting each
referenced key; discover children when encountering a new tenant or organization.
Serialize reads and application per key, durably apply a page before saving its
checkpoint, and deduplicate by `(source, id)`. A replay GET can be newer than
its event. A failed known-resource read, including an unexpected 404, must
retain the checkpoint. Unknown independent extension events may be reported
and passed. On 410, rebuild all collections into a new generation from a new
C0 and replace the previous generation only after replay catches up.

### Storage, horizon and retention

Startup migration `202610020002` creates `common_records` and `common_feeds`,
backfills existing resources, and removes the former internal-snapshot outbox
rows. Internal mutation audits remain intact. Existing records need no synthetic
creation events. Source identity and cursor signing material persist in the
database and survive restarts and URL changes; include them in backups.

All identity writers acquire the `publication_clocks` row before reading or
mutating scope data. PostgreSQL uses a row lock; SQLite uses its writer lock.
The transaction retains it until commit/rollback. A second writer cannot
allocate or commit a higher position while the first is unresolved. Feed reads,
cursor capture and purge use the same lock. The committed counter is therefore
a safe horizon, including settled gaps from internal audit facts. This favors
correctness over concurrent identity-write throughput; no background publisher
or `MAX(sequence)` heuristic is used.

Retention is **unlimited by default**, with no automatic purge or environment
setting for it. The store maintenance method
`PurgeCommonEvents(ctx, before)` removes only a settled prefix older than the
UTC cutoff and persists the last removed event position, even after complete
purge. It cannot remove a recent earlier sequence merely because a later clock
value went backwards. A cursor before the retained boundary returns 410;
capture and polling never renew lost history. Choose any future scheduled
retention window long enough for a full reconstruction.

The shared store validates immutable parent links and publication references
inside each transaction, including CLI calls. Publication insertion is private
to that path; callers cannot supply public event payloads. Xolo uses one trusted
application database credential, without PostgreSQL tenant RLS or a separate
restricted publication role. SQLite has no SQL role boundary. Code holding the
raw GORM handle or direct database write privileges remains trusted and can
bypass application checks; provisioning clients receive neither. No SQL-level
protection against a compromised application credential is claimed.

Physical deletion through existing Xolo extensions is outside this draft's
no-deletion synchronization profile. Such a deletion must not be interpreted
as a common tombstone; lifecycle events and recovery are defined by the optional
Xolo lifecycle extension described below.

## Persistent host routing

Public routing resolves an active domain record to an active tenant. The shared
hostname from `XOLO_HTTP_BASE_URL` is reserved and continues to serve the default
tenant in single-tenant mode. That binding survives a tenant slug rename.
Domain and tenant status are read from the database, without a stale local cache.
Base URLs and OIDC callbacks use the validated domain, retaining the configured
scheme, port and base path. The identity provider must allow these callback URLs.

`XOLO_MULTITENANCY_HOST_PATTERN` is optional and retained only for automatic
upgrade: at the first startup, existing tenant hosts from the old pattern are
saved as explicit domains. This happens once, with no creation events for
historical domains. Later tenant creation, slug changes or pattern edits never
create routes implicitly. Provision domains with PUT. Existing explicit domain
records and suspensions are preserved; collisions stop startup.

## Xolo extensions

Extensions live under `/v1/xolo` and retain Xolo-specific representations:

- `GET /healthz` and `GET /permissions`.
- `GET`/`PATCH /tenants/{tenantID}` for Xolo tenant metadata.
- `GET`/`PATCH /tenants/{tenantID}/organizations/{orgID}` for settings including
  description, currency and quota sharing.
- Role CRUD under `/tenants/{tenantID}/organizations/{orgID}/roles`.
- `PUT /tenants/{tenantID}/organizations/{orgID}/members/{membershipID}/roles`
  for Xolo role assignments using the internal membership identifier.
- `GET`/`PUT /tenants/{tenantID}/users` for provider/subject identity lookup and
  provisioning. This extension is separate from the common member declaration.

These extension payloads may include IDs, timestamps and Xolo settings. Their
creation/deletion responses retain their existing `201`/`204` semantics.
They do not enlarge the minimal manifest or the common contract.

## Upgrade

Database conversion is automatic at startup. Back up the database, preserve
`XOLO_SECRET_KEY`, stop older replicas, then start the new release. Existing
keys and encrypted secrets remain valid. Sessions with old tenant IDs require
sign-in again; external clients holding old IDs must refresh them.

For provisioning clients, this release intentionally replaces the old routes:
there are no aliases and no `/v2`. Change clients to complete PUT payloads and
client-generated UUIDs, and move Xolo-specific calls to `/v1/xolo`. Reissue any
client certificate without exactly one authorized URI SAN and configure
`XOLO_PROVISIONNING_API_AUTHORIZED_URIS`. For example, the certificate extension
`subjectAltName=URI:urn:example:console` matches that same configured URI.

Tests use ephemeral CAs and real TLS handshakes. Database tests exercise SQLite
and PostgreSQL. App Covenant tooling remains external; no conformance runner
or permanent CI workflow is installed in Xolo.

## Durable webhooks (Xolo extension)

Webhooks are optional hints; consumers must still poll `/v1/events` on startup,
reconnection and periodically. They never advance the consumer's feed checkpoint.
Delivery is at least once. A receiver must authenticate before JSON decoding,
accept timestamps within 300 seconds in either direction, deduplicate
`(source, id)` durably and acknowledge durable acceptance with 2xx.

Enable `XOLO_WEBHOOKS_ENABLED=true` to run the worker independently of the
provisioning listener. Configuration is optional for existing installations;
the database migration `202610020003` runs automatically either way. No manual
migration command is needed. Manage subscriptions through the mTLS listener
when both features are enabled. The minimal `/v1/manifest` is unchanged;
`GET /v1/xolo/extensions` separately announces the webhook extension.

| Variable | Default | Meaning |
| --- | --- | --- |
| `XOLO_WEBHOOKS_ENABLED` | `false` | Run materialization, delivery and cleanup |
| `XOLO_WEBHOOKS_ALLOWED_ORIGINS` | required when enabled | Comma-separated exact HTTPS origins, including nondefault ports |
| `XOLO_WEBHOOKS_ALLOW_PRIVATE_NETWORKS` | `false` | Permit private and loopback addresses for allowlisted origins |
| `XOLO_WEBHOOKS_TLS_CA_FILE` | empty | Additional CA PEM; system roots remain trusted |
| `XOLO_WEBHOOKS_WORKERS` | `2` | Concurrent deliveries per process, 1–16 |
| `XOLO_WEBHOOKS_POLL_INTERVAL` | `1s` | Poll interval, 100 ms–1 minute |
| `XOLO_WEBHOOKS_QUEUE_CAPACITY` | `10000` | Maximum retained delivery rows, 1–1,000,000 |

Origins have no path, query, fragment or credentials. Destinations may add a
path, but no query, fragment or credentials. There are no wildcard origins.
Every DNS answer is checked at connect time and the checked IP is dialed
directly. Environment HTTP proxies are ignored. Link-local, multicast,
unspecified, shared and special-use networks remain denied, including cloud
metadata addresses, even with private access enabled. TLS certificates are
always verified; the private-network switch does not disable TLS verification.
Outbound firewall restrictions should also apply to the Xolo process.

Subscriptions have client-generated UUID keys under a tenant. Their ownership
is the effective `subscription` startup policy described below.
Tenant suspension does not disable control-plane notifications. The tenant
must exist before credentials are accessed; an ID cannot move between tenants.
At most 100 subscriptions exist per instance.

| Method and route | Behavior |
| --- | --- |
| `GET /v1/xolo/tenants/{tenantID}/webhooks` | List subscriptions without secrets |
| `GET /v1/xolo/tenants/{tenantID}/webhooks/{id}` | Settings, position, state and secret count |
| `PUT /v1/xolo/tenants/{tenantID}/webhooks/{id}` | Create or replace settings, response 200 |
| `DELETE /v1/xolo/tenants/{tenantID}/webhooks/{id}` | Delete subscription, credentials and all queued/retained deliveries, response 204 |
| `GET /v1/xolo/tenants/{tenantID}/webhooks/{id}/deliveries` | Latest 100 delivery diagnostics, without payloads or credentials |
| `POST /v1/xolo/tenants/{tenantID}/webhooks/{id}/reset` | Acknowledge loss, discard queue and restart from current safe horizon |
| `GET /v1/xolo/webhooks/status` | Instance queue counts, lag and history-loss status |

PUT requires `destination`, `events` and boolean `enabled`; `events` is either
`["*"]` or a nonempty list of exact common event types. `secrets` is required
at creation and contains one or two distinct base64 secrets, optionally
prefixed `whsec_`, each decoding to 32–64 bytes. Generate secrets outside Xolo.
Omitting `secrets` on update preserves the current keys. Keys are write-only,
AES-GCM encrypted using `XOLO_SECRET_KEY`, and bound inside the encrypted value
to the tenant and subscription IDs. Back up that key with the database.

For rotation, PUT `[old, new]`, configure the receiver to trust either, then
PUT `[new]` after receiver cutover. The response exposes only `secret_count`.
Every attempt loads current subscription settings: rotation and destination
changes apply to pending deliveries as well. Event-filter changes apply only
to events not yet materialized; queued events retain their original selection. `enabled=false` pauses new
materialization and claims without advancing the position or discarding work.
An already reserved request may still complete after disable, reset or delete;
its HTTP call is bounded and a stale result cannot overwrite a new reservation.

New subscriptions start at the current safe horizon, not the start of history.
Reconstruct state before relying on notifications. Materialization reads the
safe feed under its allocation lock, then atomically inserts delivery rows and
advances the subscription position. The unique subscription/event pair prevents
duplicate queue entries. Each materialization transaction reads at most 100
publications across all subscriptions, prioritizing the oldest positions to
bound lock duration and avoid starvation. Reservations last 30 seconds and use fresh fencing
tokens. HTTP runs outside all database transactions; a crash after receiver
acceptance but before result recording may repeat the same event and ID.

The exact stored CloudEvent bytes are sent as HTTPS POST with
`Content-Type: application/cloudevents+json`. `webhook-id` is the event UUID;
`webhook-timestamp` is Unix seconds for this attempt. HMAC-SHA256 covers
`id.timestamp.body` using decoded secret bytes. `webhook-signature` contains
`v1,<base64>` for each active key, separated by spaces. Retries preserve body
and ID and refresh timestamp/signatures. No actor, email or extra attributes
are added to the common event.

The client allows 3 seconds for DNS/connect, 5 seconds for TLS and response
headers, and 10 seconds total including response reading. It refuses redirects,
limits response headers to 16 KiB and body to 64 KiB, and follows context
cancellation. Only a completely read, bounded 2xx response succeeds. Diagnostics
use fixed codes such as `transport_error`, `redirect`, `http_status`,
`response_too_large` and `credentials_unavailable`; response bodies, signatures,
secrets and detailed transport errors are never included.

Failures retry after 5, 10, 20, 40… seconds, capped at one hour, with at most
12 reservations or 24 hours from materialization. Expired reservations count
as attempts. Exhausted jobs become `failed`; inspect their diagnostics and
reconcile using the feed. There is no exactly-once or automatic terminal replay
claim. Successful and failed rows, including their independent payload copies,
are retained for seven days after completion and cleaned while the worker runs.
A pending payload survives event-feed purge. Feed retention remains unlimited
by default; setting a separate feed purge policy is an operator decision.

Queue capacity counts pending, leased **and retained terminal** rows. When full,
materialization pauses with `backpressure`, preserving its last position;
resource writes continue. Budget for capacity times payload/row/index overhead,
plus the independently retained feed and audit. The default 10,000 rows normally
represents tens of MiB rather than a byte-level disk quota; measure actual
storage and monitor free space. Lowering capacity never drops existing jobs.

If a paused position falls behind the feed retention floor, the subscription
enters `history_lost`; it never silently advances to retained history. Already
materialized deliveries can still complete. Rebuild the consumer using a fresh
C0 and the full generation algorithm, then explicitly reset with
`{"acknowledge_loss":true}` and continue polling from the consumer's own
checkpoint. A reset discards all subscription delivery rows and starts new
notifications at the current horizon. Tenant deletion also removes its
subscriptions and materialized deliveries in the same transaction. Organization
and member lifecycle reconciliation remains outside the common no-deletion
profile; the Xolo lifecycle extension defines scoped cleanup.

Operational targets at default settings are materialization and first attempt
within a few seconds when healthy, without a throughput or latency SLA.
`xolo_webhook_attempts_total` and `xolo_webhook_failures_total{reason}` are
process counters. `xolo_webhook_queue{state}`, `xolo_webhook_lag_seconds{stage}`
and `xolo_webhook_history_lost` are database-wide gauges sampled by each process;
use max rather than sum across replicas. Labels never contain tenant, endpoint
or event identifiers. Alert on sustained lag, failures, capacity saturation or
any history loss. Common publication itself is synchronous with commit; the
materialization lag measures committed events not yet queued.

Shutdown cancels HTTP and waits for all workers. Result recording receives at
most five additional seconds; a crash or unavailable database leaves a lease
that another worker can recover. Allow at least 15 seconds for process shutdown.
Console unavailability causes retries, not a server shutdown. Disabling the
worker preserves subscriptions and pending work, including when the provisioning
listener is disabled; cleanup resumes when the worker is enabled again.

## Identity, ownership and adoption (extension v1)

`GET /v1/xolo/extensions` reports effective ownership and the identity, adoption
and optional webhook extensions. The minimal `/v1/manifest` is unchanged.

### Startup ownership

`XOLO_OWNERSHIP` is a comma-separated map of `family=owner`. Families are
`tenant`, `tenant_domain`, `organization`, `member`,
`organization_membership`, and `subscription`; owners are `local` and
`control_plane`. Omitted families default to `local`. Unknown families and
values fail startup validation. For example:

```dotenv
XOLO_OWNERSHIP=tenant=control_plane,tenant_domain=control_plane,organization=control_plane,member=control_plane,organization_membership=control_plane,subscription=control_plane
```

The provisioning listener retains its required mTLS authority but rejects
writes to local families with 403. Local UI writes, including fragment requests,
are refused with 403 for control-plane families. Reads retain their existing
permissions. Enforcement is in the transaction-bound store: hiding a form is
not the enforcement mechanism. Role and invitation changes belong to
`organization_membership`; composite operations must own every affected family.
Business resources retain their existing authorization until their extension
profiles are implemented. API tokens and application shadow accounts remain usable.

When `member=control_plane`, first login requires an existing member. Adoption
keeps existing members, including locally created members. Contact email/name
are not updated from provider claims in that mode. Verified configured default
administrator emails remain the bootstrap exception; existing platform roles
are retained. Bootstrap writes and identity links are attributed to the resulting
user in the mutation audit. Provisioning never grants platform roles.

Ownership is an immutable process startup policy, not a database permission or
a live distributed lock. Stop **every** server, worker and operator writer before
changing it, and restart all replicas with identical settings. Operator tools
have database authority; they must preserve tenant/parent and last-owner checks.
`cmd/seed` is a test fixture tool, not a production maintenance path.

### Explicit identity and login

Member PUT optionally accepts `"identity":{"issuer":"https://id.example/","subject":"Subject"}`.
The issuer is an exact HTTPS URL (1–2048 UTF-8 bytes), with host and without
userinfo, query, fragment, surrounding whitespace or control characters. Subject
is an exact nonempty UTF-8 string of at most 255 bytes without control characters.
Subject spaces, case and issuer trailing slashes remain significant. No issuer
network access occurs during PUT. Null, incomplete and unknown identity fields
are rejected. GET, list and ETag include the declaration; public events contain
only resource keys and ETags, never the identity.

Configured named OIDC/Gitea discovery documents explicitly associate the local
provider ID with their issuer. Google uses `https://accounts.google.com`.
GitHub OAuth and static Gitea without discovery do not invent an issuer and
cannot prove an explicit issuer declaration. Signed ID tokens are additionally
verified against configured JWKS (RS256, issuer, audience and expiry); configured
OAuth token/UserInfo endpoints supply authenticated claims on the opaque path.
An `email_verified` boolean from the authenticator (Google's `verified_email`
also applies) is required for email attachment and default-admin bootstrap.
Contact email in the database is never verification evidence.

Links and declarations are unique **per tenant**. The same issuer/subject may
own distinct member UUIDs in different tenants. Login resolves the exact
identity first, then an existing link (including the explicitly mapped legacy
provider name). Verified-email attachment is allowed only for a member without
a declaration or another link. Conflicts refuse the entire transaction without
merging or reassigning accounts. Adding a declaration to its own matching link
preserves it; assigning a different identity to an existing link is rejected.
Keeping identity while changing email preserves the link. Omitting identity
removes the declaration and detaches the link, even when email is unchanged.
Later verified-email login may reattach it. An identical PUT is a no-op; a login
that changes neither profile nor link creates no new audit/version.

### Durable browser sessions and back-channel logout

Migration `202610030001` adds identity declarations, the session registry,
revocation watermarks and replay guards. OIDC cookies carry a random session ID;
every session authentication checks the shared database without a local cache.
Registry sessions expire after 24 hours (cookie expiry may shorten this).
Pre-migration OIDC cookies without a registry entry are rejected. Stop old
replicas before deployment: old binaries cannot enforce this registry. Preserve
shared cookie keys and synchronize replica clocks. Database failure fails closed.

Register `/auth/oidc/providers/{provider}/backchannel-logout` on a reachable
active tenant host as the provider's back-channel URI, with
`backchannel_logout_session_required=false`. It accepts a form POST of at most
16 KiB containing exactly one `logout_token`, no query parameters, no cookie.
The profile requires RS256 with configured signing JWKS, exact issuer, the sole
client audience, matching optional `azp`, nonempty `sub` and `jti`, required
`iat`/`exp`, at most five minutes of age and lifetime, and no future-time or
expiration grace. The single logout event must contain an empty object;
`nonce` is forbidden even if null. A `sid` with `sub` does not narrow scope;
sid-only logout is unsupported. Invalid tokens/replays return 400, success 200,
and a registry failure 503. No raw token is logged. This is a restricted profile
of [OIDC Back-Channel Logout 1.0](https://openid.net/specs/openid-connect-backchannel-1_0.html).

Logout atomically records the replay key and revocation watermark and removes
all registry sessions for that **issuer/subject across tenants and provider
aliases**. Other subjects and issuers remain usable. Accounts, memberships and
platform roles remain intact. Session issuance shares the publication lock with
revocation; callbacks bind their authentication start to the provider and OAuth
state, and authentication started before a revocation cannot issue a new
session afterward. Restart sign-in. Requests already authorized may finish.
Internal API tokens and externally supplied bearer/ID tokens are separate
credentials; back-channel logout revokes Xolo OIDC browser sessions only.
Watermarks and replay keys are durable and retained; budget storage accordingly.

### Export and transfer procedure

`GET /v1/xolo/export` returns the private inventory over the mTLS listener with
`Cache-Control: no-store`. Alternatively, with the existing database DSN:

```sh
go run ./cmd/adoption -action export -file /secure/xolo-adoption.json
go run ./cmd/adoption -action verify -file /secure/xolo-adoption.json
```

Export creates a new 0600 file, refuses overwrite and removes it on reported
failure. A killed process may leave an incomplete file. Protect and securely
transfer it: it contains contact data and declared identities. Do not log or
commit it. The versioned `xolo-adoption/1` JSON envelope has `payload` and
lowercase hexadecimal `sha256`. SHA-256 covers the **exact UTF-8 bytes of the
payload JSON value, including its braces**, excluding the envelope and its
whitespace. Do not pretty-print or reserialize the payload before verification.
The digest detects corruption, not malicious replacement: transport and file
permissions establish provenance.

Payload includes version, contract, persistent feed source, `c0`, the five
families, records (`family`, immutable `key`, `representation`, `etag`), record
count and `complete: true`. C0 is captured before all reads under the shared
publication lock, producing a consistent complete snapshot. Suspended resources
and `default` are included. Consumers must reject incomplete, altered or
incompatible files before committing an inventory generation. Export currently
buffers the common inventory in memory and holds the writer lock during reads;
plan a maintenance window for large inventories. It excludes business data,
linked but undeclared identities, sessions, API tokens, audit and webhook secrets.
Keep a separately tested restorable database backup.

1. Verify a real platform-administrator login and back up the instance. Keep
   ownership local while exporting. Import UUIDs **qualified by feed source**
   into a staged console inventory. Reconcile slug/domain/email collisions in
   the console without remapping IDs, duplicating `default`, merging accounts
   or sending marking PUTs. This repository provides the envelope validator,
   not a console application or an import endpoint into Xolo.
2. Replay the feed from C0, re-read resources and process duplicate invalidations
   idempotently. A 410 means discarding the staged inventory and starting again.
3. Stop all writers, catch up the final feed, set family ownership and restart
   with the same database. Verify discovery, rejection of local mutations and
   existing account access. No resource rewrite is needed.
4. To detach, test a real platform-admin login that will remain usable, stop
   **all** servers, workers and operator tools, then run:

   ```sh
   go run ./cmd/adoption -action detach -writers-stopped -operator-access-verified
   ```

   The command checks an active recorded platform administrator and atomically
   deletes subscriptions, encrypted secrets and deliveries. It records the OS
   operator UID in the audit. The flags attest operational checks; they cannot
   establish provider reachability or stop other processes automatically.
   Resources, UUIDs, identity links, audit, feed source and cursors remain intact.
   A transmitted webhook cannot be recalled. Clear ownership overrides and
   restart in local mode, then verify login, local writes and feed continuity.

## Lifecycle and business resource extensions

Both extensions default to disabled and are advertised through
`GET /v1/xolo/extensions`, separately from the common manifest. Enable
`XOLO_LIFECYCLE_ENABLED=true` only after consumers support `deleted`,
key reconciliation and history-loss resnapshot. Stop old replicas and align
all writer configurations before upgrading.

Canonical DELETE on a tenant, organization or member returns 202. The scope
remains readable and frozen until a durable export receipt and retention permit
transactional purge. Old immediate parent cascades have been removed; local
deletion returns 409 while the extension is disabled.

1. Save the deleted ETag, then download `{resource}/deletion/export` over the
   authorized mTLS listener.
2. Verify `xolo-deletion/1`, scope, version, inventory, count, `complete: true`
   and SHA-256 of the exact JSON payload bytes.
3. Save and read back the archive from protected durable storage. Only then POST
   `{resource}/purge-confirmation` with the deleted ETag in `If-Match` and
   `{"export_sha256":"…"}`. Generating a file is not confirmation.
4. Monitor `GET {resource}/deletion`. A failed purge rolls back and records
   `purge_failed`; fix storage and let the worker retry, including after restart.

`XOLO_LIFECYCLE_RETENTION` defaults to `720h`, fixed at scheduling;
`XOLO_LIFECYCLE_POLL_INTERVAL` defaults to `1m`. Tenant deletion supersedes
planned descendants. Purged UUIDs stay retired. Shared identities retain their
sessions in other tenants without restoring deleted account access. Domains
and memberships use immediate DELETE 204 with parent, last-owner and
`If-Match` checks; recreation gets a new ETag.

`XOLO_BUSINESS_RESOURCES_ENABLED=true` enables full PUT/GET/list for custom
roles, applications, quotas, alerts and providers under
`/v1/xolo/tenants/{tenantID}/organizations/{orgID}`, except tenant-level
`/v1/xolo/tenants/{tenantID}/quotas`. They share transactions,
`XOLO_OWNERSHIP`, ETags, preconditions and cursors. Provider credentials are
write-only, encrypted in storage and excluded from events.

The [complete profile and cleanup inventory](https://github.com/xolo-gateway/xolo/blob/main/internal/provisionning/LIFECYCLE.md)
specify fields, permissions, secrets, personal data, priorities and restoration
limits. Protect encryption keys separately; never replay archived sessions,
tokens or deliveries. Feed gaps return 410 and set `history_lost`.
Already authorized requests and transmitted notifications cannot be recalled.
Operators must separately define and verify archive, backup and external-log
expiration.
