package component

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
)

// TestModelsPageVirtualModelCapabilities pins how the page shows the
// capabilities of virtual models: derived ones reach the OpenCode
// configuration, undetermined ones are flagged on the card and named under
// the examples, since their false values mean unknown.
func TestModelsPageVirtualModelCapabilities(t *testing.T) {
	current, _ := url.Parse("http://xolo.test/models")
	ctx := httpCtx.SetBaseURL(context.Background(), "http://xolo.test")
	ctx = httpCtx.SetCurrentURL(ctx, current)

	derived := model.NewLLMModel("", "org", "thinker", "", "", 0, 0)
	derived.SetCapabilities(model.ModelCapabilities{Reasoning: true})
	undetermined := model.NewLLMModel("", "org", "router", "", "", 0, 0)
	models := []CatalogModel{
		{Model: derived, Names: []string{"acme/thinker"}},
		{Model: undetermined, Names: []string{"acme/router"}, CapabilitiesUndetermined: true},
	}

	var out strings.Builder
	if err := ModelsPage(ModelsPageVModel{
		AppLayoutVModel: common.AppLayoutVModel{
			Breadcrumbs: []common.BreadcrumbItem{{Label: "X", Href: ""}},
		},
		Models:    models,
		AllModels: models,
	}).Render(ctx, &out); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := out.String()

	if !strings.Contains(html, `&#34;reasoning&#34;: true`) {
		t.Errorf("the derived reasoning capability must reach the OpenCode configuration, got:\n%s", html)
	}
	if got := strings.Count(html, "capacités non déterminées"); got != 1 {
		t.Errorf("only the undetermined model must carry the badge, got %d", got)
	}
	note := html[strings.Index(html, "Capacités non déterminées pour"):]
	if !strings.Contains(note, "acme/router") || strings.Contains(note[:strings.Index(note, "</p>")], "acme/thinker") {
		t.Errorf("the note must name the undetermined model only, got:\n%s", note)
	}
}

// TestModelsPageRendersTheFilter pins the catalog view: the controls carry
// the current filter, the facets decide which controls appear, and a filter
// matching nothing says so while the call examples keep every model.
func TestModelsPageRendersTheFilter(t *testing.T) {
	current, _ := url.Parse("http://xolo.test/models?q=zzz&cap=vision")
	ctx := httpCtx.SetBaseURL(context.Background(), "http://xolo.test")
	ctx = httpCtx.SetCurrentURL(ctx, current)

	large := model.NewLLMModel("", "org", "large", "", "", 2, 6)
	large.SetCapabilities(model.ModelCapabilities{Tools: true})
	all := []CatalogModel{{Model: large, Names: []string{"acme/large"}}}

	render := func(vmodel ModelsPageVModel) string {
		vmodel.AppLayoutVModel = common.AppLayoutVModel{Breadcrumbs: []common.BreadcrumbItem{{Label: "X", Href: ""}}}
		var out strings.Builder
		if err := ModelsPage(vmodel).Render(ctx, &out); err != nil {
			t.Fatalf("render: %v", err)
		}
		return out.String()
	}

	html := render(ModelsPageVModel{
		AllModels: all,
		Filter:    ModelsFilter{Query: "zzz", Capabilities: []string{"vision"}, Sort: ModelsSortName},
		Facets:    ModelsFacets{Providers: []string{"Mistral"}},
	})
	for _, want := range []string{`name="q"`, `value="zzz"`, `id="models-cap-vision"`, "Aucun modèle trouvé", "Effacer les filtres", "0 modèle sur 1", `&#34;acme/large&#34;`} {
		if !strings.Contains(html, want) {
			t.Errorf("filtered view must contain %q", want)
		}
	}
	if strings.Contains(html, `name="provider"`) || strings.Contains(html, `name="type"`) || strings.Contains(html, `name="kind"`) {
		t.Errorf("a filter with a single choice must not be offered")
	}
	if !regexp.MustCompile(`<input checked[^>]*id="models-cap-vision"`).MatchString(html) ||
		regexp.MustCompile(`<input checked[^>]*id="models-cap-tools"`).MatchString(html) {
		t.Errorf("only the vision capability must be checked")
	}

	html = render(ModelsPageVModel{
		Models:    all,
		AllModels: all,
		Filter:    ModelsFilter{Sort: ModelsSortName},
		Facets:    ModelsFacets{Providers: []string{"Mistral", "OpenRouter"}, Virtual: true},
	})
	for _, want := range []string{`name="provider"`, `name="type"`, "1 modèle", ">acme/large</span>", "Outils"} {
		if !strings.Contains(html, want) {
			t.Errorf("unfiltered view must contain %q", want)
		}
	}
	if strings.Contains(html, "Effacer les filtres") {
		t.Errorf("an unfiltered view has no filter to clear")
	}
}

func TestDescriptionSummary(t *testing.T) {
	cases := []struct {
		description string
		summary     string
		more        bool
	}{
		{description: "Fast and cheap.", summary: "Fast and cheap."},
		{description: "  Routes to\ndeepseek.  ", summary: "Routes to deepseek."},
		{description: "First paragraph.\r\n\r\nSecond one.", summary: "First paragraph.", more: true},
		{description: strings.Repeat("long ", 20), summary: strings.TrimSpace(strings.Repeat("long ", 20)), more: true},
	}
	for _, tc := range cases {
		summary, more := descriptionSummary(tc.description)
		if summary != tc.summary || more != tc.more {
			t.Errorf("descriptionSummary(%q) = %q, %v, want %q, %v", tc.description, summary, more, tc.summary, tc.more)
		}
	}
}
