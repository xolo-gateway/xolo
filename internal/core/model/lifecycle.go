package model

import "time"

// Deletion is a durable lifecycle checkpoint. Its ETag is the frozen resource
// version, independent of acknowledgements and worker diagnostics.
type Deletion struct {
	Family       string     `json:"resource_type"`
	TenantID     string     `json:"tenant_id"`
	ResourceID   string     `json:"resource_id"`
	ETag         string     `json:"etag"`
	DeletedAt    time.Time  `json:"deleted_at"`
	PurgeAfter   time.Time  `json:"purge_after"`
	ExportSHA256 string     `json:"export_sha256,omitempty"`
	ConfirmedAt  *time.Time `json:"confirmed_at,omitempty"`
	PurgedAt     *time.Time `json:"purged_at,omitempty"`
	Attempts     int        `json:"attempts"`
	Diagnostic   string     `json:"diagnostic,omitempty"`
}
