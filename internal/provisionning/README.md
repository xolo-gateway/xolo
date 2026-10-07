# Provisionning API — instance administration (management plane)

The Provisionning API lets an external system provision and reconcile a Xolo instance
without any human interaction: a control plane, a Kubernetes operator, a
Terraform provider, an Ansible playbook or a plain script.

It is deliberately **not** part of `/api/v1/`. Its security boundary is
different — instance-wide privileges, no user context — so it lives on its own
listener, on its own port, with its own TLS configuration, its own middleware
chain and its own authentication mechanism.

```
Xolo process
├── http.Server (internal/http)          Web UI, OIDC, /api/v1, LLM proxy
└── provisionning.Server (internal/provisionning)  dedicated listener + port + mutual TLS
        └── handler/v1                   transport only
                └── service.ProvisioningService   (internal/core/service)
                        └── port.ProvisioningTransaction (tenant, org, user, role, domain stores)
```

The resources are nested the way the domain is: a **tenant** owns **domains**,
**organizations** and **users**, an organization owns **members** and **roles**.
The root of `/v1` serves the common contract (idempotent `PUT`s identified by
client-chosen UUIDs, `common.go`); the operations specific to Xolo live under
`/v1/xolo`.

Both servers share the root context of `cmd/server`, and the Provisionning API uses the
**same database** as the public server. Transactional writes use bound stores;
cache invalidation and local events are deferred until commit.

## Authentication

Mutual TLS, and nothing else. There is no OIDC, no session, no cookie and no
user API token on this port, and no Provisionning API endpoint is ever mounted on the
public HTTP port. There is no anonymous fallback.

The listener requires **TLS 1.3**, a client certificate verified against the configured
CA, and **exactly one URI SAN** matching `XOLO_PROVISIONNING_API_AUTHORIZED_URIS`
exactly. The Common Name never authorizes a client. These checks run during the
TLS handshake and in middleware around every route, including health, permissions,
unknown routes and refused methods. HTTP rejections use `403` and
`client_certificate_rejected` in the existing JSON error envelope.

An authorized URI administers the whole instance, with tenant and organization
parent checks. There are no permissions per certificate. Renewing a certificate
with the same authorized URI retains its identity and rate budget.

**Upgrade requirement:** installations already enabling this listener must reissue
client certificates with one URI SAN and configure the allowlist before restarting.
An empty list, duplicate or invalid URI prevents startup. Clients must support TLS 1.3.

TLS material is loaded at startup, before the listener opens: a missing or
inconsistent certificate, key or CA bundle is a startup failure, never a
first-request failure.

## Configuration

| Variable | Default | Description |
|---|---|---|
| `XOLO_PROVISIONNING_API_ENABLED` | `false` | Opens the administration listener |
| `XOLO_PROVISIONNING_API_ADDRESS` | `:3003` | Listen address |
| `XOLO_PROVISIONNING_API_TLS_CERT_FILE` | — | Server certificate (PEM), required when enabled |
| `XOLO_PROVISIONNING_API_TLS_KEY_FILE` | — | Server private key (PEM), required when enabled |
| `XOLO_PROVISIONNING_API_TLS_CLIENT_CA_FILE` | — | Authority verifying client certificates, required when enabled |
| `XOLO_PROVISIONNING_API_AUTHORIZED_URIS` | — | Required comma-separated list of distinct absolute client URIs |
| `XOLO_PROVISIONNING_API_RATE_LIMIT` | `10` | Requests/second per authorized URI, per process |
| `XOLO_PROVISIONNING_API_RATE_BURST` | `20` | Burst allowance per URI |
| `XOLO_PROVISIONNING_API_SHUTDOWN_TIMEOUT` | `10s` | Graceful shutdown budget |
| `XOLO_PROVISIONNING_API_EVENT_RETENTION` | `720h` | History kept in the event feed, `0` for unlimited; applies even when the listener is disabled |

The rate and burst apply to each process: with N replicas, a given URI gets N times the configured values.

Multi-tenancy is configured on the instance, not on this API:

