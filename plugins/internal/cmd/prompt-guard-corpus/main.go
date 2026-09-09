// Command prompt-guard-corpus builds and measures the labelled corpus of the
// prompt-guard plugin.
//
//	go run ./plugins/internal/cmd/prompt-guard-corpus render                 # templates -> corpus.jsonl
//	go run ./plugins/internal/cmd/prompt-guard-corpus eval                   # score the corpus with the rules
//	go run ./plugins/internal/cmd/prompt-guard-corpus inspect -name leak-only
//	go run ./plugins/internal/cmd/prompt-guard-corpus author -lang fr -count 6
//
// Only author calls a model, and only to write template skeletons. The
// words, the obfuscations and the labels come from the code: see the synth
// package for why.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bornholm/genai/llm/provider"
	"github.com/bornholm/genai/llm/provider/env"
	"github.com/bornholm/genai/llm/retry"
	"github.com/xolo-gateway/xolo/plugins/internal/promptguard"
	"github.com/xolo-gateway/xolo/plugins/internal/promptguard/synth"

	_ "github.com/bornholm/genai/llm/provider/mistral"
	_ "github.com/bornholm/genai/llm/provider/openai"
	_ "github.com/bornholm/genai/llm/provider/openrouter"
)

const (
	defaultTemplates = "plugins/internal/promptguard/synth/data/templates"
	defaultLexicons  = "plugins/internal/promptguard/synth/data/lexicons"
	defaultCorpus    = "plugins/internal/promptguard/data/corpus.jsonl"
	defaultBenign    = "plugins/internal/complexity/data/corpus.jsonl"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "render":
		err = cmdRender(os.Args[2:])
	case "eval":
		err = cmdEval(os.Args[2:])
	case "inspect":
		err = cmdInspect(os.Args[2:])
	case "author":
		err = cmdAuthor(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `prompt-guard-corpus <command> [options]

  render    fill the templates and write the labelled corpus
  eval      score a corpus with the rules and report precision/recall
  inspect   render one template a few times, for proofreading
  author    have a model write new templates, validated before being kept`)
}

func loadSources(templatesDir, lexiconsDir string) (synth.Lexicon, []*synth.Template, error) {
	lex, err := synth.LoadLexiconDir(lexiconsDir)
	if err != nil {
		return nil, nil, fmt.Errorf("lexicons: %w", err)
	}
	tpls, err := synth.LoadTemplateDir(templatesDir, lex)
	if err != nil {
		return nil, nil, fmt.Errorf("templates: %w", err)
	}
	return lex, tpls, nil
}

func cmdRender(args []string) error {
	fs := flag.NewFlagSet("render", flag.ExitOnError)
	templates := fs.String("templates", defaultTemplates, "templates directory")
	lexicons := fs.String("lexicons", defaultLexicons, "lexicons directory")
	benign := fs.String("benign", defaultBenign, "JSONL of ordinary requests for {{benign}} (empty to skip)")
	out := fs.String("out", defaultCorpus, "output corpus")
	per := fs.Int("per-template", 40, "variants per template")
	obf := fs.Float64("obfuscated", 0.25, "share of attacks that also get an obfuscated copy")
	seed := fs.Int64("seed", 1, "random seed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	lex, tpls, err := loadSources(*templates, *lexicons)
	if err != nil {
		return err
	}
	r := synth.NewRenderer(lex, tpls)
	if *benign != "" {
		texts, err := synth.ReadTexts(*benign)
		if err != nil {
			return fmt.Errorf("benign corpus: %w", err)
		}
		r.WithBenign(texts)
	}
	samples, err := r.Render(synth.Options{PerTemplate: *per, ObfuscatedShare: *obf, Seed: *seed})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := synth.WriteJSONL(f, samples); err != nil {
		return err
	}
	fmt.Printf("%d templates -> %d samples in %s\n", len(tpls), len(samples), *out)
	printCounts(samples)
	return nil
}

func printCounts(samples []synth.Sample) {
	var mal, obf int
	bySplit := map[string]int{}
	byLang := map[string]int{}
	for _, s := range samples {
		if s.Malicious {
			mal++
		}
		if s.Obfuscation != "" {
			obf++
		}
		bySplit[s.Split]++
		byLang[s.Language]++
	}
	fmt.Printf("  malicious %d, benign %d, obfuscated %d\n", mal, len(samples)-mal, obf)
	fmt.Printf("  splits: train %d, validation %d, test %d\n", bySplit["train"], bySplit["validation"], bySplit["test"])
	for _, l := range sortedKeys(byLang) {
		fmt.Printf("  %s: %d\n", l, byLang[l])
	}
}

func cmdInspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ExitOnError)
	templates := fs.String("templates", defaultTemplates, "templates directory")
	lexicons := fs.String("lexicons", defaultLexicons, "lexicons directory")
	name := fs.String("name", "", "template name (all when empty)")
	lang := fs.String("lang", "", "restrict to a language")
	n := fs.Int("n", 3, "variants to show per template")
	seed := fs.Int64("seed", time.Now().UnixNano(), "random seed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	lex, tpls, err := loadSources(*templates, *lexicons)
	if err != nil {
		return err
	}
	var chosen []*synth.Template
	for _, t := range tpls {
		if (*name == "" || t.Name == *name) && (*lang == "" || t.Lang == *lang) {
			chosen = append(chosen, t)
		}
	}
	if len(chosen) == 0 {
		return fmt.Errorf("no template matches")
	}
	r := synth.NewRenderer(lex, chosen)
	// Rendering all templates once through the public API keeps inspect on
	// the same code path as render.
	samples, err := r.Render(synth.Options{PerTemplate: *n, ObfuscatedShare: 0, Seed: *seed})
	if err != nil {
		return err
	}
	g := promptguard.New(promptguard.Options{})
	last := ""
	for _, s := range samples {
		if s.Family != last {
			last = s.Family
			fmt.Printf("\n── %s/%s  malicious=%t  labels=%s  segment=%s  split=%s\n", s.Language, s.Family, s.Malicious, strings.Join(s.Labels, ","), s.Source, s.Split)
		}
		a := g.Assess([]promptguard.Segment{{Kind: promptguard.SegmentKind(s.Source), Text: s.Text}})
		fmt.Printf("  [%.2f %-28s] %s\n", a.Risk, a.TopRule, strings.ReplaceAll(s.Text, "\n", "⏎ "))
	}
	return nil
}

