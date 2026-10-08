package adoption

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
)

// fakeInventory serves fixed projections.
type fakeInventory struct {
	items []Record
	fail  error
}

func (f fakeInventory) ReadInventory(ctx context.Context, start func(source, cursor string) error, item func(string, model.CommonItem) error) error {
	if err := start("urn:uuid:feed", "c0-token"); err != nil {
		return err
	}
	for _, r := range f.items {
		if err := item(r.Family, model.CommonItem{Key: r.Key, Representation: r.Representation, ETag: r.ETag}); err != nil {
			return err
		}
	}
	return f.fail
}

const (
	tenantID = "11111111-1111-4111-8111-111111111111"
	orgID    = "22222222-2222-4222-8222-222222222222"
	memberID = "33333333-3333-4333-8333-333333333333"
)

func validInventory() fakeInventory {
	return fakeInventory{items: []Record{
		{Family: model.FamilyTenant, Key: model.CommonKey{TenantID: tenantID}, Representation: json.RawMessage(`{"name":"Default","slug":"default","status":"active"}`), ETag: `W/"1"`},
		{Family: model.FamilyTenantDomain, Key: model.CommonKey{TenantID: tenantID, Hostname: "gw.example.test"}, Representation: json.RawMessage(`{"status":"active"}`), ETag: `W/"2"`},
		{Family: model.FamilyOrganization, Key: model.CommonKey{TenantID: tenantID, OrganizationID: orgID}, Representation: json.RawMessage(`{"name":"Org","slug":"org","status":"suspended"}`), ETag: `W/"3"`},
		{Family: model.FamilyMember, Key: model.CommonKey{TenantID: tenantID, MemberID: memberID}, Representation: json.RawMessage(`{"email":"a@example.test","status":"active","tenant_role":"owner"}`), ETag: `W/"4"`},
		{Family: model.FamilyOrganizationMembership, Key: model.CommonKey{TenantID: tenantID, OrganizationID: orgID, MemberID: memberID}, Representation: json.RawMessage(`{"role":"owner","status":"active"}`), ETag: `W/"5"`},
	}}
}

func export(t *testing.T, inventory fakeInventory) []byte {
	t.Helper()
	var out bytes.Buffer
	require.NoError(t, Export(t.Context(), inventory, &out))
	return out.Bytes()
}

func TestExportRoundTrip(t *testing.T) {
	raw := export(t, validInventory())
	summary, err := Verify(bytes.NewReader(raw))
	require.NoError(t, err)
	require.Equal(t, "urn:uuid:feed", summary.Source)
	require.Equal(t, "c0-token", summary.Cursor)
	require.Equal(t, 5, summary.Count)
	require.Equal(t, 1, summary.Families[model.FamilyOrganizationMembership])
	require.Equal(t, 7, bytes.Count(raw, []byte("\n")), "header, five records, trailer")
}

func TestExportFailureHasNoTrailer(t *testing.T) {
	inventory := validInventory()
	inventory.fail = errors.New("connection lost")
	var out bytes.Buffer
	require.Error(t, Export(t.Context(), inventory, &out))
	_, err := Verify(bytes.NewReader(out.Bytes()))
	require.ErrorIs(t, err, ErrInvalidExport)
}

func TestVerifyRejects(t *testing.T) {
	valid := string(export(t, validInventory()))
	lines := strings.SplitAfter(valid, "\n")
	lines = lines[:len(lines)-1] // the empty string after the last newline
	without := func(i int) string {
		return strings.Join(append(append([]string{}, lines[:i]...), lines[i+1:]...), "")
	}
	// reexport rebuilds a consistent checksum around altered records, so
	// only the record checks can refuse them.
	reexport := func(change func([]Record) []Record) string {
		inventory := validInventory()
		inventory.items = change(inventory.items)
		return string(export(t, inventory))
	}
	cases := map[string]string{
		"empty":             "",
		"altered byte":      strings.Replace(valid, `"Org"`, `"Orf"`, 1),
		"truncated":         valid[:len(valid)-10],
		"no final newline":  strings.TrimSuffix(valid, "\n"),
		"missing trailer":   without(len(lines) - 1),
		"missing record":    without(2),
		"data after":        valid + lines[1],
		"unknown field":     strings.Replace(valid, `"etag":`, `"extra":1,"etag":`, 1),
		"incompatible":      strings.Replace(valid, Version, "xolo-adoption/2", 1),
		"wrong count":       strings.Replace(valid, `"count":5`, `"count":6`, 1),
		"orphan membership": reexport(func(r []Record) []Record { return append(r[:3], r[4]) }),
		"duplicate":         reexport(func(r []Record) []Record { return append(r, r[4]) }),
		"out of order":      reexport(func(r []Record) []Record { return append(r, r[0]) }),
		"unknown family": reexport(func(r []Record) []Record {
			r[1].Family = "subscription"
			return r
		}),
		"invalid status": reexport(func(r []Record) []Record {
			r[0].Representation = json.RawMessage(`{"status":"deleted"}`)
			return r
		}),
		"not an object": reexport(func(r []Record) []Record {
			r[0].Representation = json.RawMessage(`42`)
			return r
		}),
		"missing etag": reexport(func(r []Record) []Record {
			r[2].ETag = ""
			return r
		}),
		"invalid hostname": reexport(func(r []Record) []Record {
			r[1].Key.Hostname = "GW.example.test"
			return r
		}),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Verify(strings.NewReader(raw))
			require.ErrorIs(t, err, ErrInvalidExport)
		})
	}
}

