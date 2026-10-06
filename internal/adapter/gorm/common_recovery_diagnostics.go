package gorm

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// Keep the report's string interface, grouping distinct values by location and
// category. Details identify mapping sources and targets without losing either.
type recoveryDiagnosticKey struct{ category, location string }
type recoveryDiagnostics map[recoveryDiagnosticKey]map[string]string

func (d recoveryDiagnostics) add(category, location, value, detail string) {
	key := recoveryDiagnosticKey{category: category, location: location}
	if d[key] == nil {
		d[key] = map[string]string{}
	}
	if detail == "" {
		detail = strconv.Quote(value)
	}
	d[key][value] = detail
}

func (d recoveryDiagnostics) issues() []string {
	issues := make([]string, 0, len(d))
	for key, values := range d {
		ordered := make([]string, 0, len(values))
		for value := range values {
			ordered = append(ordered, value)
		}
		sort.Strings(ordered)
		examples := make([]string, 0, min(5, len(ordered)))
		for _, value := range ordered[:min(5, len(ordered))] {
			examples = append(examples, values[value])
		}
		issue := fmt.Sprintf("%s: %s: %d distinct values; examples: %s",
			key.category, key.location, len(values), strings.Join(examples, ", "))
		if omitted := len(values) - len(examples); omitted > 0 {
			issue += fmt.Sprintf("; %d omitted", omitted)
		}
		issues = append(issues, issue)
	}
	sort.Strings(issues)
	return issues
}

func (ref recoveryReference) location() string {
	scope := "references " + ref.family
	if ref.where != "" {
		scope += "; " + ref.where
	}
	return ref.table + "." + ref.column + " [" + scope + "]"
}

func diagnoseRecoveryMappings(ids map[string][]string, a *RecoveryArtifact, diagnostics recoveryDiagnostics) {
	type source struct{ family, id string }
	targets := map[string][]source{}
	for family, values := range ids {
		if _, ok := a.IDs[family]; !ok {
			diagnostics.add("missing mapping family", "ids", family, "")
		}
		for _, old := range values {
			if _, ok := a.IDs[family][old]; !ok {
				diagnostics.add("missing mapping key", family+".id", old, "")
			}
		}
	}
	for family, mapping := range a.IDs {
		values, known := ids[family]
		if !known {
			diagnostics.add("unexpected mapping family", "ids", family, "")
		}
		present := make(map[string]bool, len(values))
		for _, value := range values {
			present[value] = true
		}
		for old, next := range mapping {
			detail := fmt.Sprintf("%q -> %q", old, next)
			if !present[old] {
				diagnostics.add("unexpected mapping key", family+".id", old, detail)
			}
			if _, err := model.ParseTenantID(next); err != nil {
				diagnostics.add("invalid UUID", family+".id", old, detail)
			}
			if _, err := model.ParseTenantID(old); err == nil && old != next {
				diagnostics.add("existing UUID must be preserved", family+".id", old, detail)
			}
			targets[next] = append(targets[next], source{family: family, id: old})
		}
	}
	for target, sources := range targets {
		if len(sources) < 2 {
			continue
		}
		// Include every source, grouped by its table just like other mapping
		// issues. This also caps examples when many different targets collide.
		for _, src := range sources {
			diagnostics.add("duplicate target UUID", src.family+".id", src.id,
				fmt.Sprintf("%q -> %q", src.id, target))
		}
	}
}
