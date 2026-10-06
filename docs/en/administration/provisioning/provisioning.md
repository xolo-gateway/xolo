# Provisioning API

The dedicated provisioning listener administers tenants, their domains,
organizations, members and roles using mutual TLS. It is disabled by default and
separate from the public HTTP listener.

It exposes a **common contract** — idempotent `PUT`s of tenants, domains,
organizations, members and memberships, identified by UUIDs the client chooses —
and, under `/v1/xolo`, the operations specific to Xolo. The contract replaces the
previous routes: see [Upgrading from the previous routes](#upgrading-from-the-previous-routes).

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
| `GET` | `/v1/manifest` | — returns `{"name","version","contract_version"}` |
| `PUT` | `/v1/tenants/{tenantID}` | `{"slug","name","status"}` |
| `PUT` | `/v1/tenants/{tenantID}/domains/{hostname}` | `{"status"}` |
| `PUT` | `/v1/tenants/{tenantID}/organizations/{orgID}` | `{"slug","name","status"}` |
| `PUT` | `/v1/tenants/{tenantID}/members/{memberID}` | `{"email","tenant_role","status"}`, optional `"display_name"` |
| `PUT` | `/v1/tenants/{tenantID}/organizations/{orgID}/members/{memberID}` | `{"role","status"}` |

- **Identifiers** are canonical lowercase UUIDs chosen by the client. Anything
  else is refused with `400 invalid_parameter`. A member is a user: `memberID`
  is the user identifier.
- **Every `PUT` answers `200`**, creation included, with the stored
  representation. A `PUT` identical to the stored state changes nothing and
  writes no audit, so a client can replay its whole desired state.
- **A `PUT` replaces the representation.** An omitted `display_name` is empty.
  Fields outside the contract — descriptions, currencies, identities, platform
  roles, custom roles — are never touched.
- **Values:** `status` is `active` or `suspended`; `tenant_role` is `owner` or
  `member`; an organization `role` is `owner`, `admin` or `member`. Slugs are
  lower-cased; names are 1 to 200 characters without control characters.
- **Bodies** are one JSON object of strings with `Content-Type: application/json`
  (otherwise `415 unsupported_media_type`), at most 1 MiB. An unknown field, a
  non-string value or invalid Unicode is `400 invalid_json`; a missing required
  field or a `null` is `400 invalid_representation`. The common routes accept no
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
| `invalid_parameter` | 400 | Identifier that is not a canonical UUID, or query parameter on a common route |
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

## Transactions, audit and correlation

Every provisioning mutation validates parents and performs its writes in one
transaction. A failure rolls back the whole operation, including changes to an
existing user, role assignments and the audit itself. PostgreSQL uses `SERIALIZABLE`;
SQLite writer conflicts and PostgreSQL serialization failures replay the whole
operation with bounded, cancelable backoff. There is no instance-wide publication lock.

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
audit is persisted atomically. Transactional reads bypass the shared user cache;
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

## Out of current scope

- Providers, LLM models, virtual models, middlewares, applications and their tokens, quotas, alerts and event settings: they remain managed through the web UI.
- Per-certificate scopes: any authorized URI administers the whole instance.
- Creating a member ahead of their first sign-in: a member is provisioned once
  they have signed in. The [invitation](../organisation/invitation/invitation.md)
  mechanism remains the email-based path, through the web UI.
- Deleting tenants, domains, organizations or memberships: suspend them instead.
- Conditional writes (`ETag`/`If-Match`), paginated common reads, the event feed
  and webhooks.
- No OpenAPI specification is generated yet.
