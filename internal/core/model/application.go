package model

import (
	"time"

	"github.com/rs/xid"
)

type ApplicationID string

func NewApplicationID() ApplicationID {
	return ApplicationID(xid.New().String())
}

// ApplicationProvider is the identity provider name used for the shadow user
// backing an application's auth tokens. Its subject is the ApplicationID, which
// is how permission resolution maps a request back to the application and its
// org roles.
const ApplicationProvider = "application"

type Application interface {
	WithID[ApplicationID]

	OrgID() OrgID
	Name() string
	Description() string
	Active() bool
	CreatedAt() time.Time
	UpdatedAt() time.Time
}

type BaseApplication struct {
	id          ApplicationID
	orgID       OrgID
	name        string
	description string
	active      bool
	createdAt   time.Time
	updatedAt   time.Time
}

func (a *BaseApplication) ID() ApplicationID    { return a.id }
func (a *BaseApplication) OrgID() OrgID         { return a.orgID }
func (a *BaseApplication) Name() string         { return a.name }
func (a *BaseApplication) Description() string  { return a.description }
func (a *BaseApplication) Active() bool         { return a.active }
func (a *BaseApplication) CreatedAt() time.Time { return a.createdAt }
func (a *BaseApplication) UpdatedAt() time.Time { return a.updatedAt }

var _ Application = &BaseApplication{}

type ApplicationOption func(*BaseApplication)

func WithApplicationName(name string) ApplicationOption {
	return func(a *BaseApplication) { a.name = name }
}

func WithApplicationDescription(desc string) ApplicationOption {
	return func(a *BaseApplication) { a.description = desc }
}

func WithApplicationActive(active bool) ApplicationOption {
	return func(a *BaseApplication) { a.active = active }
}

func UpdateApplication(app Application, opts ...ApplicationOption) *BaseApplication {
	b := &BaseApplication{
		id:          app.ID(),
		orgID:       app.OrgID(),
		name:        app.Name(),
		description: app.Description(),
		active:      app.Active(),
		createdAt:   app.CreatedAt(),
		updatedAt:   time.Now(),
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

func NewApplication(orgID OrgID, name, description string, active bool) *BaseApplication {
	return &BaseApplication{
		id:          NewApplicationID(),
		orgID:       orgID,
		name:        name,
		description: description,
		active:      active,
		createdAt:   time.Now(),
		updatedAt:   time.Now(),
	}
}

// SetID gives the application an identifier chosen by the provisioning client.
func (a *BaseApplication) SetID(id ApplicationID) { a.id = id }

func (a *BaseApplication) SetName(name string)        { a.name = name }
func (a *BaseApplication) SetDescription(desc string) { a.description = desc }
func (a *BaseApplication) SetActive(active bool)      { a.active = active }
