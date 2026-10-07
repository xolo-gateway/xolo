package gorm

// LockProvisioningFeed exposes the publication lock to the external tests,
// which assert that the session registry never waits on it.
var LockProvisioningFeed = lockProvisioningFeed
