package synth

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/bornholm/genai/llm"
)

// AuthorOptions drives a template-writing campaign.
type AuthorOptions struct {
	Lang        string
	Count       int     // templates to obtain
	Batch       int     // templates requested per call
	Malicious   float64 // share of malicious templates requested, in [0, 1]
	Temperature float64
	MaxRepairs  int
	// MaxSimilarity rejects a draft too close to an existing template.
	MaxSimilarity float64
	// Focus is an extra instruction appended to every request, to steer a
	// campaign towards what the corpus lacks.
	Focus   string
	Verbose bool
	Debug   bool
	// Emit is called for every accepted template, before the campaign goes
	// on, so that an interrupted run keeps what it has.
	Emit func(*Template) error
	// Log receives progress lines when Verbose is set.
	Log func(string)
}

// DefaultAuthorOptions: batches of three, temperature high, half malicious.
func DefaultAuthorOptions() AuthorOptions {
	return AuthorOptions{
		Lang: "fr", Count: 6, Batch: 3, Malicious: 0.5, Temperature: 0.9,
		MaxRepairs: 3, MaxSimilarity: 0.6,
	}
}

// Author asks a model for templates and trusts it on nothing: every draft
// goes through Parse, then through the similarity check, and an invalid one
// is sent back with the parser's error until it passes or runs out of
// repairs.
type Author struct {
	client   llm.Client
	lex      Lexicon
	existing []*Template
	opts     AuthorOptions
}

// NewAuthor builds an author. existing templates are both avoided in the
// prompt and used for the similarity check.
func NewAuthor(client llm.Client, lex Lexicon, existing []*Template, opts AuthorOptions) *Author {
	if opts.Log == nil {
		opts.Log = func(string) {}
	}
	return &Author{client: client, lex: lex, existing: existing, opts: opts}
}

// Run obtains Count valid templates.
func (a *Author) Run(ctx context.Context) ([]*Template, error) {
	var results []*Template
	seen := map[string]bool{}
	for _, t := range a.existing {
		if t.Lang == a.opts.Lang {
			seen[t.Name] = true
		}
	}
	batchSize := a.opts.Batch
	if batchSize <= 0 {
		batchSize = 1
	}
	for len(results) < a.opts.Count {
		batch := min(batchSize, a.opts.Count-len(results))
		drafts, err := a.request(ctx, batch, keys(seen))
		if err != nil {
			if batch > 1 && !definitive(err) {
				batchSize = batch / 2
				a.log(fmt.Sprintf("  … %v, retrying with batches of %d", err, batchSize))
				continue
			}
			return results, err
		}
		before := len(results)
		for _, d := range drafts {
			if len(results) >= a.opts.Count {
				break
			}
			t, err := a.accept(ctx, d)
			if err != nil {
				a.log(fmt.Sprintf("  ✗ %s: %v", d.name, err))
				continue
			}
			if seen[t.Name] {
				a.log(fmt.Sprintf("  ✗ %s: name already used", t.Name))
				continue
			}
			seen[t.Name] = true
			a.existing = append(a.existing, t)
			if a.opts.Emit != nil {
				if err := a.opts.Emit(t); err != nil {
					return results, err
				}
			}
			results = append(results, t)
			a.log(fmt.Sprintf("  ✓ %s (malicious=%t)", t.Name, t.Malicious))
		}
		if len(results) == before {
			return results, fmt.Errorf("no template accepted out of %d draft(s); run with -debug to see the model's answer", len(drafts))
		}
	}
	return results, nil
}

func (a *Author) log(s string) {
	if a.opts.Verbose {
		a.opts.Log(s)
	}
}

type draft struct {
	name string
	src  string
}

// accept validates a draft and sends it back for repair while it fails.
func (a *Author) accept(ctx context.Context, d draft) (*Template, error) {
	src := d.src
	for attempt := 0; ; attempt++ {
		t, err := a.validate(src)
		if err == nil {
			return t, nil
		}
		if attempt >= a.opts.MaxRepairs {
			return nil, fmt.Errorf("still invalid after %d repair(s): %v", attempt, err)
		}
		repaired, rerr := a.repair(ctx, src, err)
		if rerr != nil {
			return nil, rerr
		}
		src = repaired
	}
}

