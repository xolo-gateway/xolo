package component

import (
	"context"

	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

// OrgStatusActive and OrgStatusInactive are the values of the `status` query
// parameter backing the segmented filter.
const (
	OrgStatusActive   = "active"
	OrgStatusInactive = "inactive"
)

// orgFormValue returns the value a form field must show: what was posted when the
// form comes back with an error, the fallback otherwise. The creation form and
// the edit form share it, so a refused submit never drops the operator's input.
func orgFormValue(vmodel OrgFormVModel, field, fallback string) string {
	if vmodel.Submitted == nil {
		return fallback
	}
	if _, posted := vmodel.Submitted[field]; posted {
		return vmodel.Submitted.Get(field)
	}
	return fallback
}

// orgFormActive reports whether the edit form's "active" checkbox must be ticked.
//
// A GET — no submitted form — falls back on the stored state. A POST replay does
// not: a browser sends no key at all for an unchecked box, so an absent "active"
// means the operator unchecked it. Falling back to the stored value there would
// re-tick the box and silently undo the change the re-render exists to preserve.
func orgFormActive(vmodel OrgFormVModel, fallback bool) bool {
	if vmodel.Submitted == nil {
		return fallback
	}
	return vmodel.Submitted.Get("active") == "on"
}

// orgsSegments builds the filter segments of the organisation list.
//
// "En dépassement" is present but inert: selecting it would mean comparing every
// organisation's spending to its budget, and the console has no cross-org quota
// aggregate (cf. lot 7 du plan). Hiding it would quietly change the information
// architecture of the mockup, so it stays, disabled.
func orgsSegments(ctx context.Context, vmodel OrgsPageVModel) []common.Segment {
	href := func(status string) string {
		values := []string{}
		if status != "" {
			values = append(values, "status", status)
		}
		if vmodel.Search != "" {
			values = append(values, "q", vmodel.Search)
		}
		if len(values) == 0 {
			return common.BaseURLString(ctx, common.WithPath("/admin/orgs"))
		}
		return common.BaseURLString(ctx, common.WithPath("/admin/orgs"), common.WithValues(values...))
	}

	return []common.Segment{
		{Label: i18n.T(ctx, "admin.orgs.filter.all"), Href: href(""), Active: vmodel.Status == ""},
		{Label: i18n.T(ctx, "admin.orgs.filter.active"), Href: href(OrgStatusActive), Active: vmodel.Status == OrgStatusActive},
		{Label: i18n.T(ctx, "admin.orgs.filter.over_budget"), Disabled: true},
		{Label: i18n.T(ctx, "admin.orgs.filter.inactive"), Href: href(OrgStatusInactive), Active: vmodel.Status == OrgStatusInactive},
	}
}

// orgsSuccessMessage translates the redirect marker of a mutation into the
// sentence the banner shows.
func orgsSuccessMessage(ctx context.Context, success string) string {
	switch success {
	case "created":
		return i18n.T(ctx, "admin.orgs.success.created")
	case "saved":
		return i18n.T(ctx, "admin.orgs.success.saved")
	default:
		return i18n.T(ctx, "admin.orgs.success.default")
	}
}
