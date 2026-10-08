package adoption

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"github.com/pkg/errors"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// maxLineSize bounds the memory a single line may take: a projection is a
// few hundred bytes.
const maxLineSize = 1 << 20

// Summary describes a verified export.
type Summary struct {
	Source   string         `json:"source"`
	Cursor   string         `json:"c0"`
	Count    int            `json:"count"`
	Families map[string]int `json:"families"`
}

// ErrInvalidExport refuses an incomplete, altered or incompatible export.
var ErrInvalidExport = errors.New("invalid adoption export")

func invalid(line int, format string, args ...any) error {
	return errors.Wrapf(ErrInvalidExport, "line %d: %s", line, fmt.Sprintf(format, args...))
}

// Verify reads a whole export and checks its integrity, its completeness and
// the consistency of its records, keeping only their keys in memory. A
// consumer stages the inventory, verifies it, then replays the events after
// its cursor before relying on it.
func Verify(r io.Reader) (Summary, error) {
	summary := Summary{Families: map[string]int{}}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxLineSize)
	scanner.Split(scanLinesKeepingNewline)

	digest := sha256.New()
	state := newInventoryState()
	var trailer *Trailer
	line := 0
	for scanner.Scan() {
		line++
		raw := scanner.Bytes()
		if raw[len(raw)-1] != '\n' {
			return summary, invalid(line, "truncated line")
		}
		if trailer != nil {
			return summary, invalid(line, "data after the trailer")
		}
		body := raw[:len(raw)-1]
		if line == 1 {
			var header Header
			if err := decodeStrict(body, &header); err != nil {
				return summary, invalid(line, "header: %v", err)
			}
			if header.Version != Version || header.Contract != model.CommonContractVersion || header.Source == "" || header.Cursor == "" || !state.allow(header.Families) {
				return summary, invalid(line, "incompatible header")
			}
			summary.Source, summary.Cursor = header.Source, header.Cursor
			digest.Write(raw)
			continue
		}
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(body, &probe); err != nil {
			return summary, invalid(line, "%v", err)
		}
		if _, ok := probe["sha256"]; ok {
			trailer = &Trailer{}
			if err := decodeStrict(body, trailer); err != nil {
				return summary, invalid(line, "trailer: %v", err)
			}
			continue
		}
		var record Record
		if err := decodeStrict(body, &record); err != nil {
			return summary, invalid(line, "record: %v", err)
		}
		if err := state.add(record); err != nil {
			return summary, invalid(line, "%v", err)
		}
		summary.Count++
		summary.Families[record.Family]++
		digest.Write(raw)
	}
	if err := scanner.Err(); err != nil {
		return summary, errors.Wrap(ErrInvalidExport, err.Error())
	}
	if line == 0 {
		return summary, errors.Wrap(ErrInvalidExport, "empty export")
	}
	if trailer == nil || !trailer.Complete {
		return summary, errors.Wrap(ErrInvalidExport, "incomplete export: missing trailer")
	}
	if trailer.Count != summary.Count {
		return summary, errors.Wrapf(ErrInvalidExport, "trailer counts %d records, export holds %d", trailer.Count, summary.Count)
	}
	want := hex.EncodeToString(digest.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(trailer.SHA256), []byte(want)) != 1 {
		return summary, errors.Wrap(ErrInvalidExport, "checksum mismatch")
	}
	if summary.Families[model.FamilyTenant] == 0 {
		return summary, errors.Wrap(ErrInvalidExport, "no tenant")
	}
	return summary, nil
}

// scanLinesKeepingNewline splits on '\n' and keeps it, so the digest covers
// the exact bytes; a last line without newline is returned as is and refused.
func scanLinesKeepingNewline(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i+1], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func decodeStrict(raw []byte, v any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("trailing data")
	}
	return nil
}

// inventoryState checks records as they stream: their order, their keys and
// their parents, which always come first.
type inventoryState struct {
	families      map[string]bool
	family        int
	seen          map[string]bool
	tenants       map[string]bool
	organizations map[string]string
	members       map[string]string
}

