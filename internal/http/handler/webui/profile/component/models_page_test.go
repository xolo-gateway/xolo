package component

import (
	"context"
	"net/url"
	"strings"
	"testing"

	httpCtx "github.com/xolo-gateway/xolo/internal/http/context"
	common "github.com/xolo-gateway/xolo/internal/http/handler/webui/common/component"
)

// TestModelsSortControlRendersActiveTabs pins the two-segment cyclic
// sort control built from modelsSortControl: Usage and Prix. Each
// segment is a plain <a> link whose href carries the URL of the next
// cycle step. The active segment's label embeds its current
// direction (Usage ↓ / Usage ↑ / Prix ↑ / Prix ↓). Clicking the
// active segment cycles to the opposite direction; clicking the
// inactive segment lands on the new criterion at its natural default
// direction. The control uses common.SegmentedNav so it inherits the
// same visual style as the period selector (24 h / 7 j / 30 j / 12 m).
func TestModelsSortControlRendersActiveTabs(t *testing.T) {
	cases := []struct {
		name         string
		currentSort  string
		currentOrder string
		// activeLabel is the visible text of the active segment.
		activeLabel string
		// inactiveLabel is the visible text of the inactive segment.
		inactiveLabel string
		// activeMustContain / activeMustNotContain pin the URL of
		// the active segment, which carries the NEXT cycle step.
		activeMustContain    []string
		activeMustNotContain []string
		// inactiveMustContain / inactiveMustNotContain pin the URL
		// of the inactive segment, which lands on the criterion's
		// natural default.
		inactiveMustContain    []string
		inactiveMustNotContain []string
	}{
		{
			name:                   "default usage view: Usage ↓ active (→ Usage ↑), Prix inactive (→ Prix ↑)",
			currentSort:            "",
			currentOrder:           "desc",
			activeLabel:            "Usage ↓",
			inactiveLabel:          "Prix",
			activeMustContain:      []string{"order=asc", "range=7d", "show_all=true"},
			activeMustNotContain:   []string{"sort=price", "order=desc&order=asc", "order=asc&order=desc"},
			inactiveMustContain:    []string{"sort=price", "range=7d", "show_all=true"},
			inactiveMustNotContain: []string{"order=", "sort=price&sort=price"},
		},
		{
			name:                   "usage asc view: Usage ↑ active (→ Usage ↓), Prix inactive (→ Prix ↑)",
			currentSort:            "",
			currentOrder:           "asc",
			activeLabel:            "Usage ↑",
			inactiveLabel:          "Prix",
			activeMustContain:      []string{"range=7d", "show_all=true"},
			activeMustNotContain:   []string{"order=", "sort=price", "order=asc&order=desc", "order=desc&order=asc"},
			inactiveMustContain:    []string{"sort=price", "range=7d", "show_all=true"},
			inactiveMustNotContain: []string{"order=", "order=asc&order=desc", "order=desc&order=asc"},
		},
		{
			name:                   "price asc view: Prix ↑ active (→ Prix ↓), Usage inactive (→ Usage ↓)",
			currentSort:            "price",
			currentOrder:           "asc",
			activeLabel:            "Prix ↑",
			inactiveLabel:          "Usage",
			activeMustContain:      []string{"sort=price", "order=desc", "range=7d", "show_all=true"},
			activeMustNotContain:   []string{"sort=price&sort=price", "order=asc&order=desc", "order=desc&order=asc"},
			inactiveMustContain:    []string{"range=7d", "show_all=true"},
			inactiveMustNotContain: []string{"sort=", "order=", "sort=price&sort=price", "order=asc&order=desc", "order=desc&order=asc"},
		},
		{
			name:                   "price desc view: Prix ↓ active (→ Prix ↑), Usage inactive (→ Usage ↓)",
			currentSort:            "price",
			currentOrder:           "desc",
			activeLabel:            "Prix ↓",
			inactiveLabel:          "Usage",
			activeMustContain:      []string{"sort=price", "range=7d", "show_all=true"},
			activeMustNotContain:   []string{"sort=price&sort=price", "order=desc", "order=asc&order=desc", "order=desc&order=asc"},
			inactiveMustContain:    []string{"range=7d", "show_all=true"},
			inactiveMustNotContain: []string{"sort=", "order=", "sort=price&sort=price", "order=asc&order=desc", "order=desc&order=asc"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The current URL must mirror the view under test, otherwise
			// the active segment's href (which is built from the current
			// URL minus the toggled param) would not show the right
			// payload.
			var raw string
			switch {
			case tc.currentSort == "price" && tc.currentOrder == "desc":
				raw = "http://xolo.test/models?range=7d&show_all=true&sort=price&order=desc"
			case tc.currentSort == "price" && tc.currentOrder == "asc":
				raw = "http://xolo.test/models?range=7d&show_all=true&sort=price&order=asc"
			case tc.currentSort == "" && tc.currentOrder == "asc":
				raw = "http://xolo.test/models?range=7d&show_all=true&order=asc"
			default:
				raw = "http://xolo.test/models?range=7d&show_all=true"
			}
			current, _ := url.Parse(raw)
			ctx := httpCtx.SetBaseURL(context.Background(), "http://xolo.test")
			ctx = httpCtx.SetCurrentURL(ctx, current)

			var out strings.Builder
			if err := modelsSortControl(tc.currentSort, tc.currentOrder).Render(ctx, &out); err != nil {
				t.Fatalf("render: %v", err)
			}
			html := out.String()

			// Exactly two segments: the active label and the inactive label.
			activeCount := strings.Count(html, ">"+tc.activeLabel+"<")
			if activeCount != 1 {
				t.Errorf("expected exactly one occurrence of active label %q, got %d:\n%s", tc.activeLabel, activeCount, html)
			}
			inactiveCount := strings.Count(html, ">"+tc.inactiveLabel+"<")
			if inactiveCount != 1 {
				t.Errorf("expected exactly one occurrence of inactive label %q, got %d:\n%s", tc.inactiveLabel, inactiveCount, html)
			}

			// Both segments are rendered as <a> links in this cyclic
			// shape (the active one carries the next-cycle URL).
			for _, label := range []string{tc.activeLabel, tc.inactiveLabel} {
				tag := extractSegment(html, label)
				if tag == "" {
					t.Fatalf("could not find segment %q in:\n%s", label, html)
				}
				if !strings.HasPrefix(tag, "<a ") {
					t.Errorf("segment %q must be an <a>, got:\n%s", label, tag)
				}
				if !strings.Contains(tag, "href=") {
					t.Errorf("segment %q must carry an href, got:\n%s", label, tag)
				}
			}

			// Active segment carries the active styling (bg-primary-tint).
			activeTag := extractSegment(html, tc.activeLabel)
			if !strings.Contains(activeTag, "bg-primary-tint") {
				t.Errorf("active segment %q must use bg-primary-tint (matching the period selector), got:\n%s", tc.activeLabel, activeTag)
			}

			// Active segment's href carries the next cycle step.
			activeHref := extractHref(activeTag)
			if activeHref == "" {
				t.Fatalf("could not find href on active segment %q", tc.activeLabel)
			}
			activeDecoded, err := url.QueryUnescape(activeHref)
			if err != nil {
				t.Fatalf("active href %q is not valid: %v", activeHref, err)
			}
			for _, want := range tc.activeMustContain {
				if !strings.Contains(activeDecoded, want) {
					t.Errorf("active segment %q href %q should contain %q", tc.activeLabel, activeDecoded, want)
				}
			}
			for _, unwanted := range tc.activeMustNotContain {
				if strings.Contains(activeDecoded, unwanted) {
					t.Errorf("active segment %q href %q must NOT contain %q", tc.activeLabel, activeDecoded, unwanted)
				}
			}

			// Inactive segment's href carries the criterion's natural
			// default.
			inactiveTag := extractSegment(html, tc.inactiveLabel)
			inactiveHref := extractHref(inactiveTag)
			if inactiveHref == "" {
				t.Fatalf("could not find href on inactive segment %q", tc.inactiveLabel)
			}
			inactiveDecoded, err := url.QueryUnescape(inactiveHref)
			if err != nil {
				t.Fatalf("inactive href %q is not valid: %v", inactiveHref, err)
			}
			for _, want := range tc.inactiveMustContain {
				if !strings.Contains(inactiveDecoded, want) {
					t.Errorf("inactive segment %q href %q should contain %q", tc.inactiveLabel, inactiveDecoded, want)
				}
			}
			for _, unwanted := range tc.inactiveMustNotContain {
				if strings.Contains(inactiveDecoded, unwanted) {
					t.Errorf("inactive segment %q href %q must NOT contain %q", tc.inactiveLabel, inactiveDecoded, unwanted)
				}
			}
		})
	}
}

