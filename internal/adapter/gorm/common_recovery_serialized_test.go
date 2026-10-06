package gorm

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecoveryGraphReferences(t *testing.T) {
	const userUUID = "11111111-1111-4111-8111-111111111111"
	const orgUUID = "22222222-2222-4222-8222-222222222222"
	const unchanged = "33333333-3333-4333-8333-333333333333"
	a := &RecoveryArtifact{IDs: map[string]map[string]string{
		"users":         {"ababa": userUUID, "bab": userUUID, "a.b/c:~é": userUUID, "shared": userUUID, unchanged: unchanged},
		"organizations": {"shared": orgUUID},
		"tenants":       {},
	}}
	r := newRecoveryJSONRewriter(a)
	for _, tc := range []struct{ name, raw, want, issue string }{
		{name: "exact overlapping value", raw: `{"value":"ababa"}`, want: `{"value":"` + userUUID + `"}`},
		{name: "exact punctuation", raw: `{"value":"a.b/c:~é"}`, want: `{"value":"` + userUUID + `"}`},
		{name: "embedded punctuation", raw: `{"script":"lookup(a.b/c:~é)"}`, issue: "opaque legacy reference at /script"},
		{name: "overlapping substring", raw: `{"script":"zzabababzzz"}`, issue: "opaque legacy reference at /script"},
		{name: "suffix match", raw: `{"script":"abab"}`, issue: "opaque legacy reference at /script"},
		{name: "exact unknown field", raw: `{"custom":"ababa"}`, issue: "opaque legacy reference at /custom"},
		{name: "ambiguous value", raw: `{"value":"shared"}`, issue: "ambiguous ID at /value"},
		{name: "recognized family", raw: `{"user_id":"shared"}`, want: `{"user_id":"` + userUUID + `"}`},
		{name: "unchanged UUID", raw: `{ "value":"` + unchanged + `" }`, want: `{ "value":"` + unchanged + `" }`},
		{name: "embedded unchanged UUID", raw: `{"script":"lookup(` + unchanged + `)"}`, want: `{"script":"lookup(` + unchanged + `)"}`},
		{name: "escaped JSON path", raw: `{"config/~":["ababa"]}`, issue: "opaque legacy reference at /config~1~0/0"},
		{name: "unmatched", raw: `{ "script": "abbbaa" }`, want: `{ "script": "abbbaa" }`},
		{name: "precision", raw: `{"value":"ababa","n":9007199254740993}`, want: `{"n":9007199254740993,"value":"` + userUUID + `"}`},
		{name: "invalid JSON", raw: `{"value":`, issue: "invalid JSON"},
		{
			name: "topology",
			raw:  `{"nodes":[{"id":"ababa","data":{"value":"ababa"}}],"edges":[{"id":"shared","source":"ababa","target":"a.b/c:~é","sourcePort":"bab","targetPort":"shared"}]}`,
			want: `{"edges":[{"id":"shared","source":"ababa","sourcePort":"bab","target":"a.b/c:~é","targetPort":"shared"}],"nodes":[{"data":{"value":"` + userUUID + `"},"id":"ababa"}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.rewrite(tc.raw, true)
			if tc.issue != "" {
				require.ErrorContains(t, err, tc.issue)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
	// Families may agree on the same target; this is not ambiguous.
	a.IDs["organizations"]["shared"] = userUUID
	agreed, err := newRecoveryJSONRewriter(a).rewrite(`{"value":"shared"}`, true)
	require.NoError(t, err)
	require.Equal(t, `{"value":"`+userUUID+`"}`, agreed)
	// Event attributes keep opaque text, including legacy IDs.
	literal := `{"message":"ababa"}`
	got, err := r.rewrite(literal, false)
	require.NoError(t, err)
	require.Equal(t, literal, got)
}

func FuzzRecoverySubstringMatcher(f *testing.F) {
	f.Add("ababa\nbab", "abab")
	f.Add("he\nshe\nhis\nhers", "ushers")
	f.Add("a.b/c:~é\nx-y", "lookup(a.b/c:~é)")
	f.Add("abc\nbcx\nc", "bcd")
	f.Add("", "anything")
	f.Fuzz(func(t *testing.T, joined, value string) {
		patterns := map[string]bool{}
		want := false
		for _, pattern := range strings.Split(joined, "\n") {
			patterns[pattern] = true
			want = want || strings.Contains(value, pattern)
		}
		require.Equal(t, want, newRecoverySubstringMatcher(patterns).contains(value))
	})
}

func benchmarkRecoveryArtifact(count int) *RecoveryArtifact {
	a := &RecoveryArtifact{IDs: map[string]map[string]string{"users": {}, "organizations": {}, "tenants": {}}}
	for i := range count {
		a.IDs["users"][fmt.Sprintf("legacy-user-%08d", i)] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
	}
	return a
}

// Mapping size and graph volume vary independently. Each node contains many
// unmatched strings: the former nested substring loop paid per ID for each one.
func BenchmarkRecoveryGraphs(b *testing.B) {
	for _, ids := range []int{10, 1000, 10000} {
		for _, nodes := range []int{10, 1000} {
			for _, references := range []bool{false, true} {
				b.Run(fmt.Sprintf("ids=%d/nodes=%d/references=%t", ids, nodes, references), func(b *testing.B) {
					r := newRecoveryJSONRewriter(benchmarkRecoveryArtifact(ids))
					value := "ordinary literal"
					if references {
						value = "legacy-user-00000000"
					}
					node := fmt.Sprintf(`{"id":"node","type":"plugin","data":{"value":%q,"notes":[%s"tail"],"script":"some unmatched script configuration"}}`,
						value, strings.Repeat(`"unmatched descriptive string",`, 16))
					raw := `{"nodes":[` + strings.Repeat(node+",", nodes-1) + node + `],"edges":[]}`
					b.SetBytes(int64(len(raw)))
					b.ReportAllocs()
					for b.Loop() {
						if _, err := r.rewrite(raw, true); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		}
	}
}

func BenchmarkRecoveryGraphMatcherBuild(b *testing.B) {
	for _, ids := range []int{10, 1000, 10000} {
		b.Run(fmt.Sprintf("ids=%d", ids), func(b *testing.B) {
			a := benchmarkRecoveryArtifact(ids)
			b.ReportAllocs()
			for b.Loop() {
				newRecoveryJSONRewriter(a)
			}
		})
	}
}
