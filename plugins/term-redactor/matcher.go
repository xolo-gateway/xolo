package main

import (
	"sort"
	"strings"
	"unicode"
)

// termEntry is one redactable term fetched from the external API.
type termEntry struct {
	UUID     string `json:"uuid"`
	Name     string `json:"name"`
	Category string `json:"category,omitempty"`
}

// trieNode is one node of the term-matching trie, keyed by lowercase rune.
type trieNode struct {
	children map[rune]*trieNode
	uuid     string // set when a term ends at this node
	isEnd    bool
}

func newTrieNode() *trieNode {
	return &trieNode{children: make(map[rune]*trieNode)}
}

// matcher performs case-insensitive, Unicode word-boundary, longest-match-first
// matching of a fixed set of terms against arbitrary text.
//
// Go's regexp \b is ASCII-only ([0-9A-Za-z_]), which mishandles accented
// French proper nouns ("Résidence", "François") — exactly the case this
// plugin exists for. A hand-rolled trie walk with a manual, unicode.IsLetter
// based boundary check avoids that, and is bounded by the longest term's
// length rather than by the number of terms, which matters for lists with
// thousands of entries.
type matcher struct {
	root *trieNode
}

// buildMatcher indexes terms (by lowercase name) into a trie. Entries with an
// empty name or uuid are skipped (an empty uuid has no token to redact to —
// same exclusion buildTokenTables applies, kept consistent so every match the
// trie can produce has a corresponding token); when several entries share the
// exact same name, the last one wins (fetched lists are expected to be
// deduplicated upstream).
func buildMatcher(terms []termEntry) *matcher {
	root := newTrieNode()
	for _, t := range terms {
		if t.UUID == "" {
			continue
		}
		name := []rune(strings.ToLower(strings.TrimSpace(t.Name)))
		if len(name) == 0 {
			continue
		}
		node := root
		for _, r := range name {
			child, ok := node.children[r]
			if !ok {
				child = newTrieNode()
				node.children[r] = child
			}
			node = child
		}
		node.isEnd = true
		node.uuid = t.UUID
	}
	return &matcher{root: root}
}

// isWordRune decides what counts as "inside a word" for boundary checks.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// matchSpan is a candidate or accepted match, expressed as rune offsets into
// the original (not lowercased) text.
type matchSpan struct {
	start, end int
	uuid       string
}

// findMatches walks the trie from every word-boundary start position in
// text, collects every boundary-valid completion, then resolves overlaps by
// leftmost-longest selection: sorted by (start asc, length desc), a match is
// accepted only if it starts at or after the end of the last accepted match.
//
// This is what makes "Résidence Les Tilleuls" win over a separately listed
// "Les Tilleuls" contained within it: the longer, earlier-starting span is
// accepted first and consumes the text the shorter one would have matched.
func (m *matcher) findMatches(runes []rune) []matchSpan {
	n := len(runes)

	var candidates []matchSpan
	for i := 0; i < n; i++ {
		if i > 0 && isWordRune(runes[i-1]) {
			continue
		}
		node := m.root
		for j := i; j < n; j++ {
			child, ok := node.children[unicode.ToLower(runes[j])]
			if !ok {
				break
			}
			node = child
			end := j + 1
			if node.isEnd && (end == n || !isWordRune(runes[end])) {
				candidates = append(candidates, matchSpan{start: i, end: end, uuid: node.uuid})
			}
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	sort.Slice(candidates, func(a, b int) bool {
		if candidates[a].start != candidates[b].start {
			return candidates[a].start < candidates[b].start
		}
		return (candidates[a].end - candidates[a].start) > (candidates[b].end - candidates[b].start)
	})

	selected := make([]matchSpan, 0, len(candidates))
	nextAllowedStart := 0
	for _, c := range candidates {
		if c.start < nextAllowedStart {
			continue
		}
		selected = append(selected, c)
		nextAllowedStart = c.end
	}
	return selected
}

// Redact replaces every matched term in text with tokenFor(uuid), returning
// the redacted text and a token->originalSubstring map covering only the
// terms actually found in this call (not the whole indexed term list).
func (m *matcher) Redact(text string, tokenFor func(uuid string) string) (string, map[string]string) {
	if text == "" {
		return text, nil
	}
	runes := []rune(text)
	spans := m.findMatches(runes)
	if len(spans) == 0 {
		return text, nil
	}

	used := make(map[string]string, len(spans))
	var b strings.Builder
	last := 0
	for _, sp := range spans {
		b.WriteString(string(runes[last:sp.start]))
		token := tokenFor(sp.uuid)
		b.WriteString(token)
		used[token] = string(runes[sp.start:sp.end])
		last = sp.end
	}
	b.WriteString(string(runes[last:]))
	return b.String(), used
}
