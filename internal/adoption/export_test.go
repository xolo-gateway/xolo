package adoption

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/stretchr/testify/require"
	"github.com/xolo-gateway/xolo/internal/core/model"
	"testing"
)

func TestExportEnvelopeRejectsIncompleteInventory(t *testing.T) {
	tid := string(model.NewTenantID())
	p := Payload{Source: "urn:uuid:example", Cursor: "cursor", Records: []Record{{Family: "tenant", CommonItem: model.CommonItem{Key: model.CommonKey{TenantID: tid}, Representation: json.RawMessage(`{"slug":"default","name":"Default","status":"active"}`), ETag: `W/"u-1"`}}}}
	raw, err := Encode(p)
	require.NoError(t, err)
	_, err = Decode(raw)
	require.NoError(t, err)
	var envelope Envelope
	require.NoError(t, json.Unmarshal(raw, &envelope))
	var complete Payload
	require.NoError(t, json.Unmarshal(envelope.Payload, &complete))
	for _, change := range []func(*Payload){
		func(p *Payload) { p.Complete = false }, func(p *Payload) { p.Count++ }, func(p *Payload) { p.Families = p.Families[:4] }, func(p *Payload) { p.Records[0].Representation = json.RawMessage(`42`) },
	} {
		copy := complete
		copy.Records = append([]Record{}, complete.Records...)
		change(&copy)
		payload, err := json.Marshal(copy)
		require.NoError(t, err)
		sum := sha256.Sum256(payload)
		malformed, err := json.Marshal(Envelope{Payload: payload, SHA256: hex.EncodeToString(sum[:])})
		require.NoError(t, err)
		_, err = Decode(malformed)
		require.Error(t, err)
	}
	p.Records = append(p.Records, Record{Family: "organization_membership", CommonItem: model.CommonItem{Key: model.CommonKey{TenantID: tid, OrganizationID: string(model.NewOrgID()), MemberID: string(model.NewUserID())}, Representation: json.RawMessage(`{"role":"member","status":"active"}`), ETag: `W/"u-2"`}})
	raw, err = Encode(p)
	require.NoError(t, err)
	_, err = Decode(raw)
	require.Error(t, err)
}
