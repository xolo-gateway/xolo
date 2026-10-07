package v1

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"unicode/utf8"

	"github.com/xolo-gateway/xolo/internal/core/model"
	"github.com/xolo-gateway/xolo/internal/core/service"
)

// ContractVersion identifies the common provisioning contract this API
// implements.
const ContractVersion = "0.1.0-draft.1"

type manifestDTO struct {
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	ContractVersion string   `json:"contract_version"`
	Capabilities    []string `json:"capabilities"`
}

func (h *Handler) handleManifest(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, manifestDTO{Name: "Xolo", Version: h.version, ContractVersion: ContractVersion, Capabilities: h.capabilities})
}

func (h *Handler) handlePutTenant(w http.ResponseWriter, r *http.Request) {
	scope, key, ok := commonTarget(w, r, model.FamilyTenant)
	if !ok {
		return
	}
	fields, ok := decodeCommon(w, r, []string{"slug", "name", "status"})
	if !ok {
		return
	}
	h.writeCommon(w, r, scope, key, "could not write tenant", func(ctx context.Context, tx *service.ProvisioningService) error {
		_, err := tx.PutTenant(ctx, model.TenantID(key), commonResource(fields))
		return err
	})
}

func (h *Handler) handlePutDomain(w http.ResponseWriter, r *http.Request) {
	scope, key, ok := commonTarget(w, r, model.FamilyTenantDomain)
	if !ok {
		return
	}
	fields, ok := decodeCommon(w, r, []string{"status"})
	if !ok {
		return
	}
	h.writeCommon(w, r, scope, key, "could not write domain", func(ctx context.Context, tx *service.ProvisioningService) error {
		_, err := tx.PutDomain(ctx, model.TenantID(scope.TenantID), key, model.Status(fields["status"]))
		return err
	})
}

func (h *Handler) handlePutOrganization(w http.ResponseWriter, r *http.Request) {
	scope, key, ok := commonTarget(w, r, model.FamilyOrganization)
	if !ok {
		return
	}
	fields, ok := decodeCommon(w, r, []string{"slug", "name", "status"})
	if !ok {
		return
	}
	h.writeCommon(w, r, scope, key, "could not write organization", func(ctx context.Context, tx *service.ProvisioningService) error {
		_, err := tx.PutOrganization(ctx, model.TenantID(scope.TenantID), model.OrgID(key), commonResource(fields))
		return err
	})
}

func (h *Handler) handlePutTenantMember(w http.ResponseWriter, r *http.Request) {
	scope, key, ok := commonTarget(w, r, model.FamilyMember)
	if !ok {
		return
	}
	fields, ok := decodeCommon(w, r, []string{"email", "tenant_role", "status"}, "display_name")
	if !ok {
		return
	}
	h.writeCommon(w, r, scope, key, "could not write member", func(ctx context.Context, tx *service.ProvisioningService) error {
		_, err := tx.PutTenantMember(ctx, model.TenantID(scope.TenantID), model.UserID(key), service.CommonMember{
			Email:       fields["email"],
			DisplayName: fields["display_name"],
			TenantRole:  model.TenantRole(fields["tenant_role"]),
			Status:      model.Status(fields["status"]),
		})
		return err
	})
}

func (h *Handler) handlePutOrgMember(w http.ResponseWriter, r *http.Request) {
	scope, key, ok := commonTarget(w, r, model.FamilyOrganizationMembership)
	if !ok {
		return
	}
	fields, ok := decodeCommon(w, r, []string{"role", "status"})
	if !ok {
		return
	}
	h.writeCommon(w, r, scope, key, "could not write membership", func(ctx context.Context, tx *service.ProvisioningService) error {
		_, err := tx.PutOrgMember(ctx, model.TenantID(scope.TenantID), model.OrgID(scope.OrganizationID), model.UserID(key), service.CommonMembership{
			Role:   model.MembershipRole(fields["role"]),
			Status: model.Status(fields["status"]),
		})
		return err
	})
}

// writeCommon applies a PUT under its If-Match condition and answers with the
// projection written by the same transaction, and its ETag.
func (h *Handler) writeCommon(w http.ResponseWriter, r *http.Request, scope model.CommonScope, key, fallback string, put func(context.Context, *service.ProvisioningService) error) {
	condition, ok := commonCondition(w, r)
	if !ok {
		return
	}
	item, err := h.provisioning.PutCommon(r.Context(), scope, key, condition, put)
	if err != nil {
		writeServiceError(r.Context(), w, err, fallback)
		return
	}
	w.Header().Set("ETag", item.ETag)
	writeJSON(w, http.StatusOK, item.Representation)
}

