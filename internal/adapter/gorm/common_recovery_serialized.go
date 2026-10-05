package gorm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/xolo-gateway/xolo/internal/core/eventql"
	"gorm.io/gorm"
)

// RecoverySerializedOverride is an operator-reviewed replacement. Before binds
// it to the diagnosed value so a stale plan cannot overwrite an intervening edit.
// Before == After acknowledges a legacy ID that is intentionally literal text.
type RecoverySerializedOverride struct {
	Table  string `json:"table"`
	ID     string `json:"id"`
	Column string `json:"column"`
	Before string `json:"before"`
	After  string `json:"after"`
}

var recoverySerializedColumns = [][2]string{
	{"alerts", "query"},
	{"virtual_models", "graph_json"},
	{"personal_virtual_models", "graph_json"},
	{"middlewares", "graph_json"},
	{"events", "attributes"},
}

func recoveryLabelMappings(a *RecoveryArtifact) map[string]map[string]string {
	return map[string]map[string]string{
		"user": a.IDs["users"], "user_id": a.IDs["users"],
		"actor_id": a.IDs["users"], "owner_id": a.IDs["users"],
		"member_user_id": a.IDs["users"], "created_by_user_id": a.IDs["users"],
		"org": a.IDs["organizations"], "org_id": a.IDs["organizations"],
		"organization_id": a.IDs["organizations"], "tenant_id": a.IDs["tenants"],
	}
}

func planSerializedRecovery(db *gorm.DB, a *RecoveryArtifact) ([]RecoverySerializedOverride, []string, error) {
	type key struct{ table, id, column string }
	overrides := map[key]RecoverySerializedOverride{}
	var issues []string
	for _, override := range a.SerializedOverrides {
		k := key{override.Table, override.ID, override.Column}
		if _, duplicate := overrides[k]; duplicate {
			issues = append(issues, fmt.Sprintf("duplicate serialized override: %s[%s].%s", k.table, k.id, k.column))
		}
		overrides[k] = override
	}
	var changes []RecoverySerializedOverride
	for _, spec := range recoverySerializedColumns {
		table, column := spec[0], spec[1]
		if !db.Migrator().HasTable(table) || !db.Migrator().HasColumn(table, column) {
			continue
		}
		var rows []struct{ ID, Value string }
		if err := db.Table(table).Select("id, " + column + " AS value").Order("id").Find(&rows).Error; err != nil {
			return nil, nil, err
		}
		for _, row := range rows {
			k := key{table, row.ID, column}
			change := RecoverySerializedOverride{Table: table, ID: row.ID, Column: column, Before: row.Value}
			override, explicit := overrides[k]
			delete(overrides, k)
			var err error
			if explicit {
				if override.Before != row.Value {
					err = fmt.Errorf("serialized override is stale; regenerate its before value")
				} else {
					change.After = override.After
					if column == "query" {
						_, err = eventql.Compile(change.After)
					} else if !json.Valid([]byte(change.After)) {
						err = fmt.Errorf("serialized override must be valid JSON")
					}
				}
			} else if row.Value == "" {
				change.After = row.Value
			} else if column == "query" {
				change.After, err = eventql.RewriteIdentifiers(row.Value, recoveryLabelMappings(a))
			} else {
				change.After, err = rewriteRecoveryJSON(row.Value, a, column == "graph_json")
			}
			if err != nil {
				issues = append(issues, fmt.Sprintf("%s[%s].%s: %v", table, row.ID, column, err))
				continue
			}
			if change.Before != change.After {
				changes = append(changes, change)
			}
		}
	}
	for k := range overrides {
		issues = append(issues, fmt.Sprintf("unknown serialized override target: %s[%s].%s", k.table, k.id, k.column))
	}
	sort.Strings(issues)
	return changes, issues, nil
}

func rewriteRecoveryJSON(raw string, a *RecoveryArtifact, graph bool) (string, error) {
	if !json.Valid([]byte(raw)) {
		return "", fmt.Errorf("invalid JSON; provide a serialized override")
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	mappings := recoveryLabelMappings(a)
	changed := false
	var walk func(any, string, string, bool) (any, error)
	walk = func(value any, field, path string, structural bool) (any, error) {
		switch v := value.(type) {
		case map[string]any:
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				// Graph topology identifiers are node/edge IDs, not Xolo IDs.
				topology := structural && (k == "id" || k == "source" || k == "target" || k == "sourcePort" || k == "targetPort")
				if topology {
					continue
				}
				child, err := walk(v[k], k, path+"/"+strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1"), graph && (k == "nodes" || k == "edges"))
				if err != nil {
					return nil, err
				}
				v[k] = child
			}
		case []any:
			for i := range v {
				child, err := walk(v[i], field, path+"/"+strconv.Itoa(i), structural)
				if err != nil {
					return nil, err
				}
				v[i] = child
			}
		case string:
			if next, ok := mappings[field][v]; ok && next != v {
				changed = true
				return next, nil
			}
			if graph {
				// String value nodes can carry a literal resource ID. Require an
				// unambiguous mapping across identifier families.
				var next string
				for _, family := range []string{"tenants", "organizations", "users"} {
					mapped, ok := a.IDs[family][v]
					if ok && mapped != v && field == "value" {
						if next != "" && next != mapped {
							return nil, fmt.Errorf("ambiguous ID at %s; provide a serialized override", path)
						}
						next = mapped
					}
				}
				if next != "" {
					changed = true
					return next, nil
				}
				for _, mapping := range a.IDs {
					for old, next := range mapping {
						if old != next && strings.Contains(v, old) {
							return nil, fmt.Errorf("opaque legacy reference at %s; provide a serialized override", path)
						}
					}
				}
			}
		}
		return value, nil
	}
	if _, err := walk(value, "", "", false); err != nil {
		return "", err
	}
	if !changed {
		return raw, nil
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(out.String(), "\n"), nil
}
