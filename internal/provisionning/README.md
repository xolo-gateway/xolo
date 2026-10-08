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

Webhooks have their own variables, see [Webhooks](#webhooks).

## Common contract

| Method | Route | Body |
|---|---|---|
| `GET` | `/v1/manifest` | — returns `{"name","version","contract_version","capabilities"}` |
| `PUT` | `/v1/tenants/{tenantID}` | `{"slug","name","status"}` |
| `PUT` | `/v1/tenants/{tenantID}/domains/{hostname}` | `{"status"}` |
| `PUT` | `/v1/tenants/{tenantID}/organizations/{orgID}` | `{"slug","name","status"}` |
| `PUT` | `/v1/tenants/{tenantID}/members/{memberID}` | `{"email","tenant_role","status"}`, optional `"display_name"` and `"identity"` |
| `PUT` | `/v1/tenants/{tenantID}/organizations/{orgID}/members/{memberID}` | `{"role","status"}` |

Each of these resources is also readable, listable and followed through the
event feed: see [Reads, conditions and synchronization](#reads-conditions-and-synchronization).
`capabilities` lists `adoption`, `business_resources`, `conditional_writes`,
`events`, `identity`, `ownership` and `reads`, plus `webhooks` when enabled.
Treat it as a set: its order is not significant.

- **Identifiers** are canonical lowercase UUIDs chosen by the client. Anything
  else is refused with `400 invalid_parameter`. A member is a user: `memberID`
  is the user identifier.
- **Every `PUT` answers `200`**, creation included, with the stored
  representation and its `ETag`. A `PUT` identical to the stored state changes
  nothing, writes no audit and keeps its `ETag`, so a client can replay its
  whole desired state.
- **A `PUT` replaces the representation.** An omitted `display_name` is empty.
  An omitted `identity` is kept, see [Declared identity](#declared-identity).
  Fields outside the contract — descriptions, currencies, sign-in links,
  platform roles, custom roles — are never touched.
- **Values:** `status` is `active` or `suspended`; `tenant_role` is `owner` or
  `member`; an organization `role` is `owner`, `admin` or `member`. Slugs are
  lower-cased; names are 1 to 200 characters without control characters.
- **Bodies** are one JSON object of strings (except the member `identity`) with `Content-Type: application/json`
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

`PUT …/members/{memberID}` creates the member or updates it: email, display
name, tenant role, status and declared identity. `suspended` deactivates the
account. The platform roles are never modified.

**A member can be provisioned ahead of their first sign-in.** A `PUT` on an
unknown identifier creates the account with the platform `user` role, linked to
no sign-in yet: its first sign-in links it, through its declared identity or a
verified email (see below). An email that another account of the tenant already
holds, whatever its case, is refused with `409 conflict`. An API token keeps
designating its owner, linked or not.

`tenant_role` declares the tenant owners; it grants no platform privilege. A
tenant always keeps one active owner once it has one: demoting or suspending the
last one is refused with `409 last_owner`.

**Platform administrators are protected.** Any `PUT` that would change an
account holding the platform `admin` role — email, display name, status, tenant
role or identity — is refused with `409 platform_admin_protected`; an identical
`PUT` still answers `200`. The same protection applies to
`PUT /v1/xolo/tenants/{tenantID}/users`. A sign-in never attaches a platform
administrator to a new identity either, neither through a declaration nor
through an email. Provisioning never acts on platform-wide privileges.

### Declared identity

The optional `identity` field designates the sign-in of a member:

```json
{"email":"jane@corp.tld","tenant_role":"member","status":"active",
 "identity":{"issuer":"https://id.corp.tld/realms/main","subject":"6f0c2a1e"}}
```

| `identity` | Effect |
|---|---|
| absent | The declared identity and the sign-in link are kept: a client unaware of the field never detaches anybody. |
| `null` | The declared identity is removed and the sign-in link detached. The member signs in again only through a new declaration or a verified email. |
| object | The identity is declared. A member already linked to another sign-in is refused with `409 conflict`: to move an account to another provider or identity, send `null`, then the new identity. |

- `issuer` is an exact HTTPS URL of at most 2048 bytes, with a host and without
  userinfo, query, fragment, surrounding whitespace or control characters.
  `subject` is an exact UTF-8 string of 1 to 255 bytes without control
  characters. Nothing is normalized: case, spaces and a trailing slash are
  significant. Any other value — `{}`, a missing, empty or extra field, a
  non-string — is `400 invalid_representation`. Xolo calls no issuer.
- An identity designates at most one member **per tenant**, declared or already
  linked: declaring an identity another member holds is `409 conflict`. The same
  identity can own a different member in each tenant.
- `GET`, lists and the `ETag` include the declared identity. Events carry keys
  and ETags only, never the identity.

**At sign-in**, Xolo resolves the account in this order:

1. the account already linked to that sign-in;
2. the member whose declared identity is the issuer and subject the identity
   provider proved;
3. the only account of the tenant holding that email, compared regardless of
   case, when the identity provider asserts it verified (`email_verified`, or
   `verified_email` for Google) and the account is linked to no sign-in and
   declares no identity;
4. otherwise a new account, under `XOLO_HTTP_AUTHN_AUTO_CREATE_USERS`, the
   default administrators and pending invitations, as before.

A sign-in matches a declared identity only when its provider proves the issuer:
named OIDC providers and Gitea with a discovery document (the discovered
`issuer`), Google (`https://accounts.google.com`), and the token authenticators
of these providers. GitHub OAuth and a Gitea without discovery prove none: their
members sign in through a verified email.

Nothing is ever merged nor reassigned. Several accounts whose emails differ
only by case refuse the attachment, and are left as they are; a conflicting
identity never falls back on the email. A refused sign-in answers `409` and
records an `auth.login.failed` event. A request of an already linked account
only reads it: no transaction, no lock. Linking a sign-in changes no projection
and publishes no event.

Known limits: the default administrators (`XOLO_HTTP_AUTHN_DEFAULT_ADMINS`) are
recognized by the email the identity provider returns, verified or not; and each
sign-in still copies the email and display name the identity provider returns
onto the account, unless the control plane owns the members (see *Managed
members*).

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
  `resource_type` one of `tenant`, `tenant_domain`, `organization`, `member`,
  `organization_membership` and the business families `provider`,
  `custom_role`, `application`, `quota` and `alert`. A deletion carries no
  `etag`.
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

## Webhooks

Webhooks push the events of the [feed](#event-feed) to HTTPS receivers, as
they are committed. They are a notification, not a source of truth: a consumer
keeps its own checkpoint in the feed, polls `/v1/events` on startup, after a
reconnection and periodically, and never advances that checkpoint because of a
webhook. A lost webhook therefore never loses a change.

Webhooks are disabled by default. The delivery worker runs in every process
where `XOLO_WEBHOOKS_ENABLED=true`, whether or not the provisioning listener is
enabled there; subscriptions are managed through the listener.

| Variable | Default | Description |
|---|---|---|
| `XOLO_WEBHOOKS_ENABLED` | `false` | Runs the preparation, delivery and cleanup of webhooks, and announces `webhooks` in the manifest |
| `XOLO_WEBHOOKS_ALLOWED_ORIGINS` | — | Required: comma-separated HTTPS origins (`https://host[:port]`, no path) a subscription may target |
| `XOLO_WEBHOOKS_ALLOW_PRIVATE_NETWORKS` | `false` | Also allows loopback and private addresses |
| `XOLO_WEBHOOKS_TLS_CA_FILE` | — | Additional trusted authorities (PEM); the system ones stay trusted |
| `XOLO_WEBHOOKS_WORKERS` | `2` | Concurrent deliveries per process, 1 to 16 |
| `XOLO_WEBHOOKS_POLL_INTERVAL` | `1s` | Preparation and polling interval, 100 ms to 1 minute |
| `XOLO_WEBHOOKS_QUEUE_CAPACITY` | `10000` | Deliveries waiting or in flight on the instance |
| `XOLO_WEBHOOKS_SUBSCRIPTION_CAPACITY` | `1000` | Deliveries waiting or in flight for one subscription, at most the queue capacity |

### Subscriptions

| Method | Route | Answer |
|---|---|---|
| `GET` | `/v1/xolo/tenants/{tenantID}/webhooks` | `{"items":[…]}` |
| `GET` | `/v1/xolo/tenants/{tenantID}/webhooks/{subscriptionID}` | The subscription |
| `PUT` | `/v1/xolo/tenants/{tenantID}/webhooks/{subscriptionID}` | Creates or replaces it: `200` and the subscription |
| `DELETE` | `/v1/xolo/tenants/{tenantID}/webhooks/{subscriptionID}` | Deletes it with its deliveries: `204` |
| `POST` | `/v1/xolo/tenants/{tenantID}/webhooks/{subscriptionID}/reset` | `{"acknowledgeLoss":true}`: drops its deliveries and resumes at the end of the feed, `204` |
| `GET` | `/v1/xolo/tenants/{tenantID}/webhooks/{subscriptionID}/deliveries` | The latest 100 deliveries, without the event nor the response |

```json
{"destination":"https://hooks.example.com/xolo","events":["organization.updated.v1","organization.deleted.v1"],
 "enabled":true,"secrets":["whsec_…"]}
```

- A subscription delivers the events of **its tenant only**. Its identifier is a
  UUID chosen by the client and unique on the instance: it never moves to
  another tenant (`409 conflict`), and a subscription of another tenant answers
  `404 not_found`. A tenant holds at most 10 subscriptions
  (`409 webhook_capacity`).
- `events` is `["*"]` or a list of up to 20 distinct event types of the feed.
  `destination` must belong to an allowed origin.
- `secrets` holds one secret, or two distinct ones during a rotation: base64,
  optionally prefixed with `whsec_`, of 32 to 64 random bytes generated by the
  client. They are required at creation; omitting them on a later `PUT` keeps
  the stored ones. They are never returned: the subscription only shows
  `secretCount`. They are encrypted with `XOLO_SECRET_KEY` and bound to their
  tenant and subscription: back up that key with the database.
- A new subscription starts at the end of the feed: rebuild the consumer state
  first, then rely on notifications.
- `enabled: false` pauses the subscription without losing its position.
  Destination and secret changes apply to the deliveries already queued.
- Every subscription write is audited with the caller and the `X-Request-ID`,
  never with its secrets.

### Delivery

Each attempt is an HTTPS `POST` of the exact event of the feed, following
[Standard Webhooks](https://www.standardwebhooks.com):

| Header | Value |
|---|---|
| `Content-Type` | `application/cloudevents+json` |
| `webhook-id` | The `id` of the event, the same for every attempt |
| `webhook-timestamp` | Unix seconds of this attempt |
| `webhook-signature` | `v1,<base64 HMAC-SHA256 of "<id>.<timestamp>.<body>">` for each secret, separated by spaces |

A receiver verifies one of the signatures on the raw body before decoding it,
refuses a timestamp more than five minutes away, deduplicates on
`(source, id)` and answers a `2xx` once the event is durably accepted.

- **At least once, in no guaranteed order.** A retry, a crash after the
  receiver's answer or two replicas may repeat an event; use `sequence` to order
  and the unit `GET` to read the current state.
- A `2xx` acknowledges the delivery; its body is read up to 64 KiB and
  discarded. Redirects are not followed. Anything else is retried after 5, 10,
  20… seconds, at most one hour apart, for at most 12 attempts or 24 hours; the
  delivery then becomes `failed`. Reconcile through the feed.
- A delivery is leased for 30 seconds: a worker that stops mid-attempt, on any
  replica, leaves it to another one once the lease expires. A late result never
  overwrites a newer attempt.
- Deliveries are copies of their event: the retention of the feed never removes
  a queued delivery. Finished deliveries are kept seven days for diagnostics.
- The workers never take the feed lock: they read the feed of their tenant
  only, and the request path never waits for them.

### Destinations

Only the allowed origins are reachable. Every address the name resolves to is
checked when connecting, and the checked address is the one dialled, so a name
cannot be rebound in between. Link-local addresses — cloud metadata endpoints
included —, multicast, unspecified, shared and special-use ranges are always
refused; loopback and private addresses only with
`XOLO_WEBHOOKS_ALLOW_PRIVATE_NETWORKS=true`. Proxies of the environment are
ignored and certificates are always verified. Restrict the outgoing traffic of
the process with a firewall as well.

### States and recovery

| `state` | Meaning |
|---|---|
| `ready` | Up to date, or catching up |
| `backpressure` | The queue capacity is reached: preparation pauses at its position and resumes when deliveries finish. Only waiting and in-flight deliveries count, and a subscription can only fill its own share |
| `history_lost` | The [retention](#retention) removed events the subscription had not prepared yet, typically after a long pause. It never skips them silently: rebuild the consumer, then reset the subscription |

A suspended tenant receives nothing: its subscriptions pause and resume at
reactivation, the suspension included. A pause — disabled subscription or
suspended tenant — longer than the retention ends in `history_lost`, since
the purged events can no longer be told apart. Deleting a tenant deletes its
subscriptions and deliveries. The retention of the whole feed is the only
thing that can move a subscription to `history_lost`.

### Monitoring

| Metric | Meaning |
|---|---|
| `xolo_webhook_attempts_total` | Attempts of this process |
| `xolo_webhook_failures_total{reason}` | Failures of this process, by diagnostic |
| `xolo_webhook_queue{state}` | Deliveries of the database, by state |
| `xolo_webhook_lag_seconds{stage}` | Age of the oldest event not yet prepared (`materialization`) or delivered (`delivery`) |
| `xolo_webhook_history_lost`, `xolo_webhook_backpressure` | Subscriptions in that state |

The gauges describe the whole database and are sampled by every process: take
their maximum across replicas, not their sum. No label carries a tenant, a
destination or an event. Alert on a lasting lag, on failures and on any
`history_lost`.

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
| `GET`, `POST`, `PUT`, `DELETE` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles[/{key}]` | Custom roles, see [Business resources](#business-resources) |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles/builtin` | Builtin roles |
| `GET` | `/v1/xolo/tenants/{tenantID}/users` | `?provider=&subject=` for an exact lookup, otherwise `?search=&active=&page=&limit=` |
| `PUT` | `/v1/xolo/tenants/{tenantID}/users` | Idempotent upsert on `(provider, subject)`: `201` when created, `200` otherwise |
| `GET` | `/v1/xolo/tenants/{tenantID}/users/{userID}` | |
| `GET` | `/v1/xolo/ownership` | Effective write authority of each family |
| `GET` | `/v1/xolo/adoption/export` | Adoption export, NDJSON stream, see [Write authority, adoption and detachment](#write-authority-adoption-and-detachment) |

Users hang from the tenant because `(provider, subject)` is only unique within
one: the same person signing in on two tenants owns two distinct accounts.

## Business resources

Five families specific to Xolo follow the same contract as the common
families: unit reads with an `ETag`, paginated lists, conditional `PUT`s and
events. The manifest announces `business_resources`.

| Method | Route |
|---|---|
| `GET`, `GET`, `PUT` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles[/{key}]` |
| `GET`, `GET`, `PUT` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/applications[/{key}]` |
| `GET`, `GET`, `PUT` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/alerts[/{key}]` |
| `GET`, `GET`, `PUT` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/providers[/{key}]` |
| `GET`, `GET`, `PUT` | `/v1/xolo/tenants/{tenantID}/quotas[/{key}]` |
| `POST` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles` — creates a custom role under a key chosen by the server; answers `201` with `{key, representation, etag}` |
| `DELETE` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles/{key}` — deletes a custom role (`204`) |
| `GET` | `/v1/xolo/tenants/{tenantID}/organizations/{orgID}/roles/builtin` — the builtin roles, `{"items":[{"id","builtin_kind","name"}]}`, for `PUT …/members/{membershipID}/roles` |

Representations are snake_case and complete: every field is required, `null`
only where shown.

| Family | Representation |
|---|---|
| `custom_role` | `{"name","description","permissions":[…],"model_grants":[{"model_id","kind"}]}` |
| `application` | `{"name","description","active","role_ids":[…]}` |
| `quota` | `{"scope","scope_id","currency","daily_budget","monthly_budget","yearly_budget"}`, budgets in microcents or `null` |
| `alert` | `{"name","description","scope","owner_id","query","aggregation","window_seconds","comparator","threshold","for_seconds","enabled"}` |
| `provider` | `{"name","type","base_url","active","currency","cloud_tier","billing_mode","subscription_plan","retry_config","rate_limit_config"}`, the last three `null` or objects, plus the write-only `"api_key"` |

- **Keys.** A new resource is created under a canonical UUID chosen by the
  client. A resource created from the web UI keeps its local identifier, which
  reads and updates accept; it never creates one. Events and list items carry
  the key in `key.resource_id`, with `key.organization_id` except for quotas.
- **Parents.** Every reference is checked in the transaction of the write. A
  model grant designates a model of the organization (and, for an LLM model,
  a provider of the organization); `role_ids` are roles of the organization; a
  quota caps an organization of the tenant, a member of the tenant or an
  application of one of its organizations; the owner of an alert is an active
  member of the organization, required for a `personal` alert. A missing or
  foreign parent is `404 parent_not_found`; a resource of another organization
  or tenant is `404 not_found`, like a missing one.
- **Immutable fields.** The `scope` and `scope_id` of a quota and the `scope`
  and `owner_id` of an alert never change (`409 conflict`). A scope holds one
  quota: another key on the same scope is `409 conflict`.
- **Normalization.** Permissions, grants and role identifiers are sorted and
  deduplicated: a replay in another order is a no-op.
- **Runtime state stays out.** A quota `PUT` keeps the spend already recorded.
  The evaluation state of an alert is neither returned nor published; a change
  of the alert restarts it from `ok`, as an edit in the web UI does. Builtin
  roles are not part of the collection: writing one is `409 conflict`.
- **Provider keys are write-only.** `api_key` is required at creation and kept
  when omitted. It is encrypted with `XOLO_SECRET_KEY` and never appears in a
  response, a projection, an event nor in clear in the audit: a rotation
  changes no `ETag` and publishes no event, but the audit records the change
  of a fingerprint of the ciphertext.
- **Not deleted through the contract**, custom roles aside: deletions arrive
  with the resource lifecycle. Application tokens stay managed locally.
- The web UI writes publish their events as well. Migration `202610120001`
  projects the existing business resources without publishing any event;
  like the previous ones, it requires every server to be stopped.

Known limit: the cache of each process keeps a deactivated application's
tokens valid until its entries expire (`XOLO_STORAGE_DATABASE_CACHE_USERS_TTL`):
a write through this listener clears the cache of its own process only.

## Write authority, adoption and detachment

Each family of the common contract, and the webhook subscriptions, has a
**write authority**: the local instance (web UI, sign-in, invitations), the
control plane (this listener), or both.

| Variable | Default | Description |
|---|---|---|
| `XOLO_OWNERSHIP` | — | Comma-separated `family=owner` pairs. Families: `tenant`, `tenant_domain`, `organization`, `member`, `organization_membership`, `subscription`, and the business families `provider`, `custom_role`, `application`, `quota`, `alert`. Owners: `shared`, `local`, `control_plane`. Omitted families are `shared`; an unknown family or owner prevents startup |

```dotenv
XOLO_OWNERSHIP=tenant=control_plane,tenant_domain=control_plane,organization=control_plane,member=control_plane,organization_membership=control_plane,subscription=control_plane
```

- `shared`, the default, keeps the previous behavior: both write.
- `local` refuses the writes of this listener with `403 ownership_denied`.
- `control_plane` refuses the local writes: the web UI answers `403`, for full
  pages and htmx fragments alike. Invitations produce memberships: creating,
  revoking or accepting one belongs to `organization_membership`.

The policy governs the **public representation** of the resources, the one
`GET` returns. It is checked in the transaction of the write, on every
projection the write changes, cascades included: deleting a tenant whose
members belong to another authority is refused as a whole, and nothing is
removed nor published. The fields outside the contract stay local whatever the
policy: platform roles, the currency and quota sharing of an organization,
models, virtual models, middlewares and application tokens.
Application accounts and API tokens remain usable, and reads keep their
permissions. `GET /v1/xolo/ownership` returns the effective policy, and the
manifest announces `ownership`.

A write authority never grants a privilege: platform administrators stay
protected (`409 platform_admin_protected`) when the control plane owns the
members.

The policy is read at startup; it is not stored in the database. Stop
**every** server and worker before changing it, and restart every replica with
the same value: a replica still running an older policy keeps accepting the
writes that policy allows. Each process logs its effective policy at startup
(`write authority policy`). The offline operator commands (`xolo-migrate`,
`xolo-adoption`) hold database authority and check no policy.

### Managed members

With `member=control_plane`, a sign-in only resolves the members the control
plane declared:

- it links the proven identity to an existing member — its declared identity,
  then a verified email — without changing its projection;
- it never copies the email nor the display name of the identity provider: the
  declared profile stays;
- it creates no account, whatever `XOLO_HTTP_AUTHN_AUTO_CREATE_USERS` or a
  pending invitation says, except for the default administrators, which
  bootstrap the instance, and the application accounts. Any other identity is
  refused with `403`.

### Adoption export

`GET /v1/xolo/adoption/export`, like `xolo-adoption export`, streams the
inventory of the instance: every projection of the common and business
families of every tenant, suspended resources included, read on one consistent snapshot without
taking any lock. The format, `xolo-adoption/1`, is NDJSON
(`application/x-ndjson`):

```text
{"version":"xolo-adoption/1","contract":"0.1.0-draft.1","source":"urn:uuid:…","c0":"…","families":["tenant","tenant_domain","organization","member","organization_membership","provider","custom_role","application","quota","alert"]}
{"family":"tenant","key":{"tenant_id":"…"},"representation":{…},"etag":"W/\"42\""}
…
{"count":128,"complete":true,"sha256":"…"}
```

- Records follow the order of `families`, parents before children; `key`,
  `representation` and `etag` are those `GET` returns.
- `c0` is a cursor of `/v1/events` taken on the same snapshot: the events after
  it are exactly the changes the export misses.
- `sha256` is the lowercase hexadecimal SHA-256 of the exact bytes of every
  line before the last one, newlines included. It detects corruption and
  truncation, not a deliberate replacement: the transport and the file
  permissions establish provenance.
- An interrupted export lacks its last line, and verification rejects it.
- The export holds contact emails and declared identities: protect it like the
  database. It holds no undeclared sign-in link, session, token, secret nor
  audit entry. `verify` also accepts an export listing only the five common
  families, made before the business families.

`xolo-adoption verify -in <file>` (`-in -` reads the standard input) checks the
checksum, the count, the version and contract of the header, the syntax of the
keys, the statuses, duplicates and that every parent precedes its children,
keeping only keys in memory. It prints the source, `c0` and the count of each
family.

### Adopting an instance

1. Test a real platform administrator sign-in, then back up the database.
   Keep every family `shared`.
2. Export (`xolo-adoption export -out /secure/inventory.ndjson` with
   `XOLO_STORAGE_DATABASE_DSN`, or the route above), verify the file, then
   stage it on the control plane, keeping the UUIDs qualified by the feed
   `source`. Resolve slug, domain and email collisions on the control plane
   without changing any identifier.
3. Replay `/v1/events` from `c0` until caught up. A `410 cursor_expired` means
   starting again from a new export.
4. Stop every server and worker, catch up on the feed one last time, set
   `XOLO_OWNERSHIP` and restart every replica. Check `GET /v1/xolo/ownership`,
   the refusal of a local write and an administrator sign-in. No resource is
   rewritten.

### Detaching an instance

Detaching hands the instance back to its local administration.

1. Test a platform administrator sign-in that stays usable without the control
   plane, then back up the database.
2. Stop every server, worker and other database writer.
3. Run:

   ```sh
   XOLO_STORAGE_DATABASE_DSN=… xolo-adoption detach -writers-stopped -operator-access-verified
   ```

4. Remove the `control_plane` values from `XOLO_OWNERSHIP` and restart every
   replica, then check a sign-in and a local write.

The command refuses unless an active platform administrator of an active tenant
has a sign-in link. In one transaction, it deletes every webhook subscription,
its encrypted secrets and its pending deliveries, records an audit entry naming
the operator (`urn:xolo:operator:<uid>`) and prints how many subscriptions and
deliveries it removed. Resources, UUIDs, declared identities, sign-in links,
audit, the event feed and its source stay intact. A webhook already sent cannot
be recalled. The two flags attest the checks above: the command cannot stop
other processes.

`xolo-adoption` ships next to `xolo-server` in the releases and the container
images (`/usr/local/bin/xolo-adoption`). `export` and `verify` only read, and
no action migrates the schema: run the binary of the version that migrated the
database.

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
| `invalid_representation` | 400 | Common route: missing required field, `null`, or invalid member `identity` |
| `invalid_hostname` | 400 | Hostname not in lower case, with a port, an IP literal or an invalid label |
| `invalid_request` | 400 | `/v1/xolo`: malformed body, unknown field, invalid query parameter |
| `client_certificate_rejected` | 403 | Client certificate or URI is not authorized |
| `ownership_denied` | 403 | The ownership policy reserves the family to the local instance |
| `not_found` | 404 | Unknown resource or route, or a resource belonging to another tenant or organization |
| `parent_not_found` | 404 | The tenant, organization or member a resource hangs from does not exist in that scope |
| `method_not_allowed` | 405 | Known resource, wrong method |
| `unsupported_media_type` | 415 | Common route without `Content-Type: application/json` |
| `conflict` | 409 | Identifier or hostname owned by another tenant, slug already used, identity or email held by another member, or a business invariant |
| `last_owner` | 409 | The change would leave a tenant or an organization without an active owner |
| `platform_admin_protected` | 409 | The change targets a platform administrator |
| `webhook_capacity` | 409 | The tenant already holds the maximum number of webhook subscriptions |
| `cursor_expired` | 410 | List cursor older than 24 hours, or event cursor older than the retained events: rebuild |
| `precondition_failed` | 412 | `If-Match` does not designate the current revision |
| `unprocessable` | 422 | Well-formed value refused by the domain |
| `rate_limited` | 429 | Per-URI budget exceeded; retry after the `Retry-After` seconds |
| `internal_error` | 500 | Unexpected failure |

Messages are always built explicitly. Stack traces, SQL errors, file paths, TLS
details and secrets never reach the client: the full detail is logged
server-side.

## Invariants

- A write authority never grants a privilege: the ownership policy only
  decides who may write a family, and every other invariant still applies.
- Provisioning **never** grants or modifies platform-wide privileges. A user
  created through `PUT /v1/xolo/tenants/{tenantID}/users` or a member `PUT`
  receives exactly the `user` platform role, platform roles are never modified,
  and a platform administrator is never modified at all.
- An identity designates at most one account per tenant. A sign-in never merges
  accounts, never relinks a linked one and never attaches a platform
  administrator.
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
| `POST /v1/tenants/{tenantID}/organizations` `{slug, name, description, currency, active, owner}` | `PUT /v1/tenants/{tenantID}/organizations/{orgID}` `{slug, name, status}`; `description` and `currency` through `PATCH /v1/xolo/…/organizations/{orgID}`; the owner through `PUT …/organizations/{orgID}/members/{userID}` `{"role": "owner", "status": "active"}`, once declared with `PUT …/members/{userID}` |
| `PATCH /v1/tenants/{tenantID}/organizations/{orgID}` | `PATCH /v1/xolo/tenants/{tenantID}/organizations/{orgID}` (same body) |
| `DELETE /v1/tenants/{tenantID}/organizations/{orgID}` | Removed: `PUT …/organizations/{orgID}` with `"status": "suspended"` |
| `GET …/organizations/{orgID}/members[/{membershipID}]` | `GET /v1/xolo/…/organizations/{orgID}/members[/{membershipID}]` |
| `POST …/organizations/{orgID}/members` `{userId \| user, roleIds, builtinRoles}` | `PUT /v1/tenants/{tenantID}/organizations/{orgID}/members/{userID}` `{role, status}`; custom roles through `PUT /v1/xolo/…/members/{membershipID}/roles` |
| `PUT …/members/{membershipID}/roles` | `PUT /v1/xolo/…/members/{membershipID}/roles` (same body) |
| `DELETE …/members/{membershipID}` | Removed: `PUT …/organizations/{orgID}/members/{userID}` with `"status": "suspended"` |
| `…/organizations/{orgID}/roles[/{roleID}]` (all methods) | `/v1/xolo/…/organizations/{orgID}/roles[/{key}]`, with the changes below |
| `GET /v1/xolo/…/roles` (builtin and custom, camelCase `roleDTO`) | `GET /v1/xolo/…/roles` lists the custom roles as a page of projections; builtin roles through `GET /v1/xolo/…/roles/builtin` |
| `GET /v1/xolo/…/roles/{roleID}` (camelCase `roleDTO`) | Same route: the snake_case representation and its `ETag` |
| `POST /v1/xolo/…/roles` `{name, description?, permissions?, modelGrants?}` | Same route, the complete snake_case representation `{name, description, permissions, model_grants}`; `201` with `{key, representation, etag}` |
| `PUT /v1/xolo/…/roles/{roleID}` partial `{name?, description?, permissions?, modelGrants?}` | Same route, the complete representation, optional `If-Match`; creates the role under a UUID of your choice |
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
mutations, and the sign-ins that create, link or update an account, with that
account as actor; reads and ordinary web UI operations do not write it. A user
state includes its declared identity: unlike events, the audit table holds it.

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

- LLM models, virtual models, middlewares, application tokens and event
  settings. They all already have a `port.*Store`: exposing them means adding a
  handler file and its DTOs, with no architectural change.
- Per-certificate scopes.
- Deleting tenants, domains, organizations, memberships or business resources
  other than custom roles: suspend or disable them.
- No generated OpenAPI specification.

## User documentation

User-facing versions of this page live in
`docs/{en,fr,es}/administration/provisioning/provisioning.md`, e.g.
[`docs/fr/administration/provisioning/provisioning.md`](../../docs/fr/administration/provisioning/provisioning.md).
