package component

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/port"
	"github.com/xolo-gateway/xolo/internal/core/rbac"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
)

func displayOwner(users map[model.UserID]model.User, apps map[model.ApplicationID]model.Application, rec model.UsageRecord) string {
	// Check if this is an Application record (has non-empty ApplicationID)
	appID := rec.ApplicationID()
	if appID != "" {
		if app, ok := apps[appID]; ok {
			return app.Name()
		}
		return string(appID)
	}
	// Otherwise it's a User record
	uid := rec.UserID()
	if u, ok := users[uid]; ok {
		return u.DisplayName()
	}
	return string(uid)
}

// formatCostRate formats a pricing rate stored as microcents per 1K tokens,
// displaying it as dollars per million tokens (industry standard).
func formatCostRate(v int64, currency string) string {
	return fmt.Sprintf("%.4f%s/1M", float64(v)/1_000, common.CurrencySymbol(currency))
}

// formatCostRateBare formats the same rate as formatCostRate without the
// unit suffix, for a table whose header already states "par million de tokens,
// en USD" once — repeating it in every cell is what makes a dense table
// unreadable.
func formatCostRateBare(v int64) string {
	return fmt.Sprintf("%.4f", float64(v)/1_000)
}

// formatTokenWindow renders a context or output window in thousands of tokens,
// the unit these limits are always quoted in. Zero means "not declared", not
// "zero tokens", so it renders as an em dash.
func formatTokenWindow(v int64) string {
	if v <= 0 {
		return "—"
	}
	if v < 1_000 {
		return fmt.Sprintf("%d", v)
	}
	return fmt.Sprintf("%d k", v/1_000)
}

func fmtInt(v int64) string {
	if v == 0 {
		return ""
	}
	return fmt.Sprintf("%d", v)
}

func formatCostField(v int64) string {
	// v is microcents/1K tokens; return dollars per 1M tokens for the form field
	return fmt.Sprintf("%.6f", float64(v)/1_000)
}

// ExtraBodyRow is a single key/value entry of a model's ExtraBody, rendered as a
// row in the extra-body editor.
type ExtraBodyRow struct {
	Key   string
	Value string
}