func cmdEval(args []string) error {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	corpus := fs.String("corpus", defaultCorpus, "corpus to score")
	threshold := fs.Float64("threshold", 0.5, "risk at or above which a sample counts as flagged")
	split := fs.String("split", "", "restrict to train, validation or test")
	showFP := fs.Int("show-fp", 10, "false positives to print")
	showFN := fs.Int("show-fn", 10, "false negatives to print")
	if err := fs.Parse(args); err != nil {
		return err
	}
	samples, err := synth.ReadJSONLFile(*corpus)
	if err != nil {
		return err
	}
	g := promptguard.New(promptguard.Options{})

	total := counts{}
	byLang := map[string]*counts{}
	byFamily := map[string]*counts{}
	byObf := map[string]*counts{}
	var fps, fns []string
	unmatched := 0 // attacks no rule sees: what a model would have to learn
	n := 0
	start := time.Now()
	for _, s := range samples {
		if *split != "" && s.Split != *split {
			continue
		}
		n++
		a := g.Assess([]promptguard.Segment{{Kind: promptguard.SegmentKind(s.Source), Text: s.Text}})
		flagged := a.Risk >= *threshold
		tally := func(c *counts) {
			switch {
			case s.Malicious && flagged:
				c.tp++
			case s.Malicious && !flagged:
				c.fn++
			case !s.Malicious && flagged:
				c.fp++
			default:
				c.tn++
			}
		}
		tally(&total)
		tally(get(byLang, s.Language))
		tally(get(byFamily, s.Language+"/"+s.Family))
		if s.Obfuscation != "" {
			tally(get(byObf, s.Obfuscation))
		}
		if s.Malicious && len(a.Matches) == 0 {
			unmatched++
		}
		if !s.Malicious && flagged {
			fps = append(fps, fmt.Sprintf("  %.2f %-28s %s/%s: %s", a.Risk, a.TopRule, s.Language, s.Family, oneLine(s.Text)))
		}
		if s.Malicious && !flagged {
			fns = append(fns, fmt.Sprintf("  %.2f %-28s %s/%s: %s", a.Risk, a.TopRule, s.Language, s.Family, oneLine(s.Text)))
		}
	}
	elapsed := time.Since(start)
	if n == 0 {
		return fmt.Errorf("no sample selected")
	}
	report := func(label string, c *counts) {
		p, r, f := prf(c.tp, c.fp, c.fn)
		fmt.Printf("%-36s n=%-5d P=%5.1f%% R=%5.1f%% F1=%5.1f%%  FP=%d FN=%d\n", label, c.tp+c.fp+c.fn+c.tn, p*100, r*100, f*100, c.fp, c.fn)
	}
	fmt.Printf("corpus %s, threshold %.2f, %d samples in %s (%.0f µs/sample)\n\n", *corpus, *threshold, n, elapsed.Round(time.Millisecond), float64(elapsed.Microseconds())/float64(n))
	report("all", &total)
	for _, l := range sortedKeys(byLang) {
		report("lang "+l, byLang[l])
	}
	fmt.Println()
	for _, o := range sortedKeys(byObf) {
		report("obfuscation "+o, byObf[o])
	}
	fmt.Printf("\nattacks matched by no rule at all: %d\n\n", unmatched)
	fmt.Println("per family (worst first):")
	fams := sortedKeys(byFamily)
	sort.Slice(fams, func(i, j int) bool {
		_, _, fi := prf(byFamily[fams[i]].tp, byFamily[fams[i]].fp, byFamily[fams[i]].fn)
		_, _, fj := prf(byFamily[fams[j]].tp, byFamily[fams[j]].fp, byFamily[fams[j]].fn)
		if fi != fj {
			return fi < fj
		}
		return fams[i] < fams[j]
	})
	for _, f := range fams {
		c := byFamily[f]
		if c.tp+c.fn > 0 {
			report("  "+f, c)
		} else {
			fmt.Printf("  %-34s n=%-5d benign, FP=%d\n", f, c.fp+c.tn, c.fp)
		}
	}
	printSome("\nfalse positives:", fps, *showFP)
	printSome("\nfalse negatives:", fns, *showFN)
	return nil
}