// extractSegment returns the HTML tag (<button ...>...</button> or <a ...>...</a>)
// that wraps the given visible label. The label is assumed to be the
// inner text of the tag (e.g. >Usage< or >Prix ↑<), so we locate it
// and walk backwards to the nearest <a or <button opener.
func extractSegment(html string, label string) string {
	idx := strings.Index(html, label)
	if idx < 0 {
		return ""
	}
	// Walk back from the label to the nearest opening tag, picking the
	// last one whose name is 'a' or 'button'.
	searchFrom := idx
	for {
		lt := strings.LastIndex(html[:searchFrom], "<")
		if lt < 0 {
			return ""
		}
		// Read the tag name (chars until whitespace or '>').
		rest := html[lt+1:]
		end := strings.IndexAny(rest, " >\n\t")
		if end <= 0 {
			return ""
		}
		name := rest[:end]
		if name == "a" || name == "button" {
			openEnd := strings.Index(html[lt:], ">")
			if openEnd < 0 {
				return ""
			}
			openEnd += lt
			closeTag := "</" + name + ">"
			closeIdx := strings.Index(html[openEnd:], closeTag)
			if closeIdx < 0 {
				return ""
			}
			closeIdx += openEnd
			return html[lt : closeIdx+len(closeTag)]
		}
		// Some other tag (e.g. <span>): keep walking back.
		searchFrom = lt
	}
}

