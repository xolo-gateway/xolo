package service

import (
	"context"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// MemberLocale records the interface language a member chooses in Xolo.
type MemberLocale struct {
	transactions port.ProvisioningTransaction
}

func NewMemberLocale(transactions port.ProvisioningTransaction) *MemberLocale {
	return &MemberLocale{transactions: transactions}
}

// Set stores the locale of the member of tenantID, empty to clear it. A
// member of another tenant is not found and an inactive one is not allowed,
// whatever the caller checked before. Only the locale is
// written, in a provisioning transaction: its audit and the member event,
// correlated by a request ID Xolo generates, commit with it. That holds while
// the control plane owns members too. Choosing the stored locale again
// writes nothing and publishes nothing.
func (s *MemberLocale) Set(ctx context.Context, tenantID model.TenantID, userID model.UserID, locale string) error {
	locale = model.NormalizeLocale(locale)
	if !model.IsValidLocale(locale) {
		return errors.Wrap(port.ErrInvalid, "locale must be a canonical language tag")
	}
	ctx = model.WithActor(ctx, model.Actor{UserID: userID, RequestID: model.NewRequestID()})
	ctx = model.WithLocaleChange(ctx, userID)
	return s.transactions.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
		user, err := tx.GetUserByID(ctx, userID)
		if err != nil {
			return errors.WithStack(err)
		}
		if user.TenantID() != tenantID {
			return errors.WithStack(port.ErrNotFound)
		}
		if !user.Active() {
			return errors.Wrap(port.ErrNotAllowed, "an inactive member can not choose a locale")
		}
		if memberLocale(user) == locale {
			return nil
		}
		updated := model.CopyUser(user)
		setMemberLocale(updated, locale)
		return errors.WithStack(tx.SaveUser(ctx, updated))
	})
}
