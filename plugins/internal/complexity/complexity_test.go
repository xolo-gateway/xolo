package complexity

import (
	"strings"
	"testing"
)

const (
	trivialPrompt  = "Quelle est la capitale de l'Australie ?"
	chatPrompt     = "ok merci, et sinon tu penses quoi de la pluie ?"
	proofPrompt    = "Prove that the sum of the first n odd numbers equals n squared, using induction, and discuss whether the argument generalises to other arithmetic progressions."
	analysisPrompt = "Compare les architectures hexagonale et en couches pour un système de paiement soumis à PCI-DSS. Donne les compromis, un tableau, et une recommandation argumentée en 500 mots."
	specPrompt     = "Tu es un architecte logiciel senior. Rédige une spécification technique pour un service de notifications :\n- API REST en JSON\n- au moins 3 scénarios d'erreur\n- un schéma de la base\nNe dépasse pas 800 mots, en français, ton formel, et cite tes sources."
	codePrompt     = "```go\nfunc main() {\n\tfor i := 0; i < 10; i++ {\n\t\tfmt.Println(i)\n\t}\n}\n```\nPourquoi ça ne compile pas ?"
)

func TestAnalyze_EmptyIsZero(t *testing.T) {
	s := AnalyzeDefault("")
	if s.Composite != 0 || s.Level != "trivial" {
		t.Fatalf("empty text must score 0/trivial, got %v/%s", s.Composite, s.Level)
	}
}

func TestAnalyze_TrivialStaysLow(t *testing.T) {
	for _, txt := range []string{"salut", trivialPrompt, chatPrompt, "Écris un poème de quatre vers sur la pluie."} {
		if s := AnalyzeDefault(txt); s.Composite >= 0.15 {
			t.Errorf("%q should stay trivial, got %.2f", txt, s.Composite)
		}
	}
}

func TestAnalyze_DemandingReachesUpperRange(t *testing.T) {
	for _, txt := range []string{analysisPrompt, specPrompt} {
		if s := AnalyzeDefault(txt); s.Composite < 0.7 {
			t.Errorf("%q should score >= 0.7, got %.2f", txt, s.Composite)
		}
	}
	if s := AnalyzeDefault(proofPrompt); s.Composite < 0.45 {
		t.Errorf("a proof request should be at least moderate, got %.2f", s.Composite)
	}
}

func TestAnalyze_Ordering(t *testing.T) {
	trivial := AnalyzeDefault(trivialPrompt).Composite
	proof := AnalyzeDefault(proofPrompt).Composite
	analysis := AnalyzeDefault(analysisPrompt).Composite
	if !(trivial < proof && proof < analysis) {
		t.Errorf("expected trivial < proof < analysis, got %.2f < %.2f < %.2f", trivial, proof, analysis)
	}
}

func TestAnalyze_LongBanalTextIsNotComplex(t *testing.T) {
	long := "Résume ce texte : " + strings.Repeat("Le chat dort sur le canapé. ", 200)
	s := AnalyzeDefault(long)
	if s.LengthScore < 0.95 {
		t.Errorf("length should saturate, got %.2f", s.LengthScore)
	}
	if s.Composite > 0.4 {
		t.Errorf("a long repetitive text must not read as complex, got %.2f", s.Composite)
	}
}

func TestAnalyze_CodeDetection(t *testing.T) {
	fenced := AnalyzeDefault(codePrompt)
	if !fenced.Stats.HasCode || fenced.Stats.CodeBlocks != 1 {
		t.Errorf("fenced block must be detected: %+v", fenced.Stats)
	}
	unfenced := AnalyzeDefault("j'ai cette erreur: panic: runtime error: invalid memory address at main.go:42 quand j'appelle db.Query(ctx, sql) dans handler.go, une idée ?")
	if !unfenced.Stats.HasCode {
		t.Errorf("unfenced code signals must be detected: %+v", unfenced.Stats)
	}
	if prose := AnalyzeDefault(analysisPrompt); prose.Stats.HasCode {
		t.Errorf("prose must not be flagged as code: %+v", prose.Stats)
	}
}

func TestAnalyze_ConstraintsAndDemands(t *testing.T) {
	s := AnalyzeDefault(specPrompt)
	if s.Stats.ConstraintCount < 6 {
		t.Errorf("expected many constraints, got %d", s.Stats.ConstraintCount)
	}
	p := AnalyzeDefault(proofPrompt)
	if p.Stats.DemandCount < 2 {
		t.Errorf("expected at least two kinds of demand, got %d", p.Stats.DemandCount)
	}
}

func TestRise(t *testing.T) {
	if rise(0, 3, 1) != 0 {
		t.Error("rise must start at 0")
	}
	if rise(-1, 3, 1) != 0 {
		t.Error("negative input clamps to 0")
	}
	if a, b := rise(2, 3, 1), rise(4, 3, 1); !(a < b) {
		t.Errorf("rise must be increasing, got %.2f >= %.2f", a, b)
	}
	if rise(1000, 3, 1) < 0.999 {
		t.Error("rise must approach 1")
	}
}
