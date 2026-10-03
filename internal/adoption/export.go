// Package adoption defines the portable inventory envelope. It never restores
// application data or changes resource identifiers.
package adoption

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

const Version = "xolo-adoption/1"
const Contract = "0.1.0-draft.1"

type Record struct {
	Family string `json:"family"`
	model.CommonItem
}
type Payload struct {
	Version  string   `json:"version"`
	Source   string   `json:"source"`
	Contract string   `json:"contract"`
	Cursor   string   `json:"c0"`
	Families []string `json:"families"`
	Records  []Record `json:"records"`
	Count    int      `json:"count"`
	Complete bool     `json:"complete"`
}
type Envelope struct {
	Payload json.RawMessage `json:"payload"`
	SHA256  string          `json:"sha256"`
}

var Families = []string{"tenant", "tenant_domain", "organization", "member", "organization_membership"}

func Encode(p Payload) ([]byte, error) {
	p.Version = Version
	p.Contract = Contract
	p.Families = append([]string{}, Families...)
	p.Count = len(p.Records)
	p.Complete = true
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return json.Marshal(Envelope{Payload: raw, SHA256: hex.EncodeToString(digest[:])})
}
func decode(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing export data")
	}
	return nil
}

// Decode verifies SHA-256 over the exact UTF-8 bytes of the payload JSON value
// (including its own braces, excluding envelope whitespace). Consumers must
// stage the entire verified inventory and replay C0 before making it authoritative.
func Decode(raw []byte) (Payload, error) {
	var e Envelope
	var p Payload
	if err := decode(raw, &e); err != nil {
		return p, err
	}
	digest := sha256.Sum256(e.Payload)
	if hex.EncodeToString(digest[:]) != e.SHA256 {
		return p, errors.New("export checksum mismatch")
	}
	if err := decode(e.Payload, &p); err != nil {
		return p, err
	}
	if p.Version != Version || p.Contract != Contract || p.Source == "" || p.Cursor == "" || !p.Complete || p.Count != len(p.Records) || len(p.Families) != len(Families) {
		return p, errors.New("incomplete or incompatible export")
	}
	for i, f := range Families {
		if p.Families[i] != f {
			return p, errors.New("incomplete export families")
		}
	}
	seen := map[string]bool{}
	for _, r := range p.Records {
		allowed := false
		for _, f := range Families {
			allowed = allowed || r.Family == f
		}
		key, _ := json.Marshal(r.Key)
		id := r.Family + string(key)
		if !allowed || seen[id] || r.ETag == "" || len(r.Representation) == 0 || string(r.Representation) == "null" {
			return p, fmt.Errorf("invalid export record")
		}
		seen[id] = true
		if _, err := model.ParseTenantID(r.Key.TenantID); err != nil {
			return p, err
		}
	}
	if err := validateRecords(p.Records); err != nil {
		return p, err
	}
	return p, nil
}

func validateRecords(records []Record) error {
	tenants, organizations, members := map[string]bool{}, map[string]string{}, map[string]string{}
	for _, r := range records {
		k := r.Key
		var rep map[string]json.RawMessage
		if err := json.Unmarshal(r.Representation, &rep); err != nil || rep == nil {
			return errors.New("invalid record representation")
		}
		var status model.Status
		if json.Unmarshal(rep["status"], &status) != nil || !status.Valid() {
			return errors.New("invalid record status")
		}
		switch r.Family {
		case "tenant":
			if k.OrganizationID != "" || k.MemberID != "" || k.Hostname != "" {
				return errors.New("invalid tenant key")
			}
			tenants[k.TenantID] = true
		case "organization":
			if _, err := model.ParseOrgID(k.OrganizationID); err != nil {
				return err
			}
			if k.MemberID != "" || k.Hostname != "" || organizations[k.OrganizationID] != "" {
				return errors.New("invalid organization key")
			}
			organizations[k.OrganizationID] = k.TenantID
		case "member":
			if _, err := model.ParseUserID(k.MemberID); err != nil {
				return err
			}
			if k.OrganizationID != "" || k.Hostname != "" || members[k.MemberID] != "" {
				return errors.New("invalid member key")
			}
			members[k.MemberID] = k.TenantID
		case "tenant_domain":
			host, err := model.NormalizeHostname(k.Hostname)
			if err != nil || host != k.Hostname || k.OrganizationID != "" || k.MemberID != "" {
				return errors.New("invalid domain key")
			}
		case "organization_membership":
			if _, err := model.ParseOrgID(k.OrganizationID); err != nil {
				return err
			}
			if _, err := model.ParseUserID(k.MemberID); err != nil {
				return err
			}
			if k.Hostname != "" {
				return errors.New("invalid membership key")
			}
		}
	}
	if len(tenants) == 0 {
		return errors.New("missing tenants")
	}
	for _, r := range records {
		if !tenants[r.Key.TenantID] {
			return errors.New("missing tenant parent")
		}
		if r.Family == "organization_membership" && (organizations[r.Key.OrganizationID] != r.Key.TenantID || members[r.Key.MemberID] != r.Key.TenantID) {
			return errors.New("missing membership parent")
		}
	}
	return nil
}
