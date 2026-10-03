package port

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// IdentityLinkStore is available only inside a provisioning transaction.
type IdentityLinkStore interface {
	GetUserByDeclaredIdentity(context.Context, model.TenantID, model.Identity) (model.User, error)
	GetUserByEmail(context.Context, model.TenantID, string) (model.User, error)
	SaveAuthenticatedUser(context.Context, model.User) error
}