// extractHref returns the href attribute value of an anchor tag.
func extractHref(tag string) string {
	start := strings.Index(tag, `href="`)
	if start < 0 {
		return ""
	}
	start += len(`href="`)
	end := strings.Index(tag[start:], `"`)
	if end < 0 {
		return ""
	}
	return tag[start : start+end]
}

// TestModelsPageRangeFormHidesOrderWhenNotExplicit pins the rule that the
// range form only round-trips the `order` hidden input when the request
// explicitly carried ?order=... . On the default usage view (no order
// param) the form must stay clean, so the user's bookmark of /models
// does not pick up a redundant ?order=desc suffix just from changing
// the period.
func TestModelsPageRangeFormHidesOrderWhenNotExplicit(t *testing.T) {
	current, _ := url.Parse("http://xolo.test/models")
	ctx := httpCtx.SetBaseURL(context.Background(), "http://xolo.test")
	ctx = httpCtx.SetCurrentURL(ctx, current)

	vmodel := ModelsPageVModel{
		AppLayoutVModel: common.AppLayoutVModel{
			Breadcrumbs: []common.BreadcrumbItem{{Label: "X", Href: ""}},
		},
		Range:         "7d",
		Order:         "desc",
		OrderExplicit: false, // default view: no ?order=... on the request
	}

	var out strings.Builder
	if err := ModelsPage(vmodel).Render(ctx, &out); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := out.String()

	if strings.Contains(html, `name="order"`) {
		t.Errorf("default view must NOT emit a hidden order input, got:\n%s", html)
	}
}

// TestModelsPageRangeFormRoundTripsExplicitOrder confirms the opposite:
// when the request explicitly carried ?order=desc, the range form must
// emit the hidden input so the order survives a period change.
func TestModelsPageRangeFormRoundTripsExplicitOrder(t *testing.T) {
	current, _ := url.Parse("http://xolo.test/models?order=desc")
	ctx := httpCtx.SetBaseURL(context.Background(), "http://xolo.test")
	ctx = httpCtx.SetCurrentURL(ctx, current)

	vmodel := ModelsPageVModel{
		AppLayoutVModel: common.AppLayoutVModel{
			Breadcrumbs: []common.BreadcrumbItem{{Label: "X", Href: ""}},
		},
		Range:         "7d",
		Order:         "desc",
		OrderExplicit: true,
	}

	var out strings.Builder
	if err := ModelsPage(vmodel).Render(ctx, &out); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := out.String()

	if !strings.Contains(html, `name="order" value="desc"`) {
		t.Errorf("explicit ?order=desc view must emit the hidden input, got:\n%s", html)
	}
}
