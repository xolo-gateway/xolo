package component

import (
	"context"

	"github.com/xolo-gateway/xolo/internal/core/model"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
	"github.com/xolo-gateway/xolo/internal/i18n"
)

type MiddlewaresPageVModel struct {
	common.AppLayoutVModel
	Org         model.Organization
	Middlewares []model.Middleware
	Success     string
	Error       string
}

// MiddlewareTargetOption is a selectable model (LLM or virtual) a middleware can target.
type MiddlewareTargetOption struct {
	Kind  string // model.ModelRefKindLLM | model.ModelRefKindVirtual
	ID    string
	Label string
}

type MiddlewareFormVModel struct {
	common.AppLayoutVModel
	Org          model.Organization
	Middleware   model.Middleware
	IsNew        bool
	Name         string
	Description  string
	Enabled      bool
	Priority     int
	AppliesToAll bool
	Options      []MiddlewareTargetOption
	Selected     map[string]bool // "kind\x00id" -> checked
	Error        string
}

// middlewareTintClass returns the tint of a middleware's icon square. A disabled
// middleware is drawn muted, so a row that does nothing reads as such before its
// switch is even looked at.
func middlewareTintClass(mw model.Middleware) string {
	if !mw.Enabled() {
		return "bg-muted text-muted-foreground"
	}
	return "bg-chart-3/15 text-chart-3"
}

// middlewarePluginName returns the plugin a middleware's pipeline inserts, which
// the mockup shows in monospace next to the name — it is what a middleware
// actually *does*, whereas its name is free text.
//
// A pipeline may chain several plugins; the first one is shown, since the row
// has space for one identifier and the pipeline editor holds the full truth.
func middlewarePluginName(ctx context.Context, mw model.Middleware) string {
	for _, card := range PipelineCards(ctx, mw.Graph()) {
		if card.Type == model.NodeTypePlugin {
			return card.Name
		}
	}

	return ""
}

// middlewareToggleTitle returns the tooltip of the enable switch, stating the
// action rather than the state — the switch position already shows the state.
func middlewareToggleTitle(ctx context.Context, mw model.Middleware) string {
	if mw.Enabled() {
		return i18n.T(ctx, "org.middleware_toggle.disable")
	}
	return i18n.T(ctx, "org.middleware_toggle.enable")
}

// TargetKey builds the option value/key for a target model. It uses ":" as a
// separator (HTML-form-safe, unlike a NUL byte) — neither the kind (llm|virtual)
// nor the id (xid) contains a colon.
func TargetKey(kind, id string) string {
	return kind + ":" + id
}
