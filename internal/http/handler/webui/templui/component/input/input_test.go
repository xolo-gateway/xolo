package input

import (
	"context"
	"strings"
	"testing"
)

// toggleButton returns the rendered toggle button, from its opening tag to the
// first closing one — buttons do not nest, so the first match is the whole one.
func toggleButton(t *testing.T, html string) string {
	t.Helper()

	start := strings.Index(html, "<button")
	if start < 0 {
		t.Fatalf("no button in: %s", html)
	}
	end := strings.Index(html[start:], "</button>")
	if end < 0 {
		t.Fatalf("unterminated button in: %s", html)
	}

	return html[start : start+end]
}

func renderInput(t *testing.T, props Props) string {
	t.Helper()

	var out strings.Builder
	if err := Input(props).Render(context.Background(), &out); err != nil {
		t.Fatalf("render: %v", err)
	}

	return out.String()
}

// Le nom du bouton est son contenu, et le script bascule « hidden » entre les
// deux spans : le nom annoncé suit donc l'état du champ. Un aria-label, lui,
// l'emporterait sur le contenu et resterait figé sur « afficher ».
func TestPasswordToggleNameFollowsTheState(t *testing.T) {
	button := toggleButton(t, renderInput(t, Props{
		ID:                      "api_key",
		Type:                    TypePassword,
		PasswordToggleLabel:     "Show password",
		PasswordToggleHideLabel: "Hide password",
	}))

	if strings.Contains(button, "aria-label") {
		t.Errorf("an aria-label overrides the content and freezes the name: %s", button)
	}

	open, closed := strings.Index(button, `class="icon-open`), strings.Index(button, `class="icon-closed`)
	show, hide := strings.Index(button, "Show password"), strings.Index(button, "Hide password")

	if show < 0 || hide < 0 {
		t.Fatalf("both labels must be inside the button: %s", button)
	}
	if !(open < show && show < closed) {
		t.Errorf("the label of the hidden state must sit in the span that is visible then: %s", button)
	}
	if !(closed < hide) {
		t.Errorf("the label of the visible state must sit in the span the script reveals: %s", button)
	}
}

// Un appelant qui ne donne qu'un libellé garde le comportement d'avant : le nom
// ne change pas d'état, mais la clé reste employée et le bouton reste nommé.
func TestPasswordToggleWithOneLabelUsesItInBothStates(t *testing.T) {
	button := toggleButton(t, renderInput(t, Props{
		ID:                  "api_key",
		Type:                TypePassword,
		PasswordToggleLabel: "Show password",
	}))

	if got := strings.Count(button, "Show password"); got != 2 {
		t.Errorf("the single label must be rendered in both states, found %d occurrences: %s", got, button)
	}
}