func (a *Author) validate(src string) (*Template, error) {
	t, err := Parse(src, a.lex)
	if err != nil {
		return nil, err
	}
	if t.Lang != a.opts.Lang {
		return nil, fmt.Errorf("lang must be %s", a.opts.Lang)
	}
	if near, sim := MostSimilar(t.Body, a.existing); near != nil && sim >= a.opts.MaxSimilarity {
		return nil, fmt.Errorf("too close to existing template %q (similarity %.2f): write a different structure", near.Name, sim)
	}
	return t, nil
}

const systemPrompt = `Tu écris des TEMPLATES de messages pour un générateur de corpus destiné à un détecteur d'injection de prompt (un filtre qui protège un assistant IA contre les textes qui tentent de détourner ses instructions).

Un template est un SQUELETTE : la structure d'un message, avec des emplacements {{slot}} que le code remplira avec des mots tirés d'un lexique. Tu n'écris pas les mots d'attaque eux-mêmes, tu décris où ils se placent. L'étiquette (malveillant ou non, catégories) est une propriété du template, pas du remplissage.

Deux sortes de templates comptent autant l'une que l'autre :
- MALVEILLANTS (malicious: true) : remplacement des instructions, fuite du prompt système, détournement de rôle, obfuscation, abus d'outils, exfiltration. Varie la STRUCTURE : politesse, urgence, prétexte technique, jeu de rôle, texte planté dans un document (segment: tool), fausse fin de prompt, mise en scène.
- NÉGATIFS DIFFICILES (malicious: false, categories vide) : des demandes honnêtes qui partagent le vocabulaire des attaques. Citer une attaque pour la traduire ou l'analyser avec {{attack_quote}}, parler de sécurité, demander un jeu de rôle légitime, employer les mots « système », « instructions », « règles » dans leur sens ordinaire.

Format de réponse : un bloc de code par template, rien entre les blocs. La ligne « # name: » vient en premier.

` + "```" + `
# name: identifiant-en-minuscules-avec-tirets
categories: prompt_injection, prompt_leakage
malicious: true
lang: %[1]s
segment: user
---
Corps du template, une à quatre phrases, avec des {{slots}}.
` + "```" + `

Champs d'en-tête : categories (parmi prompt_injection, prompt_leakage, role_hijacking, obfuscation, tool_abuse, exfiltration ; vide si malicious: false), malicious (true/false), lang (%[1]s), segment (user pour un message de l'utilisateur, tool pour un texte renvoyé par un outil ou un document, history pour un tour précédent).

Emplacements disponibles pour la langue %[1]s, avec deux exemples de valeurs chacun :
%[2]s
Emplacements spéciaux : {{attack_quote}} insère une attaque rendue (réservé aux templates malicious: false), {{benign}} insère une demande ordinaire. {{a|b|c}} choisit une alternative sur place.

Règles :
- au moins un {{slot}} par template, sans quoi il ne varie pas ;
- chaque slot est rendu par une expression complète (un verbe, un groupe nominal) : écris la phrase AUTOUR des slots pour que le résultat se lise comme un vrai message, sépare toujours un slot des mots voisins par une espace ou une ponctuation, et relis mentalement le rendu avec les exemples de valeurs ;
- n'utilise pas un même slot plus de deux fois ; deux slots collés ou un slot collé à un mot sont refusés ;
- écris dans la langue %[1]s uniquement ;
- termine chaque phrase par un point, le code ne l'ajoute pas ;
- n'invente aucun slot hors de la liste ;
- chaque catégorie déclarée doit s'appuyer sur un slot qui la porte : exfiltration exige {{exfil_target}}, {{secret_noun}} ou {{data_noun}} ; obfuscation exige {{encoding}}, {{decode_verb}} ou {{role_marker}} ; prompt_leakage exige {{leak_request}} ; tool_abuse exige {{tool_noun}} ou {{no_confirm}} ; role_hijacking exige {{persona}}, {{no_limits}} ou {{mode}} ;
- un template malveillant dit ce qu'il veut obtenir, un négatif difficile dit une chose honnête qui ressemble à une attaque.`

const userPrompt = `Écris %d templates, tous de STRUCTURES différentes les unes des autres : %d malveillant(s) et %d négatif(s) difficile(s).

%s%s`

