# Unreleased — UUID identity migration

Tenant, organization and user IDs become UUIDs in migration `202610020001`.
Existing UUIDs and relations are preserved. External consumers retaining Xolo
IDs must update their references using the recovery mapping.

**A stop-the-world upgrade is mandatory. Stop ALL old replicas, servers, workers
and other writers before migration; rolling upgrades are unsupported.** Old
binaries can write retired IDs into usage and quota tables after migration,
creating orphaned rows and unaccounted spending. The migration lock cannot stop
those binaries. Back up the database and rehearse the upgrade first.

`xolo-migrate diagnose`, `plan` and `apply` provide an offline recovery path for
email collisions, serialized references and other data issues. The migration
binary ships in release archives and the server Docker image. Set
`XOLO_STORAGE_AUTO_MIGRATE=false` after applying migrations manually; startup then
checks compatibility without changing the schema. Automatic migration remains
enabled by default. Downgrading after conversion requires the pre-upgrade backup.

Upgrade instructions:
[English](docs/en/administration/installation/uuid-migration.md),
[Français](docs/fr/administration/installation/uuid-migration.md),
[Español](docs/es/administration/installation/uuid-migration.md).