func cmdAuthor(args []string) error {
	fs := flag.NewFlagSet("author", flag.ExitOnError)
	envFile := fs.String("env-file", ".env", "environment file holding GENAI_CHAT_COMPLETION_*")
	templates := fs.String("templates", defaultTemplates, "templates directory, avoided and used for the similarity check")
	out := fs.String("out", "", "where new templates go (<out>/<lang>); default: the templates directory. A separate directory makes a sealed set, also avoided if it exists")
	focus := fs.String("focus", "", "extra instruction for this campaign, e.g. what kind of negatives to write")
	lexicons := fs.String("lexicons", defaultLexicons, "lexicons directory")
	lang := fs.String("lang", "fr", "language of the templates to write")
	count := fs.Int("count", 6, "templates to obtain")
	batch := fs.Int("batch", 3, "templates requested per call")
	malicious := fs.Float64("malicious", 0.5, "share of malicious templates")
	temp := fs.Float64("temperature", 0.9, "sampling temperature")
	repairs := fs.Int("max-repairs", 3, "repair attempts per invalid template")
	quiet := fs.Bool("quiet", false, "no progress output")
	debug := fs.Bool("debug", false, "print raw model answers")
	if err := fs.Parse(args); err != nil {
		return err
	}
	lex, tpls, err := loadSources(*templates, *lexicons)
	if err != nil {
		return err
	}
	if _, ok := lex[*lang]; !ok {
		return fmt.Errorf("no lexicon for %q", *lang)
	}
	ctx := context.Background()
	client, err := provider.Create(ctx, env.With("GENAI_", *envFile))
	if err != nil {
		return fmt.Errorf("model configuration (%s): %w", *envFile, err)
	}
	if *out == "" {
		*out = *templates
	} else if _, err := os.Stat(*out); err == nil {
		// A sealed set already started: avoid what it holds too.
		more, err := synth.LoadTemplateDir(*out, lex)
		if err != nil {
			return fmt.Errorf("existing templates in %s: %w", *out, err)
		}
		tpls = append(tpls, more...)
	}
	dir := filepath.Join(*out, *lang)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	opts := synth.AuthorOptions{
		Lang: *lang, Count: *count, Batch: *batch, Malicious: *malicious,
		Temperature: *temp, MaxRepairs: *repairs, MaxSimilarity: 0.6, Focus: *focus,
		Verbose: !*quiet, Debug: *debug,
		Log: func(s string) { fmt.Println(s) },
		Emit: func(t *synth.Template) error {
			return os.WriteFile(filepath.Join(dir, t.Name+".tmpl"), []byte(t.Source), 0o644)
		},
	}
	a := synth.NewAuthor(retry.NewClient(client, 2*time.Second, 3), lex, tpls, opts)
	results, err := a.Run(ctx)
	fmt.Printf("%d template(s) written to %s\n", len(results), dir)
	return err
}

type counts struct{ tp, fp, fn, tn int }

func get(m map[string]*counts, k string) *counts {
	if c, ok := m[k]; ok {
		return c
	}
	c := &counts{}
	m[k] = c
	return c
}

func prf(tp, fp, fn int) (p, r, f float64) {
	if tp+fp > 0 {
		p = float64(tp) / float64(tp+fp)
	}
	if tp+fn > 0 {
		r = float64(tp) / float64(tp+fn)
	}
	if p+r > 0 {
		f = 2 * p * r / (p + r)
	}
	return
}

func printSome(title string, lines []string, n int) {
	if len(lines) == 0 || n == 0 {
		return
	}
	fmt.Printf("%s (%d)\n", title, len(lines))
	rand.New(rand.NewSource(1)).Shuffle(len(lines), func(i, j int) { lines[i], lines[j] = lines[j], lines[i] })
	for i, l := range lines {
		if i >= n {
			fmt.Printf("  … %d more\n", len(lines)-n)
			break
		}
		fmt.Println(l)
	}
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", "⏎ ")
	if len(s) > 140 {
		s = s[:140] + "…"
	}
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
