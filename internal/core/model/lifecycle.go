package model

import "time"

// LifecycleFamilies lists the families whose deletion is recorded before
// their purge: everything they hold is frozen in between.
var LifecycleFamilies = []string{FamilyTenant, FamilyOrganization, FamilyMember}

// Deletion records the deletion of a resource. From DeletedAt, the resource
// and everything it holds are frozen: no row of its scope may be written
// anymore. PurgeAfter is the earliest time it may be purged.
type Deletion struct {
	Family     string    `json:"resource_type"`
	TenantID   string    `json:"tenant_id"`
	ResourceID string    `json:"resource_id"`
	DeletedAt  time.Time `json:"deleted_at"`
	PurgeAfter time.Time `json:"purge_after"`
}
