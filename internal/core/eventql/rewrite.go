package eventql

import (
	"fmt"
	"strconv"
	"strings"
)

// RewriteIdentifiers updates exact equality/inequality matchers using mappings
// keyed by selector or recognized ID attribute name. Regexes on these selectors
// and attributes containing a retired identifier require an explicit edit.
// Line filters (|=, |~, !=, !~) search the unchanged events.message and are neither
// rewritten nor reported. Other tokens and formatting stay unchanged.
func RewriteIdentifiers(input string, mappings map[string]map[string]string) (string, error) {
	if _, err := Compile(input); err != nil {
		return "", err
	}
	tokens, err := lex(input)
	if err != nil {
		return "", err
	}
	runes := []rune(input)
	var out strings.Builder
	start := 0
	for i := 0; i+2 < len(tokens); i++ {
		name, op, value := tokens[i], tokens[i+1], tokens[i+2]
		if name.kind != tIdent || value.kind != tString {
			continue
		}
		mapping := mappings[name.val]
		if op.kind == tRe || op.kind == tNre {
			for old, next := range mapping {
				if old != next && strings.Contains(value.val, old) {
					return "", fmt.Errorf("regexp on %s contains legacy ID %q; provide a serialized override", name.val, old)
				}
			}
			continue
		}
		if op.kind != tEq && op.kind != tNeq {
			continue
		}
		next, ok := mapping[value.val]
		if !ok || next == value.val {
			continue
		}
		_, end, err := lexString(runes, value.pos)
		if err != nil {
			return "", err
		}
		out.WriteString(string(runes[start:value.pos]))
		out.WriteString(strconv.Quote(next))
		start = end
	}
	if start == 0 {
		return input, nil
	}
	out.WriteString(string(runes[start:]))
	return out.String(), nil
}
