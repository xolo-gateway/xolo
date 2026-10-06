# UUID upgrade and offline migrations

Migration `202610020001` converts tenant, organization and user IDs to UUIDs.
It preserves existing UUIDs, rewrites relations and retains platform roles.
Other identifiers (tokens, models, graph nodes, etc.) keep their existing format.
Application IDs remain xids, including quota `scope_id` values for
`scope = 'application'` and `application_id` event attributes.

**Stop every old server, replica, worker and database writer before applying
this upgrade. Rolling upgrades are unsupported.** An old binary can still write
retired IDs into usage and quota records: those rows become orphaned and spend
can disappear from quota accounting. The migration lock serializes new migration
processes; it cannot stop old binaries. Startup logs an explicit warning.

Lock waits keep their existing behavior. With SQLite's default 5-second
busy timeout, 11 attempts and ten 100 ms pauses give an approximately 56-second
retry window under contention; a different busy timeout changes that duration.
PostgreSQL has no application-defined advisory-lock timeout: waiting ends when
the lock is acquired, the context is cancelled or a database/session timeout fires.

## Recommended procedure

1. Back up the database using your database's consistent backup procedure.
   Include SQLite WAL state; copying only an active `.sqlite` file is insufficient.
2. Rehearse the commands below on a restored copy.
3. Stop all writers on the live database and take a final backup.
4. Generate and review a recovery plan against that stopped database; resolve
   every diagnostic, then apply the exact saved plan.
5. Start only the new binaries and verify login, organizations, API tokens,
   alert queries and quota totals before reopening traffic.

The release archives and Docker image include `xolo-migrate`. From source,
`make build-migrate` produces `bin/migrate`; substitute that path below.
Only `XOLO_STORAGE_DATABASE_DSN` is required. The CLI does not load `.env` itself.
Use the existing SQLite file or a PostgreSQL DSN through this environment variable.

```bash
export XOLO_STORAGE_DATABASE_DSN=/data/data.sqlite
xolo-migrate diagnose
xolo-migrate plan -out recovery.json
# Review recovery.json and add any required explicit corrections.
xolo-migrate diagnose -plan recovery.json
xolo-migrate apply -plan recovery.json -writers-stopped
export XOLO_STORAGE_AUTO_MIGRATE=false
xolo-server
```

`diagnose` and `plan` use a read-only database snapshot and never migrate. A failed
diagnostic returns a nonzero exit code and an actionable JSON report. `plan`
still saves its artifact when the report has blocking issues, so you can resolve
them. The output file is created with permissions `0600`; an existing file is
never overwritten. Keep the mapping and artifact securely, and reuse them for retries.

`apply` executes the complete migration chain in one transaction, including its
migration markers. It revalidates the plan under the migration lock. A failure
rolls everything back. Reapplying the same successful plan is idempotent; an
artifact with different decisions is rejected after migration. The saved artifact
is also recorded in the database's recovery checkpoint.

For a fresh installation, or an upgrade needing no operator decisions, use
`xolo-migrate apply -writers-stopped` without a plan. Fresh installations do not
need `plan`, which expects the previous release's tenant/user/organization schema.

## Resolve diagnostics

| Case | Resolution |
| --- | --- |
| Legacy IDs, including non-xid values such as `org-acme` | Converted automatically. Keep the generated `ids` mapping; valid UUIDs must stay unchanged. |
| Emails differing only by case or surrounding spaces in one tenant | Preserved exactly, including case and spaces. This migration does not normalize emails or merge accounts. |
| Exact EventQL matches on `user`, `org`, `actor_id`, `user_id`, `org_id` and other recognized ID attributes | Rewritten automatically using the matching ID family. Indexed selectors support `user`/`org`; `user_id`/`actor_id` are pipeline attributes. Historical event ID attributes are rewritten too. |
| Graph references in recognized ID fields or exact string `value` fields | Rewritten automatically; graph topology IDs and edges remain unchanged. |
| EventQL regex on a recognized ID selector or attribute containing an old ID, opaque graph script/configuration or ambiguous ID | The report names the table, row, column and, for graphs, JSON path. Add a `serialized_overrides` entry as described below. |
| Invalid query or JSON | Repair it with a valid serialized override. Unknown EventQL selectors such as `{user_id="..."}` must become a supported selector or attribute filter. |
| Incomplete, stale, duplicate or invalid UUID mapping | Regenerate an unapplied plan from the final stopped database and review it again. Never edit an already applied mapping. |
| Empty primary ID or orphaned relation | Repair source data on a backup-tested copy before replanning. Diagnostics never discard affected rows. |

Line filters (`|=`, `|~`, `!=`, `!~`) search `events.message`, which the migration
preserves unchanged; they are therefore neither rewritten nor reported.

