package component

import (
	"context"
	"strings"

	"github.com/a-h/templ"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/http/handler/webui/templui/component/icon"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

// Context identifies which of the three product spaces the current page belongs
// to. It drives the colour of the top rail, the pill of the context switcher and
// the set of groups rendered in the sidebar.
type Context string

const (
	ContextPersonal Context = "personal"
	ContextOrg      Context = "org"
	ContextPlatform Context = "platform"
)

// Kicker returns the small uppercase line displayed above the context name in
// the switcher.
func (c Context) Kicker(ctx context.Context) string {
	switch c {
	case ContextOrg:
		return i18n.T(ctx, "nav.context.org")
	case ContextPlatform:
		return i18n.T(ctx, "nav.context.platform")
	default:
		return i18n.T(ctx, "nav.context.personal")
	}
}

// DefaultName returns the context name to fall back on when the handler did not
// provide one. The personal space is named after the user, so resolve overrides
// this with the display name whenever there is one.
func (c Context) DefaultName(ctx context.Context) string {
	switch c {
	case ContextPlatform:
		return i18n.T(ctx, "nav.context.platform_name")
	case ContextOrg:
		return ""
	default:
		return i18n.T(ctx, "nav.context.personal_name")
	}
}

// switcherGroupLabel returns the heading to draw above entries[i], or an empty
// string when the entry continues the group above it.
//
// The console has no heading: the mockup separates it from the organisations
// with a plain rule, since it is a single row whose label already says what it
// is.
func switcherGroupLabel(ctx context.Context, entries []SwitcherEntry, i int) string {
	if i > 0 && entries[i].Context == entries[i-1].Context {
		return ""
	}
	switch entries[i].Context {
	case ContextPersonal:
		return i18n.T(ctx, "nav.context.personal")
	case ContextOrg:
		count := 0
		for _, e := range entries {
			if e.Context == ContextOrg {
				count++
			}
		}
		return i18n.T(ctx, "switcher.group.orgs", count)
	default:
		return ""
	}
}

// RailClass returns the background utility of the 3px rail pinned at the top of
// the window, which is the primary way a user tells the three spaces apart.
func (c Context) RailClass() string {
	switch c {
	case ContextOrg:
		return "bg-primary"
	case ContextPlatform:
		return "bg-platform"
	default:
		return "bg-personal"
	}
}

// AccentClass returns the foreground utility of the context accent. It inks
// everything in the sidebar header that announces the space: the watermark
// logo, the version and the kicker above the context name.
func (c Context) AccentClass() string {
	switch c {
	case ContextOrg:
		return "text-primary"
	case ContextPlatform:
		return "text-platform"
	default:
		return "text-personal"
	}
}

// TintGradientClass returns the first stop of the wash the sidebar header fades
// out from — the second way, after the rail, that a space announces itself.
func (c Context) TintGradientClass() string {
	switch c {
	case ContextOrg:
		return "from-primary-tint"
	case ContextPlatform:
		return "from-platform-tint"
	default:
		return "from-personal-tint"
	}
}

// PillClass returns the tint/foreground pair of the small square shown in front
// of the context name in the switcher.
//
// The square is tinted, never filled: §02 of the design system reserves the flat
// accent for the rail and the banners, and the pastille always reads as
// "tint background + accent text".
func (c Context) PillClass() string {
	switch c {
	case ContextOrg:
		return "bg-primary-tint text-primary"
	case ContextPlatform:
		return "bg-platform-tint text-platform"
	default:
		return "bg-personal-tint text-personal"
	}
}

// Initial returns the one or two letters displayed inside the switcher pill.
//
// It is built from the label rather than from the slug so that an organisation
// carries the same pastille here and in the tables that list it, which key on
// the name ("Ville de Lyon" → VL, not VI).
func (e SwitcherEntry) Initial() string {
	if e.Label != "" {
		return Initials(e.Label)
	}
	return Initials(e.Slug)
}

// Initials returns the one or two letters every pastille of the design system is
// built from — the user avatars as well as the organisation squares.
//
// Names reach it in several shapes, and all of them have to read well: a display
// name ("Ville de Lyon"), a slug ("ville-lyon"), a login ("a.leroy", "cmsassot")
// and — since preferred_username often carries one — an e-mail address
// ("m.bernard@acme.example"). Hence the three rules below: the domain of an
// address is dropped, the separators of a login and of a slug count as word
// boundaries, and the second letter is taken from the *last* word rather than
// the second one, so a particle in the middle is skipped — "Ville de Lyon"
// gives VL, not VD.
func Initials(name string) string {
	if local, _, ok := strings.Cut(name, "@"); ok && local != "" {
		name = local
	}
	fields := strings.FieldsFunc(name, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '.', '_', '-':
			return true
		default:
			return false
		}
	})
	switch len(fields) {
	case 0:
		return "?"
	case 1:
		r := []rune(fields[0])
		if len(r) > 1 {
			return strings.ToUpper(string(r[:2]))
		}
		return strings.ToUpper(string(r))
	default:
		first := []rune(fields[0])[:1]
		last := []rune(fields[len(fields)-1])[:1]
		return strings.ToUpper(string(first) + string(last))
	}
}

