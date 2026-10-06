# Unreleased — UUID identity migration

Tenant, organization and user IDs become UUIDs in migration `202610020001`.
Existing UUIDs, relations, emails, provider identities and every role assignment
are preserved. Emails are not normalized and membership roles are not collapsed. External consumers retaining Xolo
IDs must update their references using the recovery mapping.

**A stop-the-world upgrade is mandatory. Stop ALL old replicas, servers, workers
and other writers before migration; rolling upgrades are unsupported.** Old
binaries can write retired IDs into usage and quota tables after migration,
creating orphaned rows and unaccounted spending. The migration lock cannot stop
those binaries. Back up the database and rehearse the upgrade first.

`xolo-migrate diagnose`, `plan` and `apply` provide an offline recovery path for
serialized references and other migration data issues. The migration
binary ships in release archives and the server Docker image. Set
`XOLO_STORAGE_AUTO_MIGRATE=false` after applying migrations manually; startup then
checks compatibility without changing the schema. Automatic migration remains
enabled by default. Downgrading after conversion requires the pre-upgrade backup.

Upgrade instructions:
[English](docs/en/administration/installation/uuid-migration.md),
[Français](docs/fr/administration/installation/uuid-migration.md),
[Español](docs/es/administration/installation/uuid-migration.md).

Recovery plans now use version 2: UUID mappings and explicit serialized corrections
only. Regenerate version 1 plans against the stopped pre-migration database and
review them; changing their version number is unsupported. Conversion uses an
indexed temporary mapping table, one SQL operation per relational reference and
1,000-row pages for serialized fields, in a single atomic transaction. Ordinary
writes retain their existing transactions and configured user cache. Publication
counters, domain management and new role/status concepts are deferred to a later
migration.
