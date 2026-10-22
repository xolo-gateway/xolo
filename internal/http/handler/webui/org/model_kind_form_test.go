package org

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// The provider of postCreateModel is an openai one, which serves chat only.

func TestCreateModel_DefaultsToTheKindTheAPIServes(t *testing.T) {
	rr, s := postCreateModel(t, url.Values{"proxy_name": {"gpt"}, "real_model": {"gpt-4o"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	if kind := s.created[0].Kind(); kind != model.ModelKindCompletion {
		t.Errorf("kind = %q, want completion", kind)
	}

	rr, s = postCreateModel(t, url.Values{"proxy_name": {"jev"}, "real_model": {"jev-latest"}, "api_type": {"typesafe"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	if kind := s.created[0].Kind(); kind != model.ModelKindDecision {
		t.Errorf("kind = %q, want decision: typesafe serves nothing else", kind)
	}

	// The form preselected completion from the openai provider, and the
	// admin switched the API type to typesafe without touching the kind.
	rr, s = postCreateModel(t, url.Values{"proxy_name": {"jev"}, "real_model": {"jev-latest"}, "api_type": {"typesafe"}, "kind": {"completion"}, "kind_default": {"completion"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	if kind := s.created[0].Kind(); kind != model.ModelKindDecision {
		t.Errorf("kind = %q, want decision: the preselected kind follows the API type", kind)
	}
}

func TestCreateModel_StoresADecisionModel(t *testing.T) {
	rr, s := postCreateModel(t, url.Values{"proxy_name": {"jev"}, "real_model": {"~typesafe/jev-latest"}, "api_type": {"openrouter"}, "kind": {"decision"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	if kind := s.created[0].Kind(); kind != model.ModelKindDecision {
		t.Errorf("kind = %q, want decision", kind)
	}
}

func TestCreateModel_RefusesAKindTheAPIDoesNotServe(t *testing.T) {
	for name, fields := range map[string]url.Values{
		"decision through openai":     {"kind": {"decision"}},
		"completion through typesafe": {"kind": {"completion"}, "api_type": {"typesafe"}},
		"unknown kind":                {"kind": {"rerank"}},
		// A typesafe provider preselected decision, and the override serves
		// two kinds: the server will not pick one for the admin.
		"preselected decision through openai": {"kind": {"decision"}, "kind_default": {"decision"}, "api_type": {"openai"}},
	} {
		t.Run(name, func(t *testing.T) {
			fields.Set("proxy_name", "m")
			fields.Set("real_model", "m-real")
			rr, s := postCreateModel(t, fields)
			if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "Nature") {
				t.Fatalf("status = %d, the kind must be refused: %s", rr.Code, rr.Body.String())
			}
			if len(s.created) != 0 {
				t.Errorf("created %d models, want none", len(s.created))
			}
		})
	}
}

// The update form posts the kind back: saving a decision model must not turn
// it into a completion one.
func TestUpdateModel_KeepsTheKind(t *testing.T) {
	existing := model.NewLLMModel("", "", "jev", "jev-latest", "", 0, 0)
	existing.SetAPIType("typesafe")
	existing.SetKind(model.ModelKindDecision)
	rr, s := postUpdateModel(t, existing, url.Values{"proxy_name": {"jev"}, "real_model": {"jev-latest"}, "api_type": {"typesafe"}, "kind": {"decision"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	if kind := s.saved[0].Kind(); kind != model.ModelKindDecision {
		t.Errorf("kind = %q, want decision", kind)
	}
}

// An update posting no kind, a script's or a form whose select script did not
// run, keeps the model's: an embeddings model must not become a completion
// one, the default of its API.
func TestUpdateModel_WithoutKindKeepsTheModels(t *testing.T) {
	existing := model.NewLLMModel("", "", "vectors", "text-embedding-3-small", "", 0, 0)
	existing.SetKind(model.ModelKindEmbeddings)
	rr, s := postUpdateModel(t, existing, url.Values{"proxy_name": {"vectors"}, "real_model": {"text-embedding-3-small"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	if kind := s.saved[0].Kind(); kind != model.ModelKindEmbeddings {
		t.Errorf("kind = %q, want embeddings", kind)
	}
}

func TestModelForm_OffersTheKind(t *testing.T) {
	rr, _ := postCreateModel(t, url.Values{"proxy_name": {"m"}, "real_model": {"m-real"}, "kind": {"decision"}})
	body := rr.Body.String()
	for _, want := range []string{`name="kind"`, "Complétion", "Décision"} {
		if !strings.Contains(body, want) {
			t.Errorf("the form must offer the kind, missing %q", want)
		}
	}
}
