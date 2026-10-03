package service

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
)

// Common representations include the optional, separately discovered identity
// declaration. Application settings and platform permissions remain independent.
type CommonResource struct {
	Slug, Name string
	Status     model.Status
}
type CommonMember struct {
	Identity           *model.Identity
	Email, DisplayName string
	TenantRole         model.TenantRole
	Status             model.Status
}
type CommonMembership struct {
	Role   model.MembershipRole
	Status model.Status
}

func validCommonText(s string, min, max int) bool {
	if !utf8.ValidString(s) || len(s) < min || len(s) > max {
		return false
	}
	for _, r := range s {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
func normalizeCommonResource(p CommonResource) (CommonResource, error) {
	p.Slug = strings.ToLower(strings.TrimSpace(p.Slug))
	p.Name = strings.TrimSpace(p.Name)
	p.Status = model.Status(strings.TrimSpace(string(p.Status)))
	if !model.IsValidSlug(p.Slug) || !validCommonText(p.Name, 1, 200) || !p.Status.Valid() {
		return p, port.ErrInvalid
	}
	return p, nil
}
func commonParent(ctx context.Context, tx port.ProvisioningTx, id model.TenantID) error {
	_, err := tx.GetTenantByID(ctx, id)
	if errors.Is(err, port.ErrNotFound) {
		return port.ErrParentNotFound
	}
	return err
}
func (s *ProvisioningService) PutCommonTenant(ctx context.Context, id model.TenantID, p CommonResource) (CommonResource, error) {
	ctx = model.WithCommonPUT(ctx)
	p, err := normalizeCommonResource(p)
	if err != nil {
		return p, err
	}
	if _, err = model.ParseTenantID(string(id)); err != nil {
		return p, port.ErrInvalid
	}
	err = s.transactions.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
		old, err := tx.GetTenantByID(ctx, id)
		if errors.Is(err, port.ErrNotFound) {
			if !s.multiTenant {
				return port.ErrNotAllowed
			}
			t := model.NewTenant(p.Slug, p.Name, "")
			t.SetID(id)
			return tx.CreateTenant(ctx, model.UpdateTenant(t, model.WithTenantActive(p.Status == model.StatusActive)))
		}
		if err != nil {
			return err
		}
		if old.Slug() == p.Slug && old.Name() == p.Name && model.DeclaredStatus(old.Active()) == p.Status {
			return nil
		}
		return tx.SaveTenant(ctx, model.UpdateTenant(old, model.WithTenantSlug(p.Slug), model.WithTenantName(p.Name), model.WithTenantActive(p.Status == model.StatusActive)))
	})
	return p, err
}
func (s *ProvisioningService) PutCommonOrganization(ctx context.Context, tid model.TenantID, id model.OrgID, p CommonResource) (CommonResource, error) {
	ctx = model.WithCommonPUT(ctx)
	p, err := normalizeCommonResource(p)
	if err != nil {
		return p, err
	}
	if _, err = model.ParseTenantID(string(tid)); err != nil {
		return p, port.ErrInvalid
	}
	if _, err = model.ParseOrgID(string(id)); err != nil {
		return p, port.ErrInvalid
	}
	err = s.transactions.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
		if err := commonParent(ctx, tx, tid); err != nil {
			return err
		}
		old, err := tx.GetOrgByID(ctx, id)
		if errors.Is(err, port.ErrNotFound) {
			o := model.NewOrganization(tid, p.Slug, p.Name, "")
			o.SetID(id)
			return tx.CreateOrg(ctx, model.UpdateOrganization(o, model.WithOrgActive(p.Status == model.StatusActive)))
		}
		if err != nil {
			return err
		}
		if old.TenantID() != tid {
			return port.ErrAlreadyExists
		}
		if old.Slug() == p.Slug && old.Name() == p.Name && model.DeclaredStatus(old.Active()) == p.Status {
			return nil
		}
		return tx.SaveOrg(ctx, model.UpdateOrganization(old, model.WithOrgSlug(p.Slug), model.WithOrgName(p.Name), model.WithOrgActive(p.Status == model.StatusActive)))
	})
	return p, err
}
func (s *ProvisioningService) PutCommonMember(ctx context.Context, tid model.TenantID, id model.UserID, p CommonMember) (CommonMember, error) {
	ctx = model.WithCommonPUT(ctx)
	if err := ValidateIdentity(p.Identity); err != nil {
		return p, err
	}
	p.Email = model.NormalizeEmail(p.Email)
	p.DisplayName = strings.TrimSpace(p.DisplayName)
	p.TenantRole = model.TenantRole(strings.TrimSpace(string(p.TenantRole)))
	if !validCommonText(p.Email, 1, 320) || !strings.Contains(p.Email, "@") || !validCommonText(p.DisplayName, 0, 200) || !p.TenantRole.Valid() || !p.Status.Valid() {
		return p, port.ErrInvalid
	}
	if _, err := model.ParseTenantID(string(tid)); err != nil {
		return p, port.ErrInvalid
	}
	if _, err := model.ParseUserID(string(id)); err != nil {
		return p, port.ErrInvalid
	}
	err := s.transactions.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
		if err := commonParent(ctx, tx, tid); err != nil {
			return err
		}
		old, err := tx.GetUserByID(ctx, id)
		if err != nil && !errors.Is(err, port.ErrNotFound) {
			return err
		}
		if old != nil && old.TenantID() != tid {
			return port.ErrAlreadyExists
		}
		// A client must not acquire the default-admin identity through an email edit.
		for _, email := range s.reservedEmails {
			if email == p.Email && (old == nil || model.NormalizeEmail(old.Email()) != p.Email) {
				return port.ErrNotAllowed
			}
		}
		if old != nil && identitiesEqual(old.DeclaredIdentity(), p.Identity) && (p.Identity != nil || old.Provider() == "") && old.Email() == p.Email && old.DisplayName() == p.DisplayName && old.TenantRole() == p.TenantRole && model.DeclaredStatus(old.Active()) == p.Status {
			return nil
		}
		var u *model.BaseUser
		if old == nil {
			u = model.NewUser(tid, "", "", p.Email, p.DisplayName, p.Status == model.StatusActive)
			u.SetID(id)
		} else {
			u = model.CopyUser(old)
			u.SetEmail(p.Email)
			u.SetDisplayName(p.DisplayName)
			u.SetActive(p.Status == model.StatusActive)
		}
		if p.Identity == nil {
			u.SetIdentity("", "")
		}
		u.SetDeclaredIdentity(p.Identity)
		u.SetTenantRole(p.TenantRole)
		return tx.SaveUser(ctx, u)
	})
	return p, err
}
func (s *ProvisioningService) PutCommonDomain(ctx context.Context, tid model.TenantID, hostname string, status model.Status) (model.Status, error) {
	ctx = model.WithCommonPUT(ctx)
	status = model.Status(strings.TrimSpace(string(status)))
	if !status.Valid() {
		return status, port.ErrInvalid
	}
	if _, err := model.ParseTenantID(string(tid)); err != nil {
		return status, port.ErrInvalid
	}
	host, err := model.NormalizeHostname(hostname)
	if err != nil {
		return status, port.ErrInvalidHostname
	}
	err = s.transactions.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
		if err := commonParent(ctx, tx, tid); err != nil {
			return err
		}
		old, err := tx.GetDomain(ctx, host)
		if err != nil && !errors.Is(err, port.ErrNotFound) {
			return err
		}
		if err == nil {
			if old.TenantID != tid {
				return port.ErrAlreadyExists
			}
			if old.Status == status {
				return nil
			}
		}
		return tx.SaveDomain(ctx, model.Domain{Hostname: host, TenantID: tid, Status: status})
	})
	return status, err
}
func (s *ProvisioningService) PutCommonMembership(ctx context.Context, tid model.TenantID, oid model.OrgID, uid model.UserID, p CommonMembership) (CommonMembership, error) {
	ctx = model.WithCommonPUT(ctx)
	p.Role = model.MembershipRole(strings.TrimSpace(string(p.Role)))
	p.Status = model.Status(strings.TrimSpace(string(p.Status)))
	if !p.Role.Valid() || !p.Status.Valid() {
		return p, port.ErrInvalid
	}
	if _, err := model.ParseTenantID(string(tid)); err != nil {
		return p, port.ErrInvalid
	}
	if _, err := model.ParseOrgID(string(oid)); err != nil {
		return p, port.ErrInvalid
	}
	if _, err := model.ParseUserID(string(uid)); err != nil {
		return p, port.ErrInvalid
	}
	err := s.transactions.WithProvisioningTransaction(ctx, func(tx port.ProvisioningTx) error {
		if err := commonParent(ctx, tx, tid); err != nil {
			return err
		}
		org, err := tx.GetOrgByID(ctx, oid)
		if errors.Is(err, port.ErrNotFound) {
			return port.ErrParentNotFound
		}
		if err != nil {
			return err
		}
		if org.TenantID() != tid {
			return port.ErrParentNotFound
		}
		user, err := tx.GetUserByID(ctx, uid)
		if errors.Is(err, port.ErrNotFound) {
			return port.ErrParentNotFound
		}
		if err != nil {
			return err
		}
		if user.TenantID() != tid {
			return port.ErrParentNotFound
		}
		old, err := tx.GetUserOrgMembership(ctx, uid, oid)
		if err != nil && !errors.Is(err, port.ErrNotFound) {
			return err
		}
		if err == nil && old.CommonRole() == p.Role && old.Status() == p.Status {
			return nil
		}
		return tx.SetCommonMembership(ctx, oid, uid, p.Role, p.Status)
	})
	return p, err
}

// Identity values are exact: HTTPS issuer without userinfo/query/fragment and
// a nonempty UTF-8 subject, at most 255 bytes, without control characters.
func ValidateIdentity(v *model.Identity) error {
	if v == nil {
		return nil
	}
	if !v.Valid() {
		return port.ErrInvalid
	}
	return nil
}
func identitiesEqual(a, b *model.Identity) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