// exportWith writes an export declaring families, as an older version did.
func exportWith(t *testing.T, families []string, records []Record) []byte {
	t.Helper()
	var out bytes.Buffer
	digest := sha256.New()
	require.NoError(t, writeLine(&out, digest, Header{Version: Version, Contract: model.CommonContractVersion, Source: "urn:uuid:feed", Cursor: "c0", Families: families}))
	for _, record := range records {
		require.NoError(t, writeLine(&out, digest, record))
	}
	require.NoError(t, writeLine(&out, nil, Trailer{Count: len(records), Complete: true, SHA256: hex.EncodeToString(digest.Sum(nil))}))
	return out.Bytes()
}

func TestVerifyBusinessFamilies(t *testing.T) {
	inventory := validInventory()
	provider := Record{Family: model.FamilyProvider, Key: model.CommonKey{TenantID: tenantID, OrganizationID: orgID, ResourceID: "44444444-4444-4444-8444-444444444444"}, Representation: json.RawMessage(`{"name":"OpenAI","active":true}`), ETag: `W/"6"`}
	quota := Record{Family: model.FamilyQuota, Key: model.CommonKey{TenantID: tenantID, ResourceID: string(model.NewQuotaID())}, Representation: json.RawMessage(`{"scope":"org"}`), ETag: `W/"7"`}
	inventory.items = append(inventory.items, provider, quota)
	summary, err := Verify(bytes.NewReader(export(t, inventory)))
	require.NoError(t, err)
	require.Equal(t, 1, summary.Families[model.FamilyQuota])

	// An export made before the business families stays valid.
	legacy := validInventory().items
	_, err = Verify(bytes.NewReader(exportWith(t, model.CommonFamilies, legacy)))
	require.NoError(t, err)
	_, err = Verify(bytes.NewReader(exportWith(t, model.CommonFamilies, append(legacy, provider))))
	require.ErrorIs(t, err, ErrInvalidExport, "a family the header does not declare")
	_, err = Verify(bytes.NewReader(exportWith(t, model.CommonFamilies[1:], legacy[1:])))
	require.ErrorIs(t, err, ErrInvalidExport, "the common families are required")

	for name, record := range map[string]Record{
		"foreign organization":        {Family: model.FamilyProvider, Key: model.CommonKey{TenantID: tenantID, OrganizationID: "55555555-5555-4555-8555-555555555555", ResourceID: "44444444-4444-4444-8444-444444444444"}, Representation: json.RawMessage(`{}`), ETag: `W/"8"`},
		"quota under an organization": {Family: model.FamilyQuota, Key: model.CommonKey{TenantID: tenantID, OrganizationID: orgID, ResourceID: string(model.NewQuotaID())}, Representation: json.RawMessage(`{}`), ETag: `W/"8"`},
		"missing resource key":        {Family: model.FamilyAlert, Key: model.CommonKey{TenantID: tenantID, OrganizationID: orgID}, Representation: json.RawMessage(`{}`), ETag: `W/"8"`},
	} {
		inventory := validInventory()
		inventory.items = append(inventory.items, record)
		_, err := Verify(bytes.NewReader(export(t, inventory)))
		require.ErrorIs(t, err, ErrInvalidExport, name)
	}
}
