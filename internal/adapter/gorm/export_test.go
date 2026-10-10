package gorm

// LockProvisioningFeed exposes the publication lock to the external tests,
// which assert that the session registry never waits on it.
var LockProvisioningFeed = lockProvisioningFeed

// SetInventoryBatchSize shrinks the export pages so that tests cross page
// boundaries; it returns the restoring function.
func SetInventoryBatchSize(n int) func() {
	previous := inventoryBatchSize
	inventoryBatchSize = n
	return func() { inventoryBatchSize = previous }
}

// MigrateBusinessProjections exposes the business backfill to the tests.
var MigrateBusinessProjections = migrateBusinessProjections

// MigrateTeams exposes the teams migration to the tests.
var MigrateTeams = migrateTeams

// LegacyRoleModel is the model grant a role carried before teams.
type LegacyRoleModel = legacyRoleModel
