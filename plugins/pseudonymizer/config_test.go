package main

import (
	"slices"
	"testing"

	goanon "github.com/bornholm/go-anon"
	"github.com/bornholm/go-anon/pkg/ner"
)

func TestParseConfig_Engine(t *testing.T) {
	cfg, err := parseConfig(`{"strategy":"tag"}`)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.Engine != engineCRF {
		t.Errorf("a config without engine gets %q, want crf", cfg.Engine)
	}
	if cfg, err := parseConfig(`{"engine":"indecis"}`); err != nil || cfg.Engine != engineIndecis {
		t.Errorf("engine indecis = %q, %v", cfg.Engine, err)
	}
	if _, err := parseConfig(`{"engine":"spacy"}`); err == nil {
		t.Errorf("an unknown engine was accepted")
	}
}

func TestParseConfig_Bias(t *testing.T) {
	cfg, err := parseConfig(`{"strategy":"tag"}`)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.Bias != defaultBias {
		t.Errorf("a config without bias gets %v, want the default %v", cfg.Bias, defaultBias)
	}
	if cfg, err := parseConfig(`{"bias":0}`); err != nil || cfg.Bias != 0 {
		t.Errorf("bias 0 = %v, %v; want 0 kept", cfg.Bias, err)
	}
	for _, raw := range []string{`{"bias":-1}`, `{"bias":10.5}`} {
		if _, err := parseConfig(raw); err == nil {
			t.Errorf("%s accepted, want an error", raw)
		}
	}
}

// A config written for one engine keeps blocking the same persons with the
// other: PER and PERSON name the same type.
func TestPostFilters_BlocklistAppliesToBothPersonTypes(t *testing.T) {
	cfg := defaultConfig()
	cfg.Blocklist = map[string][]string{"PER": {"Monsieur"}}

	got := []ner.Entity{
		{Type: typePerson, Text: "Monsieur", Start: 0, End: 8, Confidence: 1},
		{Type: goanon.TypePER, Text: "Monsieur", Start: 9, End: 17, Confidence: 1},
		{Type: goanon.TypeLOC, Text: "Monsieur", Start: 18, End: 26, Confidence: 1},
	}
	for _, f := range postFilters(cfg) {
		got = f("Monsieur Monsieur Monsieur", got)
	}
	if len(got) != 1 || got[0].Type != goanon.TypeLOC {
		t.Errorf("filtered entities = %+v, want the LOC one only", got)
	}
}

func TestTypeAliases(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want []string
	}{
		{"PER", []string{"PER", "PERSON"}},
		{"PERSON", []string{"PERSON", "PER"}},
		{"LOC", []string{"LOC"}},
	} {
		if got := typeAliases(tt.in); !slices.Equal(got, tt.want) {
			t.Errorf("typeAliases(%s) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestSkipTypesOf(t *testing.T) {
	for _, tt := range []struct {
		name                 string
		base, shown, checked []string
		want                 []string
	}{
		{"unchecked types are skipped", nil, []string{"PER", "LOC", "EMAIL"}, []string{"PER"}, []string{"LOC", "EMAIL"}},
		{"checking a type pseudonymizes it again", []string{"LOC"}, []string{"PER", "LOC"}, []string{"PER", "LOC"}, nil},
		// The page of the CRF does not show DATETIME: a type skipped for
		// the indecis engine stays skipped.
		{"types not shown are kept", []string{"DATETIME"}, []string{"PER", "LOC"}, []string{"PER", "LOC"}, []string{"DATETIME"}},
		// PERSON skipped for indecis, PER checked on the CRF page: the
		// person is pseudonymized again whatever the engine.
		{"an alias checked clears the other", []string{"PERSON"}, []string{"PER"}, []string{"PER"}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := skipTypesOf(tt.base, tt.shown, tt.checked); !slices.Equal(got, tt.want) {
				t.Errorf("skipTypesOf = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTypeSkipped(t *testing.T) {
	if !typeSkipped([]string{"PER"}, typePerson) || !typeSkipped([]string{"PERSON"}, goanon.TypePER) {
		t.Errorf("PER and PERSON do not stand for each other")
	}
	if typeSkipped([]string{"PER"}, goanon.TypeLOC) {
		t.Errorf("LOC reads as skipped")
	}
}

// Skipping every type offered pseudonymizes none: go-anon reads nil
// EntityTypes as "every type".
func TestAnonymizerConfig_EverySkippedTypeLeavesNone(t *testing.T) {
	for _, engine := range []string{engineCRF, engineIndecis} {
		cfg := defaultConfig()
		cfg.Engine = engine
		for _, typ := range allEntityTypes {
			cfg.SkipTypes = append(cfg.SkipTypes, string(typ))
		}
		if got := anonymizerConfig(cfg).EntityTypes; got == nil || len(got) != 0 {
			t.Errorf("%s: EntityTypes = %#v, want an empty non-nil list", engine, got)
		}
	}
	cfg := defaultConfig()
	cfg.Engine = engineIndecis
	cfg.SkipTypes = []string{"PER"}
	for _, typ := range anonymizerConfig(cfg).EntityTypes {
		if typ == typePerson || typ == goanon.TypePER {
			t.Errorf("PER skipped, but %s is pseudonymized", typ)
		}
	}
	if anonymizerConfig(defaultConfig()).EntityTypes != nil {
		t.Errorf("nothing skipped: EntityTypes must stay nil, every type")
	}
}

// The leak scan skips the same types as the anonymizer: PER and PERSON
// stand for each other there too.
func TestKeepDetectedTypes_Aliases(t *testing.T) {
	got := keepDetectedTypes([]ner.Entity{{Type: typePerson}, {Type: goanon.TypeLOC}}, []string{"PER"})
	if len(got) != 1 || got[0].Type != goanon.TypeLOC {
		t.Errorf("kept %+v, want the LOC entity only", got)
	}
}