| Variable | Default | Description |
|---|---|---|
| `XOLO_MULTITENANCY_ENABLED` | `false` | Allows more than one tenant and routes requests through domains |
| `XOLO_MULTITENANCY_HOST_PATTERN` | — | Upgrade only: expanded once into one domain per existing tenant, see [Domains and routing](#domains-and-routing) |
| `XOLO_MULTITENANCY_DEFAULT_TENANT_SLUG` | `default` | The tenant served when multi-tenancy is disabled |

## Common contract

| Method | Route | Body |
|---|---|---|
| `GET` | `/v1/manifest` | — returns `{"name","version","contract_version","capabilities"}` |
| `PUT` | `/v1/tenants/{tenantID}` | `{"slug","name","status"}` |
| `PUT` | `/v1/tenants/{tenantID}/domains/{hostname}` | `{"status"}` |
| `PUT` | `/v1/tenants/{tenantID}/organizations/{orgID}` | `{"slug","name","status"}` |
| `PUT` | `/v1/tenants/{tenantID}/members/{memberID}` | `{"email","tenant_role","status"}`, optional `"display_name"` |
| `PUT` | `/v1/tenants/{tenantID}/organizations/{orgID}/members/{memberID}` | `{"role","status"}` |

Each of these resources is also readable, listable and followed through the
event feed: see [Reads, conditions and synchronization](#reads-conditions-and-synchronization).
`capabilities` lists `conditional_writes`, `events` and `reads`.

- **Identifiers** are canonical lowercase UUIDs chosen by the client. Anything
  else is refused with `400 invalid_parameter`. A member is a user: `memberID`
  is the user identifier.
- **Every `PUT` answers `200`**, creation included, with the stored
  representation and its `ETag`. A `PUT` identical to the stored state changes
  nothing, writes no audit and keeps its `ETag`, so a client can replay its
  whole desired state.
- **A `PUT` replaces the representation.** An omitted `display_name` is empty.
  Fields outside the contract — descriptions, currencies, identities, platform
  roles, custom roles — are never touched.
- **Values:** `status` is `active` or `suspended`; `tenant_role` is `owner` or
  `member`; an organization `role` is `owner`, `admin` or `member`. Slugs are
  lower-cased; names are 1 to 200 characters without control characters.
- **Bodies** are one JSON object of strings with `Content-Type: application/json`
  (otherwise `415 unsupported_media_type`), at most 1 MiB. An unknown field, a
  non-string value or invalid Unicode is `400 invalid_json`; a missing required
  field or a `null` is `400 invalid_representation`. The `PUT`s accept no
  query parameter (`400 invalid_parameter`).

### Tenants

`PUT /v1/tenants/{tenantID}` creates the tenant or renames it: slug and name
change freely, domains stay attached. Creating a second tenant is refused with
`409` while `XOLO_MULTITENANCY_ENABLED` is false. The `default` tenant keeps its
slug and stays `active`: it is the tenant every single-tenant instance resolves
to. Its identifier is read with `GET /v1/xolo/tenants?slug=default`.

A `suspended` tenant answers `404` on all its domains.

### Organizations

`PUT …/organizations/{orgID}` creates the organization with its builtin roles,
or updates its slug, name and status. An organization identifier already used
by another tenant is a `409`.

### Members

`PUT …/members/{memberID}` updates a user of the tenant: email, display name,
tenant role and status. `suspended` deactivates the account. The authentication
identity and the platform roles are never modified.

**A member is provisioned once they have signed in.** A `PUT` on an unknown
user answers `404`: an account created without an identity would block that
person's first sign-in on its email. The control plane finds the account with
`GET /v1/xolo/tenants/{tenantID}/users?provider=&subject=`, or lets it sign in
inactive (`XOLO_HTTP_AUTHN_ACTIVE_BY_DEFAULT=false`) and picks it up with
`GET /v1/xolo/tenants/{tenantID}/users?active=false`.

`tenant_role` declares the tenant owners; it grants no platform privilege. A
tenant always keeps one active owner once it has one: demoting or suspending the
last one is refused with `409 last_owner`.

**Platform administrators are protected.** Any `PUT` that would change an
account holding the platform `admin` role — email, display name, status or
tenant role — is refused with `409 platform_admin_protected`; an identical `PUT`
still answers `200`. The same protection applies to
`PUT /v1/xolo/tenants/{tenantID}/users`. Provisioning never acts on
platform-wide privileges.

### Memberships

`PUT …/organizations/{orgID}/members/{memberID}` adds the member to the
organization or updates the membership. `role` sets the builtin role of the
membership; custom roles assigned through `/v1/xolo` are kept. A `suspended`
membership grants no access to the organization and is kept with its roles.

An organization always keeps one active owner once it has one: demoting or
suspending the last one is refused with `409 last_owner`. An organization or a
member of another tenant is a `404 parent_not_found`.

## Domains and routing

`PUT /v1/tenants/{tenantID}/domains/{hostname}` declares a hostname of the tenant
or changes its status. The hostname must already be in lower case, without port
or IP literal (`400 invalid_hostname` otherwise), and belongs to at most one
tenant (`409` otherwise). A tenant may own several domains.

With `XOLO_MULTITENANCY_ENABLED=true`, the public server routes each request
through these domains: the request host must be an `active` domain of an
`active` tenant, otherwise the request answers `404`. Links, redirects and OAuth
callbacks keep the scheme, port and path of `XOLO_HTTP_BASE_URL` and take the
domain as host. On a single-tenant instance domains are stored but not used for
routing.

On the first multi-tenant startup of an upgraded instance,
`XOLO_MULTITENANCY_HOST_PATTERN`, when set, is expanded once into one `active`
domain per existing tenant, so every tenant stays reachable on its former
hostname. The expansion never runs again: tenants created afterwards need their
domains declared through the API, and the variable can be removed. A hostname
already declared is kept and logged, never reassigned.

## Reads, conditions and synchronization

Every resource of the common contract has a **projection**: the representation
its `PUT` answers, kept in the same transaction as the resource. Every change
of a projection, whatever its origin — this API, the web UI, a sign-in, an
accepted invitation, a deletion — receives the next position of a single
**event feed** and publishes one event.

| Method | Route | Answer |
|---|---|---|
| `GET` | `/v1/tenants/{tenantID}`, `…/domains/{hostname}`, `…/organizations/{orgID}`, `…/members/{memberID}`, `…/organizations/{orgID}/members/{memberID}` | The representation, with its `ETag` header |
| `GET` | `/v1/tenants`, `…/domains`, `…/organizations`, `…/members`, `…/organizations/{orgID}/members` | `{"items":[{"key","representation","etag"}],"next_cursor"}` |
| `GET` | `/v1/events/cursor` | `{"cursor"}`: the current end of the feed |
| `GET` | `/v1/events?cursor=` | `{"items":[…],"next_cursor","has_more"}` |

### ETags and conditional writes

- **The ETag is a revision**, `W/"<n>"`: the feed position of the last change
  of the representation. Revisions are persisted and strictly increase across
  the whole instance; they never depend on a clock, so a clock step or a skew
  between replicas never brings an ETag back, and neither does deleting then
  recreating a resource. Existing resources received their own revision on
  upgrade. ETags are opaque validators: compare them, do not compute them.
- A `PUT` answers the projection written by its own transaction and its `ETag`.
  A `PUT` that changes nothing keeps the revision.
- **`If-Match`** on a `PUT` is checked within the mutation transaction, before
  any write: `*` or a comma-separated list of ETags, compared weakly. A stale
  condition answers `412 precondition_failed`, even for a body identical to the
  stored state; `*` on a missing resource is a `412` as well. Without `If-Match`
  the write is unconditional. Of two writers holding the same ETag, exactly
  one succeeds. A malformed condition, or any `If-None-Match`, is
  `400 invalid_precondition`.

### Lists and cursors

- A collection is the path of its unit resource without the last segment.
  Lists accept only `limit` (1 to 1000, default 100) and `cursor`, once each
  (`400 invalid_parameter` otherwise); unit reads, `/v1/manifest` and
  `/v1/events/cursor` accept no query parameter.
- Items are ordered by the bytewise order of their key. A page is not a
  snapshot of the collection: changes committed during an enumeration are
  caught by the feed. There is no total count.
- `next_cursor` is `null` on the last page. Cursors are signed with a secret of
  the instance and bound to the collection, its parents and the limit: an
  altered cursor, or one used for another collection, limit or the feed,
  answers `400 invalid_cursor`. A list cursor expires 24 hours after the first
  page, without renewal (`410 cursor_expired`). Cursors are opaque but not
  encrypted.
- An unknown parent answers `404 parent_not_found` on a list and `404
  not_found` on a unit read.

### Event feed

Events follow a closed [CloudEvents 1.0](https://cloudevents.io) profile, and
carry no representation and no personal data:

```json
{"specversion":"1.0","id":"…","source":"urn:uuid:…","type":"organization.updated.v1",
 "time":"…","datacontenttype":"application/json","sequence":"42","requestid":"…",
 "data":{"resource_type":"organization","key":{"tenant_id":"…","organization_id":"…"},"etag":"W/\"42\""}}
```

- `type` is `<resource_type>.created.v1`, `.updated.v1` or `.deleted.v1`, with
  `resource_type` one of `tenant`, `tenant_domain`, `organization`, `member` and
  `organization_membership`. A deletion carries no `etag`.
- **One event per resource and per commit.** A no-op or a rolled back
  transaction publishes nothing. Within a commit, deletions come first, from
  children to parents, then creations and updates from parents to children.
- `sequence` orders events **in commit order**: a position is visible only once
  every lower position is either visible or rolled back, so a consumer never
  skips an event by resuming after the last position it read. Positions have
  gaps. `time` is informative only. `requestid` is the `X-Request-ID` of the
  provisioning request, or a generated correlation for other changes.
- `has_more=false` means the page reached the end of the feed; its
  `next_cursor` resumes there. Event cursors never expire by themselves.
- Application shadow users are not members: they are neither projected nor
  writable through `PUT …/members/{memberID}`.

**Consumer algorithm.**

1. Capture a cursor with `GET /v1/events/cursor`, **before** the inventory.
2. Enumerate `/v1/tenants`, then the domains, organizations, members and
   memberships of each tenant.
3. Poll `GET /v1/events?cursor=`; for each event, read the resource again and
   apply its current representation (`404` means it is gone). Deduplicate
   events by `(source, id)`; never let a read issued earlier overwrite the
   result of a later one.
4. Persist the applied state, **then** the `next_cursor`. A crash in between
   replays events, which is harmless.
5. On `410 cursor_expired`, rebuild into a new generation from step 1, and
   switch over only once it is complete.

### Retention

`XOLO_PROVISIONNING_API_EVENT_RETENTION` (default `720h`, `0` keeps every
event) removes, every hour, the oldest events created before the retention
period. The removal stops at the first event kept, so a clock step back never
removes an event that follows a retained one. A cursor older than the removed
events answers `410 cursor_expired`. Retention runs even when the listener is
disabled, since changes are published in any case. Only this retention ever
removes events: deleting resources never invalidates other consumers' cursors.

### Storage and costs

- The source of the feed and the secret signing cursors are stored in the
  database: back them up with it. Restoring another database invalidates
  cursors (`400 invalid_cursor`) and changes the `source`: consumers rebuild.
- A transaction takes the **feed lock** only when it changes a projection, at
  its very end, and holds it until commit; on PostgreSQL it is an advisory lock
  next to a sequence. Reads, no-ops, sign-ins that change nothing and the LLM
  proxy never take it. Projection changes are thereby serialized for the short
  time of their commit: this trades write throughput for a gapless feed.

## Xolo extensions

The operations specific to Xolo live under `/v1/xolo`. Their payloads are JSON
in camelCase, timestamps are RFC 3339, collections are returned as
`{"items": […], "page": 1, "limit": 50, "total": 123}`, and unknown fields are
rejected.

| Method | Route | Notes |
|---|---|---|
| `GET` | `/v1/xolo/healthz` | Behind mutual TLS as well |
| `GET` | `/v1/xolo/permissions` | The RBAC catalog: the only source of valid permission codes |
| `GET` | `/v1/xolo/tenants` | `?slug=` for an exact lookup, otherwise `?page=&limit=` |
| `GET` | `/v1/xolo/tenants/{tenantID}` | |
| `PATCH` | `/v1/xolo/tenants/{tenantID}` | `name`, `description`, `active` |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations` | `?slug=` for an exact lookup, otherwise `?page=&limit=` |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}` | |
| `PATCH` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}` | `name`, `description`, `active`, `currency`, `shareQuotaEqually` |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/members` | Paginated |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/members/{membershipID}` | |
| `PUT` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/members/{membershipID}/roles` | Full replacement of the role set |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles` | Builtin and custom roles |
| `POST` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles` | Custom role |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles/{roleID}` | |
| `PUT` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles/{roleID}` | Custom roles only |
| `DELETE` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles/{roleID}` | Custom roles only |
| `GET` | `/v1/xolo/tenants/{tenantID}/users` | `?provider=&subject=` for an exact lookup, otherwise `?search=&active=&page=&limit=` |
| `PUT` | `/v1/xolo/tenants/{tenantID}/users` | Idempotent upsert on `(provider, subject)`: `201` when created, `200` otherwise |
| `GET` | `/v1/xolo/tenants/{tenantID}/users/{userID}` | |

Users hang from the tenant because `(provider, subject)` is only unique within
one: the same person signing in on two tenants owns two distinct accounts.

## Errors

Every error uses the same envelope:

```json
{"error": {"code": "last_owner", "message": "…"}}
```

| Code | HTTP | Cause |
|---|---|---|
| `invalid_parameter` | 400 | Identifier that is not a canonical UUID, or query parameter a common route does not define |
| `invalid_cursor` | 400 | Cursor altered, empty, or issued for another collection, limit or feed |
| `invalid_precondition` | 400 | Malformed `If-Match`, or `If-None-Match` |
| `invalid_json` | 400 | Common route: malformed JSON, unknown field, non-string value, invalid Unicode |
| `invalid_representation` | 400 | Common route: missing required field or `null` |
| `invalid_hostname` | 400 | Hostname not in lower case, with a port, an IP literal or an invalid label |
| `invalid_request` | 400 | `/v1/xolo`: malformed body, unknown field, invalid query parameter |
| `client_certificate_rejected` | 403 | Client certificate or URI is not authorized |
| `not_found` | 404 | Unknown resource or route, or a resource belonging to another tenant or organization |
| `parent_not_found` | 404 | The tenant, organization or member a resource hangs from does not exist in that scope |
| `method_not_allowed` | 405 | Known resource, wrong method |
| `unsupported_media_type` | 415 | Common route without `Content-Type: application/json` |
| `conflict` | 409 | Identifier or hostname owned by another tenant, slug already used, or a business invariant |
| `last_owner` | 409 | The change would leave a tenant or an organization without an active owner |
| `platform_admin_protected` | 409 | The change targets a platform administrator |
| `cursor_expired` | 410 | List cursor older than 24 hours, or event cursor older than the retained events: rebuild |
| `precondition_failed` | 412 | `If-Match` does not designate the current revision |
| `unprocessable` | 422 | Well-formed value refused by the domain |
| `rate_limited` | 429 | Per-URI budget exceeded; retry after the `Retry-After` seconds |
| `internal_error` | 500 | Unexpected failure |

Messages are always built explicitly. Stack traces, SQL errors, file paths, TLS
details and secrets never reach the client: the full detail is logged
server-side.

## Invariants

- Provisioning **never** grants or modifies platform-wide privileges. A user
  created through `PUT /v1/xolo/tenants/{tenantID}/users` receives exactly the
  `user` platform role, platform roles are never modified, and a platform
  administrator is never modified at all.
- The addresses listed in `XOLO_HTTP_AUTHN_DEFAULT_ADMINS` are reserved: writing
  one of them on a user is refused with `422`. The authentication bridge grants
  the platform admin role to whoever signs in with such an address, so accepting
  it here would be an indirect privilege escalation.
- A tenant or an organization keeps at least one active owner once it has one.
- A suspended membership grants nothing; a suspended domain or tenant routes
  nothing.
- A role can only be assigned to a membership of the organization it belongs to.
  Anything else is `422`, and no role is modified.
- A membership or role belonging to another organization is reported as `404`,
  and so is an organization or a user belonging to another tenant.
- The `default` tenant keeps its slug and stays active.
- Builtin roles cannot be modified nor deleted.
- Only permission codes present in the RBAC catalog are accepted.

## Upgrading from the previous routes

Every previous route moved or was replaced; there is no alias. Identifiers are
unchanged: an existing resource is addressed by its current UUID.

| Previous | Now |
|---|---|
| `GET /v1/healthz`, `GET /v1/permissions` | `GET /v1/xolo/healthz`, `GET /v1/xolo/permissions` |
| `GET /v1/tenants` | `GET /v1/xolo/tenants` |
| `POST /v1/tenants` `{slug, name, description, active}` | `PUT /v1/tenants/{tenantID}` `{slug, name, status}` with a UUID of your choice; `description` through `PATCH /v1/xolo/tenants/{tenantID}` |
| `GET /v1/tenants/{tenantID}` | `GET /v1/xolo/tenants/{tenantID}` |
| `PATCH /v1/tenants/{tenantID}` `{name, description, active}` | `PATCH /v1/xolo/tenants/{tenantID}` (same body), or `PUT /v1/tenants/{tenantID}` `{slug, name, status}` |
| `DELETE /v1/tenants/{tenantID}` | Removed: `PUT /v1/tenants/{tenantID}` with `"status": "suspended"` |
| `GET /v1/tenants/{tenantID}/organizations[/{orgID}]` | `GET /v1/xolo/tenants/{tenantID}/organizations[/{orgID}]` |
| `POST /v1/tenants/{tenantID}/organizations` `{slug, name, description, currency, active, owner}` | `PUT /v1/tenants/{tenantID}/organizations/{orgID}` `{slug, name, status}`; `description` and `currency` through `PATCH /v1/xolo/…/organizations/{orgID}`; the owner through `PUT …/organizations/{orgID}/members/{userID}` `{"role": "owner", "status": "active"}` once they have signed in |
| `PATCH /v1/tenants/{tenantID}/organizations/{orgID}` | `PATCH /v1/xolo/tenants/{tenantID}/organizations/{orgID}` (same body) |
| `DELETE /v1/tenants/{tenantID}/organizations/{orgID}` | Removed: `PUT …/organizations/{orgID}` with `"status": "suspended"` |
| `GET …/organizations/{orgID}/members[/{membershipID}]` | `GET /v1/xolo/…/organizations/{orgID}/members[/{membershipID}]` |
| `POST …/organizations/{orgID}/members` `{userId \| user, roleIds, builtinRoles}` | `PUT /v1/tenants/{tenantID}/organizations/{orgID}/members/{userID}` `{role, status}`; custom roles through `PUT /v1/xolo/…/members/{membershipID}/roles` |
| `PUT …/members/{membershipID}/roles` | `PUT /v1/xolo/…/members/{membershipID}/roles` (same body) |
| `DELETE …/members/{membershipID}` | Removed: `PUT …/organizations/{orgID}/members/{userID}` with `"status": "suspended"` |
| `…/organizations/{orgID}/roles[/{roleID}]` (all methods) | `/v1/xolo/…/organizations/{orgID}/roles[/{roleID}]` (same bodies) |
| `GET`, `PUT /v1/tenants/{tenantID}/users` | `GET`, `PUT /v1/xolo/tenants/{tenantID}/users` (same bodies) |
| `GET /v1/tenants/{tenantID}/users/{userID}` | `GET /v1/xolo/tenants/{tenantID}/users/{userID}` |
| `PATCH /v1/tenants/{tenantID}/users/{userID}` `{email, displayName, active}` | `PUT /v1/tenants/{tenantID}/members/{userID}` `{email, display_name, tenant_role, status}` |

**Stop every server before upgrading.** Migration `202610070001` adds domains,
tenant roles and membership statuses. An older server still running would keep
routing on the host pattern and grant access through suspended memberships. The
migration cannot be rolled back. With `XOLO_STORAGE_AUTO_MIGRATE=false`, stop all
writers, back up the database, then run `bin/migrate apply -writers-stopped`
before starting the server. The expansion of `XOLO_MULTITENANCY_HOST_PATTERN`
into domains is done by the server at startup, in both modes.

**Migration `202610080001` also requires every server to be stopped.** It
creates the projections and the event feed, and gives every existing resource
its own revision without publishing any event: consumers start with an
inventory. An older server still running would write without publishing, and
projections, ETags and the feed would silently diverge. The migration cannot be
rolled back.

## Transactions, audit and correlation

Every provisioning mutation validates parents and performs its writes in one
transaction. A failure rolls back the whole operation, including changes to an
existing user, role assignments and the audit itself. PostgreSQL uses `SERIALIZABLE`;
SQLite writer conflicts and PostgreSQL serialization failures replay the whole
operation with bounded, cancelable backoff. Only a transaction changing a projection
takes the feed lock, at its very end (see [Storage and costs](#storage-and-costs)).

`mutation_audits` records one before/after pair per resource actually changed
(tenants, domains, organizations, users, memberships and roles), including role associations
and cascading deletion. Repeated changes to a resource coalesce within the
transaction; a no-op produces no audit. States exclude secrets and technical
timestamps. Audits retain the tenant/organization scope after resource deletion.
UUID audit identifiers do not imply commit order. This audit covers provisioning
mutations only; reads and ordinary web UI operations do not write it.

Send `X-Request-ID` as a single value of exactly 32 lowercase hexadecimal characters.
Invalid, repeated or missing values are replaced. The retained value is returned in
the response and used in logs, audit and local events, unchanged across retries.
The actor URI comes only from the authorized certificate. Internal calls use
`urn:xolo:operator:local` and a generated correlation ID when no actor is supplied.

Existing local member and role events retain their types and messages, including
separate member-added and role-assigned events. They are emitted only after commit
through the existing asynchronous mechanism; they are not a durable outbox. The
audit, the projections and the event feed are persisted atomically with the change. Transactional reads bypass the shared user cache;
affected user entries, secondary keys and cascading token entries are invalidated
after commit.

Rate budgets are local to each process, allocated only for configured URIs and
shared by certificates bearing the same URI. Exceeding the budget returns `429`
with `Retry-After` (seconds), `rate_limited`, and the retained request ID.

Migration `202610060001` follows the UUID migration and adds the audit table on
upgrades and fresh installations. Its rollback refuses to erase audit history.

## Development PKI

```bash
mkdir -p dev-pki && cd dev-pki

# Certificate authority
openssl req -x509 -newkey rsa:4096 -nodes -days 365 \
  -keyout ca.key -out ca.crt -subj "/CN=xolo-dev-ca"

# Server certificate
openssl req -newkey rsa:4096 -nodes -keyout server.key -out server.csr \
  -subj "/CN=localhost"
openssl x509 -req -in server.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out server.crt -days 365 \
  -extfile <(printf "subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth")

# Client certificate
openssl req -newkey rsa:4096 -nodes -keyout client.key -out client.csr \
  -subj "/CN=control-plane"
openssl x509 -req -in client.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out client.crt -days 365 \
  -extfile <(printf "subjectAltName=URI:urn:xolo:client:control-plane\nextendedKeyUsage=clientAuth")
```

```bash
XOLO_SECRET_KEY=$(openssl rand -hex 32) \
XOLO_PROVISIONNING_API_ENABLED=true \
XOLO_PROVISIONNING_API_AUTHORIZED_URIS=urn:xolo:client:control-plane \
XOLO_PROVISIONNING_API_TLS_CERT_FILE=dev-pki/server.crt \
XOLO_PROVISIONNING_API_TLS_KEY_FILE=dev-pki/server.key \
XOLO_PROVISIONNING_API_TLS_CLIENT_CA_FILE=dev-pki/ca.crt \
bin/server

# Refused: no client certificate
curl -sk https://localhost:3003/v1/manifest

CURL="curl -s --cacert dev-pki/ca.crt --cert dev-pki/client.crt --key dev-pki/client.key -H Content-Type:application/json"

# Accepted: read the identifier of the default tenant
$CURL "https://localhost:3003/v1/xolo/tenants?slug=default"
TENANT=... # the id read above

# Create an organization with an identifier of your choice
ORG=$(uuidgen | tr A-Z a-z)
$CURL -X PUT "https://localhost:3003/v1/tenants/$TENANT/organizations/$ORG" \
  -d '{"slug":"acme","name":"Acme","status":"active"}'
```

In production, use a managed certificate authority (Vault, cert-manager, internal PKI) and rotate client certificates.

## Out of scope for now

- Providers, LLM models, virtual models, middlewares, applications and their
  tokens, quotas, alerts and event settings. They all already have a
  `port.*Store`: exposing them means adding a handler file and its DTOs, with no
  architectural change.
- Per-certificate scopes.
- Creating a member ahead of their first sign-in: an account without identity
  would block that person's sign-in on its email until identities can be
  declared with the member. The existing `InviteToken` mechanism remains the
  email path, through the Web UI.
- Deleting tenants, domains, organizations or memberships: suspend them.
- Webhooks: the event feed is polled.
- No generated OpenAPI specification.

## User documentation

User-facing versions of this page live in
`docs/{en,fr,es}/administration/provisioning/provisioning.md`, e.g.
[`docs/fr/administration/provisioning/provisioning.md`](../../docs/fr/administration/provisioning/provisioning.md).