// extraBodyRows flattens a model's ExtraBody map into sorted key/value rows for
// pre-filling the editor. Values are rendered back to the textual form the
// editor expects (see extraBodyValueString): booleans as "true"/"false",
// whole numbers without a trailing ".0", everything else verbatim.
func extraBodyRows(m model.LLMModel) []ExtraBodyRow {
	if m == nil {
		return nil
	}
	eb := m.ExtraBody()
	if len(eb) == 0 {
		return nil
	}
	keys := make([]string, 0, len(eb))
	for k := range eb {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	rows := make([]ExtraBodyRow, 0, len(eb))
	for _, k := range keys {
		rows = append(rows, ExtraBodyRow{Key: k, Value: extraBodyValueString(eb[k])})
	}
	return rows
}

// extraBodyValueString renders a decoded extra-body value as the text a user
// would type. It mirrors the type inference done on submission so that a
// save → reload round-trip is stable.
func extraBodyValueString(v any) string {
	switch t := v.(type) {
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		if t == math.Trunc(t) && !math.IsInf(t, 0) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case json.Number:
		return t.String()
	case string:
		return t
	default:
		// Non-scalar values (nested objects/arrays) cannot be edited as plain
		// key/value; surface them as compact JSON so they remain visible.
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// formatActiveParamsBillions converts raw param count to billions for display in the form.
func formatActiveParamsBillions(v int64) string {
	if v <= 0 {
		return ""
	}
	return strconv.FormatFloat(float64(v)/1e9, 'f', -1, 64)
}

func formatBudgetField(v int64) string {
	// v is microcents; convert to the display unit (e.g. EUR/USD) with full precision.
	// Use 'f' format with prec=-1 so strconv uses the minimum number of digits needed
	// to represent the value exactly, avoiding both truncation ("0.00" for 100 µ¢)
	// and unnecessary trailing zeros ("100.000000" for 100_000_000 µ¢).
	return strconv.FormatFloat(float64(v)/1_000_000, 'f', -1, 64)
}

// durationValue returns the numeric component of d in its natural unit (minutes,
// seconds, or milliseconds). Returns "" for a zero duration.
func durationValue(d time.Duration) string {
	if d == 0 {
		return ""
	}
	if d%time.Minute == 0 {
		return fmt.Sprintf("%d", int64(d/time.Minute))
	}
	if d%time.Second == 0 {
		return fmt.Sprintf("%d", int64(d/time.Second))
	}
	return fmt.Sprintf("%d", int64(d/time.Millisecond))
}

// durationUnit returns "min", "s", or "ms" depending on the most coarse
// whole unit that can represent d. Returns "s" for a zero duration.
func durationUnit(d time.Duration) string {
	if d != 0 && d%time.Minute == 0 {
		return "min"
	}
	if d != 0 && d%time.Second == 0 {
		return "s"
	}
	if d != 0 {
		return "ms"
	}
	return "s"
}

// budgetScope names one of the three ceilings of a quota.
type budgetScope int

const (
	budgetScopeDaily budgetScope = iota
	budgetScopeMonthly
	budgetScopeYearly
)

// budgetFieldValue returns the value of a ceiling as the form should show it, or
// an empty string when the ceiling is unset — which the domain reads as
// "unlimited".
func budgetFieldValue(vmodel QuotaPageVModel, scope budgetScope) string {
	if vmodel.Quota == nil {
		return ""
	}

	var budget *int64
	switch scope {
	case budgetScopeDaily:
		budget = vmodel.Quota.DailyBudget()
	case budgetScopeMonthly:
		budget = vmodel.Quota.MonthlyBudget()
	case budgetScopeYearly:
		budget = vmodel.Quota.YearlyBudget()
	}

	if budget == nil {
		return ""
	}
	return formatBudgetField(*budget)
}

// budgetInputValue resolves the value to display in a budget input.
//
// The Submitted map, when populated by quotaFormSubmitted, carries only
// fields the operator actually POSTed: a present key holds whatever the
// operator typed (including the empty string for "I cleared this field
// on purpose"), and an absent key means "I did not touch this field".
//
// On a re-render after a rejected submit, the operator typed at least one
// bad value; the fields they did not touch must keep their stored budget
// so the next save does not silently wipe them out — which was the
// silent-data-loss regression Conclave review #2 caught (issue #88). The
// implementation falls back to budgetFieldValue when the key is absent;
// a present key (including the empty string) wins so the re-render echoes
// the operator's input verbatim.
//
// On a fresh GET vmodel.Submitted is nil and budgetFieldValue returns the
// stored value or "" for an unset ceiling — the unlimited default.
func budgetInputValue(vmodel QuotaPageVModel, field string, scope budgetScope) string {
	if vmodel.Submitted != nil {
		if v, ok := vmodel.Submitted[field]; ok {
			return v
		}
	}
	return budgetFieldValue(vmodel, scope)
}

// quotaFieldError returns the field-level error message for a budget input,
// or "" if the field parsed cleanly. The empty default keeps the templating
// branch (HasError, form.Message) a single conditional.
func quotaFieldError(fieldErrors map[string]string, field string) string {
	if fieldErrors == nil {
		return ""
	}
	return fieldErrors[field]
}

// quotaFormErrorSummary builds the top-of-form alert description: one line
// per offending field, prefixed with its label. The alert itself is rendered
// by the template; this helper only formats the prose so the error copy
// stays in one place and tests can pin it.
func quotaFormErrorSummary(fieldErrors map[string]string) string {
	if len(fieldErrors) == 0 {
		return ""
	}
	labels := map[string]string{
		"daily_budget":   "Journalier",
		"monthly_budget": "Mensuel",
		"yearly_budget":  "Annuel",
	}
	// Stable order matches the form layout (daily → monthly → yearly), so
	// the alert reads top-to-bottom the same way the inputs do.
	order := []string{"daily_budget", "monthly_budget", "yearly_budget"}
	var lines []string
	for _, f := range order {
		if msg, ok := fieldErrors[f]; ok {
			lines = append(lines, fmt.Sprintf("%s : %s", labels[f], msg))
		}
	}
	return strings.Join(lines, "\n")
}

// cachedTokensNote states the share of prompt tokens served from the provider's
// cache — the figure that explains a token count far above the billed cost.
func cachedTokensNote(agg *port.UsageAggregate) string {
	if agg == nil || agg.CachedTokens <= 0 || agg.PromptTokens <= 0 {
		return ""
	}
	return fmt.Sprintf("Dont %s en cache (%.0f %% des tokens prompt)",
		common.FormatCount(agg.CachedTokens),
		float64(agg.CachedTokens)/float64(agg.PromptTokens)*100,
	)
}

// totalCostValue renders the total cost, or an em dash when nothing was billed —
// a zero would read as a measured value rather than as an absence.
func totalCostValue(vmodel OrgUsagePageVModel) string {
	if vmodel.Aggregate == nil || vmodel.Aggregate.TotalCost == 0 {
		return "—"
	}
	return common.FormatCostCompact(vmodel.Aggregate.TotalCost, vmodel.Aggregate.Currency)
}

// budgetRuleClass returns the coloured left rule of the budget banner, on the
// thresholds of the design system. An org without a ceiling gets a neutral rule.
func budgetRuleClass(budget *int64, pct int) string {
	if budget == nil {
		return "border-l-3 border-l-border"
	}
	switch common.BudgetTone(pct) {
	case common.ToneNegative:
		return "border-l-3 border-l-destructive"
	case common.ToneWarning:
		return "border-l-3 border-l-warning"
	default:
		return "border-l-3 border-l-success"
	}
}

// orgDashboardMeta builds the grey line under the dashboard title. The mockup
// puts the identity of what is displayed there — organisation, size, currency,
// period — rather than a sentence explaining the screen.
func orgDashboardMeta(vmodel OrgUsagePageVModel) string {
	parts := []string{vmodel.Org.Name()}

	if n := len(vmodel.Members); n > 0 {
		parts = append(parts, Pluralize(n, "membre", "membres"))
	}

	parts = append(parts, "devise "+vmodel.Currency)

	if label := common.RangeLabel(vmodel.Range); label != "" {
		parts = append(parts, label)
	}

	return strings.Join(parts, " · ")
}

// recordsRangeLabel renders the "1 – 8" position indicator the mockup puts in
// the header of the requests table.
//
// The mockup also states a total ("sur 284 100"). The store paginates without
// counting, so the total is left out rather than guessed: an approximate total
// on a table a user reconciles against an invoice would be worse than none.
func recordsRangeLabel(vmodel OrgUsagePageVModel) string {
	if len(vmodel.Records) == 0 {
		return ""
	}

	page := vmodel.Page
	if page < 1 {
		page = 1
	}

	first := (page-1)*vmodel.PageSize + 1

	return fmt.Sprintf("%d – %d", first, first+len(vmodel.Records)-1)
}

// FilteredPermissionGroup is a catalog group reduced to the permissions that
// match the screen's filters. Groups with nothing left are dropped, so an empty
// section header never sits above no rows.
type FilteredPermissionGroup struct {
	Label string
	Perms []rbac.PermissionDef
}

// filterCatalog applies the search box and the role filter to rbac.Catalog().
//
// Both filters read as one question — "show me the permissions I care about" —
// so they compose: searching "write" while filtering on a role lists the write
// permissions that role grants. The search deliberately covers the group label
// too, so typing "budget" finds the section even though no code contains it.
func filterCatalog(vmodel RolesPageVModel) []FilteredPermissionGroup {
	needle := strings.ToLower(strings.TrimSpace(vmodel.Query))
	role := selectedRole(vmodel)

	var out []FilteredPermissionGroup
	for _, group := range rbac.Catalog() {
		var kept []rbac.PermissionDef

		for _, def := range group.Perms {
			if role != nil && !roleGrants(role, def.Code) {
				continue
			}
			if needle != "" && !permissionMatches(group, def, needle) {
				continue
			}
			kept = append(kept, def)
		}

		if len(kept) > 0 {
			out = append(out, FilteredPermissionGroup{Label: group.Label, Perms: kept})
		}
	}

	return out
}

// permissionMatches reports whether a permission answers the search. The code is
// what a reader types when they know what they want ("quota:write"); the labels
// are what they type when they do not.
func permissionMatches(group rbac.PermissionGroup, def rbac.PermissionDef, needle string) bool {
	return strings.Contains(strings.ToLower(string(def.Code)), needle) ||
		strings.Contains(strings.ToLower(def.Label), needle) ||
		strings.Contains(strings.ToLower(group.Label), needle)
}

// selectedRole resolves the `?role=` filter, or nil when it is absent or points
// at a role this organisation does not have.
func selectedRole(vmodel RolesPageVModel) model.Role {
	if vmodel.RoleID == "" {
		return nil
	}
	for _, role := range vmodel.Roles {
		if string(role.ID()) == vmodel.RoleID {
			return role
		}
	}
	return nil
}

// countPermissions totals the rows a filtered catalog holds, for the count shown
// next to the filters.
func countPermissions(groups []FilteredPermissionGroup) int {
	var total int
	for _, group := range groups {
		total += len(group.Perms)
	}
	return total
}

// totalPermissions is the size of the unfiltered catalog, so the count can read
// "12 sur 26" rather than a bare number.
func totalPermissions() int {
	var total int
	for _, group := range rbac.Catalog() {
		total += len(group.Perms)
	}
	return total
}

// PermissionCell is the state of one cell of the permission matrix.
//
// It separates what the role *holds* from what it *effectively has*, because the
// two differ and the difference is exactly what a checkbox would otherwise get
// wrong: unchecking a permission that another one implies would appear to do
// nothing, since the permission set re-derives it on the next read.
type PermissionCell struct {
	// Granted is the permission as stored on the role — what a checkbox binds to.
	Granted bool
	// ImpliedBy names the permission that grants this one implicitly, when the
	// role does not hold it directly ("providers:write" implies "providers:read").
	ImpliedBy string
	// Editable reports whether this cell can be toggled here.
	Editable bool
	// AlwaysGranted marks the builtin owner, which authorizes everything without
	// holding any code — there is nothing to toggle.
	AlwaysGranted bool
}

// Effective reports whether the role can perform the action, whatever the route.
func (c PermissionCell) Effective() bool {
	return c.AlwaysGranted || c.Granted || c.ImpliedBy != ""
}

// permissionCell resolves one cell for a given role and permission.
//
// `writable` is the reader's own authority: without roles:write the whole matrix
// is read-only, whoever the role in the column is.
func permissionCell(role model.Role, perm rbac.Permission, writable bool) PermissionCell {
	// The owner bypasses every check, so its column is a statement of fact rather
	// than a set of switches — editing it would suggest it could be reduced.
	if role.BuiltinKind() == "owner" {
		return PermissionCell{AlwaysGranted: true}
	}

	cell := PermissionCell{Editable: writable}

	for _, held := range role.Permissions() {
		if held == string(perm) {
			cell.Granted = true
			continue
		}
		if implied, ok := rbac.Implies(rbac.Permission(held)); ok && implied == perm {
			cell.ImpliedBy = held
		}
	}

	return cell
}

// permissionToggleTitle words the tooltip of a matrix checkbox, stating the
// action rather than the state — the checkbox already shows the state.
func permissionToggleTitle(cell PermissionCell, perm rbac.Permission) string {
	if cell.Granted {
		return "Retirer " + string(perm) + " à ce rôle"
	}
	return "Accorder " + string(perm) + " à ce rôle"
}

// roleGrants reports whether a role authorizes a permission, for one cell of the
// permission matrix.
//
// It resolves through rbac.NewPermissionSet rather than scanning the raw code
// list, so the matrix shows the *effective* permissions: the "write implies
// read" rule is applied there, and the builtin owner authorizes everything
// without carrying any code at all. Reading the stored list directly would draw
// a role as unable to read what it can write.
func roleGrants(role model.Role, perm rbac.Permission) bool {
	if role.BuiltinKind() == "owner" {
		return true
	}

	return rbac.NewPermissionSet(role.Permissions(), nil).Has(perm)
}

// aggregateRequests and aggregateTokens read a possibly-nil aggregate. The KPI
// band is always rendered, including on a period with no usage at all, so its
// cells must survive the absence of an aggregate rather than be conditioned on
// it — a missing cell would leave an empty track in the band.
func aggregateRequests(agg *port.UsageAggregate) int64 {
	if agg == nil {
		return 0
	}
	return agg.TotalRequests
}

func aggregateTokens(agg *port.UsageAggregate) int64 {
	if agg == nil {
		return 0
	}
	return agg.TotalTokens
}

// forecastLabel names the end of the budget period. The period is carried as an
// adjective ("mensuel") because that is how the banner title reads it; a cell
// headed "Projection fin de mensuel" needs the noun instead.
func forecastLabel(periodLabel string) string {
	switch periodLabel {
	case "journalier":
		return "Projection fin de journée"
	case "annuel":
		return "Projection fin d'année"
	default:
		return "Projection fin de mois"
	}
}

// projectedSpend extrapolates the spend at the end of the current period from
// what has been spent so far, assuming the pace holds.
//
// It is a linear projection over the calendar period the budget is expressed in,
// which is what the mockup's "Projection fin de mois" marker shows. A projection
// is deliberately not a forecast: it answers "if nothing changes", which is the
// only claim the data supports.
func projectedSpend(spent int64, periodLabel string) int64 {
	now := time.Now()

	var elapsed, total float64
	switch periodLabel {
	case "journalier":
		elapsed = float64(now.Hour())*60 + float64(now.Minute())
		total = 24 * 60
	case "annuel":
		elapsed = float64(now.YearDay())
		total = 365
		if isLeapYear(now.Year()) {
			total = 366
		}
	default: // mensuel
		elapsed = float64(now.Day())
		total = float64(daysInMonth(now))
	}

	// Guard the first instants of a period: dividing by a near-zero elapsed
	// fraction turns a single early call into an absurd projection.
	if elapsed < 1 || total <= 0 {
		return spent
	}

	return int64(float64(spent) * total / elapsed)
}

func daysInMonth(t time.Time) int {
	return time.Date(t.Year(), t.Month()+1, 0, 0, 0, 0, 0, t.Location()).Day()
}

func isLeapYear(year int) bool {
	return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}