Event attributes are rewritten only under recognized ID keys: `user`, `user_id`,
`actor_id`, `owner_id`, `member_user_id`, `created_by_user_id`, `org`, `org_id`,
`organization_id` and `tenant_id`. Other keys, including plugin-defined keys such
as `tenant_user`, keep their original values. Before migration, `diagnose` and
`plan` report possible legacy IDs in those values (including embedded IDs) in
`notices`, grouped by key with distinct-value counts and up to five sorted
examples. These notices are informational: they do not block `apply`; only
`issues` block it. Automatic migration and `apply` also log them at INFO level.
For example:

```text
unmapped event attribute: events.attributes [key "tenant_user"]: 1 distinct values; examples: "user-alice"
```

Review any alert filters using these custom attributes. If appropriate, use an
`events.attributes` serialized override to correct a value, or identical
`before`/`after` values to acknowledge literal text. An explicit override
suppresses the notice for that event; it still undergoes the usual JSON and
stale-value validation. Recovery plans remain at version 2.

Normal user deletion leaves historical references: owners of organization
alerts, invitation creators, personal plugin-secret scopes (`~:<userID>`) and
`mcp-bridge` OAuth keys (`oauth:<userID>`). Migration rewrites these values when
the user has an ID mapping and preserves them unchanged when the user no longer
exists. Empty or NULL legacy alert scopes mean organization scope. Personal-alert
owners and other required relationships still block migration when orphaned.
Organization scopes of plugin secrets must still resolve to an organization.
Events, usage records and quota usage retain their existing historical policy.
No rows or encrypted secret values are discarded, and any reference to a mapped
old ID remaining in these relational locations still fails verification.

Relational and mapping diagnostics are grouped by table/column, reference scope
and issue category. Each group gives the total number of distinct offending
values, up to five sorted, quoted examples, and the number omitted. For example:

```text
orphan: quota.scope_id [references users; scope = 'user']: 7 distinct values; examples: "missing-0", "missing-1", "missing-2", "missing-3", "missing-4"; 2 omitted
missing mapping key: users.id: 1 distinct values; examples: "user-alice"
invalid UUID: users.id: 1 distinct values; examples: "user-alice" -> "bad-uuid"
```

Unexpected mapping keys are also named. Duplicate-target diagnostics identify
the target UUID and its source IDs, including both sources of a two-ID collision.
Historical deleted-user references described above do not produce orphan issues.

Plans use **version 2** and contain only `ids` mappings and optional
`serialized_overrides`. Version 1 plans are rejected: regenerate with
`xolo-migrate plan -out recovery-v2.json` against the stopped, pre-migration
database and review every correction again. Do not change the version number by
hand. Unknown fields are rejected rather than silently ignored.

Emails, provider/subject identities and all platform and membership role
assignments are preserved, including multiple builtin roles. Domain management,
new role/status concepts and publication counters belong to a later migration.

Mappings are loaded into an indexed temporary table in batches. Each declared
relational reference is rewritten by one SQL operation; serialized fields are
read in pages of 1,000 rows and updated in batches. All batches remain inside the
same atomic transaction. Graph scans reuse a byte-based substring matcher built once
per serialized scan from changed IDs; opaque references containing punctuation or
overlapping IDs still require explicit overrides. The migration lock is used only for migrations;
ordinary store operations retain their existing transactions and configured cache.

For a serialized correction, add the following array to the existing plan. Obtain
the actual new UUID from `ids.users` (or the appropriate family):

```json
"serialized_overrides": [{
  "table": "alerts",
  "id": "existing-alert-id",
  "column": "query",
  "before": "{user=~\"old-user-id\"}",
  "after": "{user=\"UUID-FROM-THE-PLAN\"}"
}]
```

`before` must exactly match the stored field. A concurrent edit makes the plan
stale and blocks application. `after` replaces the whole query or JSON field, so
include every intended change and preserve unrelated configuration. Equal
`before`/`after` values explicitly acknowledge intentionally literal text. Allowed
targets are alert queries, virtual/personal model and middleware graphs, and
historical event attributes. Unknown targets and duplicate overrides are rejected.

## Startup policy and recovery

`XOLO_STORAGE_AUTO_MIGRATE` defaults to `true`, preserving automatic startup
migration. It does **not** make a rolling upgrade safe. With `false`, both startup
and later store access only check migration history: pending or unknown migrations
refuse startup with a diagnostic, without applying schema changes.

If an automatic upgrade is blocked, keep writers stopped and use the CLI to
produce, correct, diagnose and apply a plan. A failed transaction leaves the old
database intact. After a successful UUID conversion, returning to an old binary
requires restoring the pre-upgrade backup; there is no reverse UUID migration.
External consumers that store Xolo IDs must update their references using the
saved mapping. This change preserves the provisioning routes; their replacement
belongs to a separate release change.
