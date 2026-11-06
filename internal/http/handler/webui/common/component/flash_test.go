package component

import (
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/text/language"
)

func TestFlashMessageAgreesWithAFeminineSubject(t *testing.T) {
	fr, es := inLanguage(t, language.French), inLanguage(t, language.Spanish)

	for verb, want := range map[string]string{
		"created": "La règle a été créée.",
		"updated": "La règle a été enregistrée.",
		"deleted": "La règle a été supprimée.",
		"revoked": "La règle a été révoquée.",
	} {
		require.Equal(t, want, flashMessageFeminine(fr, verb, "La règle"), verb)
	}
	require.Equal(t, "Le rôle a été créé.", flashMessage(fr, "created", "Le rôle"))
	require.Equal(t, "La regla se ha creado.", flashMessageFeminine(es, "created", "La regla"))
	require.Equal(t, flashMessage(fr, "deleted", ""), flashMessageFeminine(fr, "deleted", ""))
}
