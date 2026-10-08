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