func newInventoryState() *inventoryState {
	return &inventoryState{seen: map[string]bool{}, tenants: map[string]bool{}, organizations: map[string]string{}, members: map[string]string{}}
}

// allow accepts the families an export declares: an ordered selection of
// Families holding at least the common contract. An export made before the
// business families is still valid.
func (s *inventoryState) allow(families []string) bool {
	last := -1
	s.families = map[string]bool{}
	for _, family := range families {
		index := slices.Index(Families, family)
		if index <= last {
			return false
		}
		last = index
		s.families[family] = true
	}
	for _, family := range model.CommonFamilies {
		if !s.families[family] {
			return false
		}
	}
	return true
}

func (s *inventoryState) add(r Record) error {
	family := slices.Index(Families, r.Family)
	if family < 0 || !s.families[r.Family] {
		return fmt.Errorf("unknown family %q", r.Family)
	}
	if family < s.family {
		return fmt.Errorf("family %q out of order", r.Family)
	}
	s.family = family
	if r.ETag == "" {
		return errors.New("missing etag")
	}
	var rep struct {
		Status *model.Status `json:"status"`
	}
	business := model.IsBusinessFamily(r.Family)
	if !bytes.HasPrefix(bytes.TrimSpace(r.Representation), []byte("{")) || json.Unmarshal(r.Representation, &rep) != nil ||
		(!business && (rep.Status == nil || !rep.Status.Valid())) {
		return errors.New("invalid representation")
	}
	key, err := json.Marshal(r.Key)
	if err != nil {
		return err
	}
	id := r.Family + string(key)
	if s.seen[id] {
		return errors.New("duplicate record")
	}
	s.seen[id] = true

	k := r.Key
	if _, err := model.ParseTenantID(k.TenantID); err != nil {
		return errors.New("invalid tenant key")
	}
	if r.Family != model.FamilyTenant && !s.tenants[k.TenantID] {
		return errors.New("missing tenant parent")
	}
	if business {
		return s.addBusiness(r)
	}
	if k.ResourceID != "" {
		return errors.New("unexpected resource key")
	}
	switch r.Family {
	case model.FamilyTenant:
		if k.OrganizationID != "" || k.MemberID != "" || k.Hostname != "" {
			return errors.New("invalid tenant key")
		}
		s.tenants[k.TenantID] = true
	case model.FamilyTenantDomain:
		host, err := model.NormalizeHostname(k.Hostname)
		if err != nil || host != k.Hostname || k.OrganizationID != "" || k.MemberID != "" {
			return errors.New("invalid domain key")
		}
	case model.FamilyOrganization:
		if _, err := model.ParseOrgID(k.OrganizationID); err != nil || k.MemberID != "" || k.Hostname != "" {
			return errors.New("invalid organization key")
		}
		s.organizations[k.OrganizationID] = k.TenantID
	case model.FamilyMember:
		if _, err := model.ParseUserID(k.MemberID); err != nil || k.OrganizationID != "" || k.Hostname != "" {
			return errors.New("invalid member key")
		}
		s.members[k.MemberID] = k.TenantID
	case model.FamilyOrganizationMembership:
		if k.Hostname != "" {
			return errors.New("invalid membership key")
		}
		if s.organizations[k.OrganizationID] != k.TenantID || s.members[k.MemberID] != k.TenantID {
			return errors.New("missing membership parent")
		}
	}
	return nil
}

// addBusiness checks the key of a business record: its own key, and the
// organization it hangs from, except a quota, which hangs from its tenant.
func (s *inventoryState) addBusiness(r Record) error {
	k := r.Key
	if _, _, err := model.ParseBusinessKey(k.ResourceID); err != nil || k.MemberID != "" || k.Hostname != "" {
		return errors.New("invalid resource key")
	}
	if r.Family == model.FamilyQuota {
		if k.OrganizationID != "" {
			return errors.New("invalid quota key")
		}
		return nil
	}
	if s.organizations[k.OrganizationID] != k.TenantID {
		return errors.New("missing organization parent")
	}
	return nil
}