func (a *Author) request(ctx context.Context, n int, avoid []string) ([]draft, error) {
	malicious := int(float64(n)*a.opts.Malicious + 0.5)
	if malicious > n {
		malicious = n
	}
	res, err := a.client.ChatCompletion(ctx,
		llm.WithMessages(
			llm.NewMessage(llm.RoleSystem, fmt.Sprintf(systemPrompt, a.opts.Lang, a.lex.Spec(a.opts.Lang))),
			llm.NewMessage(llm.RoleUser, fmt.Sprintf(userPrompt, n, malicious, n-malicious, avoidClause(avoid), focusClause(a.opts.Focus))),
		),
		llm.WithTemperature(a.opts.Temperature),
	)
	if err != nil {
		return nil, fmt.Errorf("model call: %w", err)
	}
	raw := res.Message().Content()
	if a.opts.Debug {
		a.opts.Log(fmt.Sprintf("--- raw answer (%d bytes) ---\n%s\n--- end ---", len(raw), raw))
	}
	drafts := extract(raw)
	if len(drafts) == 0 {
		return nil, fmt.Errorf("no template block in the answer\n%s", excerpt(raw))
	}
	return drafts, nil
}

var (
	fenceRe = regexp.MustCompile("(?s)```[a-zA-Z0-9_-]*\n(.*?)```")
	nameRe2 = regexp.MustCompile(`(?m)^#\s*name\s*:\s*(\S+)\s*$`)
)

// extract keeps the code blocks that look like templates: a name line and a
// header separator. Illustration blocks in the model's commentary are
// ignored without having to forbid them.
func extract(raw string) []draft {
	var out []draft
	for _, m := range fenceRe.FindAllStringSubmatch(raw, -1) {
		body := m[1]
		nm := nameRe2.FindStringSubmatch(body)
		if nm == nil || !strings.Contains(body, "\n---") {
			continue
		}
		name := strings.TrimSuffix(strings.ToLower(nm[1]), ".tmpl")
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		out = append(out, draft{name: name, src: body})
	}
	return out
}

const repairPrompt = `Le template ci-dessous a été refusé.

Erreur :
%s

Template :
` + "```" + `
%s` + "```" + `

Renvoie-le corrigé dans un seul bloc de code, en ne changeant que ce qui est nécessaire. Conserve la ligne « # name: » et l'en-tête.`

func (a *Author) repair(ctx context.Context, src string, cause error) (string, error) {
	res, err := a.client.ChatCompletion(ctx,
		llm.WithMessages(
			llm.NewMessage(llm.RoleSystem, fmt.Sprintf(systemPrompt, a.opts.Lang, a.lex.Spec(a.opts.Lang))),
			llm.NewMessage(llm.RoleUser, fmt.Sprintf(repairPrompt, cause.Error(), src)),
		),
		llm.WithTemperature(0.2), // repairing is not inventing
	)
	if err != nil {
		return "", fmt.Errorf("repair call: %w", err)
	}
	raw := res.Message().Content()
	if a.opts.Debug {
		a.opts.Log(fmt.Sprintf("--- repair (%d bytes) ---\n%s\n--- end ---", len(raw), raw))
	}
	repaired := extract(raw)
	if len(repaired) == 0 {
		return "", fmt.Errorf("repair answer has no usable block\n%s", excerpt(raw))
	}
	return repaired[0].src, nil
}

func avoidClause(names []string) string {
	if len(names) == 0 {
		return "Couvre des situations variées : demande directe, prétexte technique, jeu de rôle, document ou page web piégée, citation d'attaque à analyser, question de sécurité légitime."
	}
	return "Ces structures existent DÉJÀ, n'en produis aucun équivalent : " + strings.Join(names, ", ") + "."
}

func focusClause(focus string) string {
	if strings.TrimSpace(focus) == "" {
		return ""
	}
	return "\n\nConsigne particulière pour cette campagne : " + strings.TrimSpace(focus)
}

func excerpt(s string) string {
	const max = 1200
	s = strings.TrimSpace(s)
	if s == "" {
		return "  (empty answer)"
	}
	if len(s) > max {
		s = s[:max] + "\n… (truncated)"
	}
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// definitive tells a refusal from a failure: a 4xx other than 408 and 429
// will not pass better with a smaller batch.
func definitive(err error) bool {
	msg := err.Error()
	for _, code := range []string{"400", "401", "403", "404", "422"} {
		if strings.Contains(msg, "http "+code) || strings.Contains(msg, "status "+code) || strings.Contains(msg, " "+code+" ") {
			return true
		}
	}
	return false
}