// ProxyStatus is the upstream health indicator shown in the header.
//
// TODO(rework-ux): no health probe exists yet on the domain side; every page
// currently reports ProxyStatusUnknown, which the header renders as a disabled
// "Bientôt disponible" badge. See lot 7.
type ProxyStatus string

const (
	ProxyStatusUnknown     ProxyStatus = ""
	ProxyStatusOperational ProxyStatus = "operational"
	ProxyStatusDegraded    ProxyStatus = "degraded"
	ProxyStatusDown        ProxyStatus = "down"
)

// NavEntry is a single sidebar link. Entries are plain data so the navigation of
// each context can be built — and unit tested — in Go rather than in a template.
type NavEntry struct {
	Label string
	Href  string
	Icon  func(...icon.Props) templ.Component
	// Active highlights the entry as the current page.
	Active bool
	// Badge is an optional counter or ratio rendered at the end of the row.
	Badge string
	// BadgeTone inks that counter. §06 of the design system tints it like any
	// badge — grey for a plain count, amber for a budget nearing its ceiling.
	BadgeTone Tone
	// Disabled renders the entry as non-interactive. Combined with ComingSoon it
	// is how features present in the mockup but absent from the product are
	// surfaced (see lot 7 of docs/plan/rework-ux.md).
	Disabled   bool
	ComingSoon bool
}

// NavGroup is a titled section of the sidebar navigation.
type NavGroup struct {
	Title string
	Items []NavEntry
}

// SwitcherEntry is one destination of the context switcher popover.
type SwitcherEntry struct {
	Context Context
	Label   string
	// Slug is displayed under the label in mono; empty for the personal space and
	// the platform console.
	Slug    string
	Href    string
	Current bool
	// Cost and Usage back the 30-day cost and the mini gauge of the mockup.
	//
	// TODO(rework-ux): no cross-org aggregate is exposed by the domain yet, so
	// both stay zero and the popover omits the gauge. See lot 7.
	Cost  string
	Usage int
}

// AppLayoutVModel defines the view model for the app layout.
type AppLayoutVModel struct {
	User         model.User
	IsAdmin      bool
	SelectedItem string
	Breadcrumbs  []BreadcrumbItem
	Version      string
	APIDocURL    string

	// Context and its companions describe the space the page belongs to. Handlers
	// only set Context (plus name/slug/ID for an organisation); everything else is
	// derived by resolve.
	Context      Context
	ContextName  string
	ContextSlug  string
	ContextOrgID model.OrgID

	// Switcher lists the destinations of the context popover. Left nil, resolve
	// builds it from the memberships carried by the request context.
	Switcher []SwitcherEntry

	// IsAdminVisit reports a platform admin browsing an organisation they are not
	// a member of. Derived by resolve; it drives the warning banner.
	IsAdminVisit bool

	// NavGroups is the sidebar navigation. Left nil, resolve builds the groups of
	// the current context.
	NavGroups []NavGroup

	ProxyStatus ProxyStatus

	HomeLink string
	// FullBleed removes padding from the main content area and disables overflow-auto,
	// useful for full-height canvas views like the pipeline editor.
	FullBleed bool
}

// BreadcrumbItem represents a single item in the breadcrumb navigation.
type BreadcrumbItem struct {
	Label string
	Href  string
}