// commonCondition reads If-Match. If-None-Match is refused rather than
// ignored: a client relying on it must not believe it was honoured.
func commonCondition(w http.ResponseWriter, r *http.Request) (model.MatchCondition, bool) {
	if len(r.Header.Values("If-None-Match")) > 0 {
		writeError(w, http.StatusBadRequest, codeInvalidPrecondition, "If-None-Match is not supported")
		return model.MatchCondition{}, false
	}
	condition, err := model.ParseMatchCondition(r.Header.Values("If-Match"))
	if err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidPrecondition, "malformed If-Match")
		return model.MatchCondition{}, false
	}
	return condition, true
}

func commonResource(fields map[string]string) service.CommonResource {
	return service.CommonResource{Slug: fields["slug"], Name: fields["name"], Status: model.Status(fields["status"])}
}

// pathTenantID reads the tenant ID of the path: a canonical lowercase UUID.
func pathTenantID(w http.ResponseWriter, r *http.Request) (model.TenantID, bool) {
	id, err := model.ParseTenantID(r.PathValue("tenantID"))
	if err != nil {
		writeInvalidID(w)
		return "", false
	}
	return id, true
}

func writeInvalidID(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, codeInvalidParameter, "identifiers must be canonical lowercase UUIDs")
}

// decodeCommon reads a common representation: one JSON object of string
// fields, every required field present and none null, no unknown field. The
// whole bounded body is read before decoding, so trailing data counts toward
// the size limit; raw values keep the null, missing and type distinctions.
func decodeCommon(w http.ResponseWriter, r *http.Request, required []string, optional ...string) (map[string]string, bool) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, codeUnsupportedMediaType, "application/json is required")
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBodySize+1))
	if err != nil || len(raw) > maxRequestBodySize || !utf8.Valid(raw) {
		writeError(w, http.StatusBadRequest, codeInvalidJSON, "invalid JSON body")
		return nil, false
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		writeError(w, http.StatusBadRequest, codeInvalidJSON, "expected one JSON object")
		return nil, false
	}
	if object == nil {
		writeError(w, http.StatusBadRequest, codeInvalidRepresentation, "null is not a representation")
		return nil, false
	}
	allowed := map[string]bool{}
	for _, field := range append(append([]string{}, required...), optional...) {
		allowed[field] = true
	}
	fields := map[string]string{}
	for field, value := range object {
		if !allowed[field] {
			writeError(w, http.StatusBadRequest, codeInvalidJSON, "unknown field "+field)
			return nil, false
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			writeError(w, http.StatusBadRequest, codeInvalidRepresentation, "field "+field+" can not be null")
			return nil, false
		}
		// encoding/json silently replaces unpaired UTF-16 surrogates: reject
		// them before that lossy conversion, while accepting a genuine U+FFFD.
		if !validJSONString(value) {
			writeError(w, http.StatusBadRequest, codeInvalidJSON, "field "+field+" is not valid Unicode")
			return nil, false
		}
		var text string
		if json.Unmarshal(value, &text) != nil {
			writeError(w, http.StatusBadRequest, codeInvalidJSON, "field "+field+" must be a string")
			return nil, false
		}
		fields[field] = text
	}
	for _, field := range required {
		if _, ok := fields[field]; !ok {
			writeError(w, http.StatusBadRequest, codeInvalidRepresentation, "field "+field+" is required")
			return nil, false
		}
	}
	return fields, true
}

// validJSONString reports whether every \u escape of a JSON string literal,
// already known to be valid JSON, forms a complete UTF-16 code point.
func validJSONString(v []byte) bool {
	v = bytes.TrimSpace(v)
	for i := 1; i < len(v)-1; i++ {
		if v[i] != '\\' {
			continue
		}
		i++
		if v[i] != 'u' {
			continue
		}
		unit := hexUnit(v[i+1 : i+5])
		i += 4
		if unit >= 0xdc00 && unit <= 0xdfff {
			return false
		}
		if unit >= 0xd800 && unit <= 0xdbff {
			if i+6 >= len(v) || v[i+1] != '\\' || v[i+2] != 'u' {
				return false
			}
			low := hexUnit(v[i+3 : i+7])
			if low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

func hexUnit(b []byte) int {
	n := 0
	for _, c := range b {
		n *= 16
		switch {
		case c >= '0' && c <= '9':
			n += int(c - '0')
		case c >= 'a' && c <= 'f':
			n += int(c-'a') + 10
		default:
			n += int(c-'A') + 10
		}
	}
	return n
}
