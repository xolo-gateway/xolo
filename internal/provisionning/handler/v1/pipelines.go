package v1

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/xolo-gateway/xolo/internal/core/model"
)

// pipelineNodeRoute is the representation a node PUT accepts: its
// configuration, and the secrets of a plugin node.
var pipelineNodeRoute = businessRoute{required: []string{"data"}, optional: []string{"secrets"}}

// mountPipelineNodes serves the nodes of the virtual models or middlewares
// of collection. A node has no revision of its own: its routes return and
// require the ETag of its pipeline.
func (h *Handler) mountPipelineNodes(family, collection string) {
	h.mux.HandleFunc("GET "+collection, h.handlePipelineNodes(family))
	h.mux.HandleFunc("GET "+collection+"/{nodeID}", h.handlePipelineNode(family))
	h.mux.HandleFunc("PUT "+collection+"/{nodeID}", h.handlePutPipelineNode(family))
}

func (h *Handler) handlePipelineNodes(family string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope, key, ok := businessTarget(w, r, family)
		if !ok || !noQuery(w, r) {
			return
		}
		nodes, etag, err := h.provisioning.ReadPipelineNodes(r.Context(), scope, key)
		if err != nil {
			writeServiceError(r.Context(), w, err, "could not read nodes")
			return
		}
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, map[string]any{"items": nodes})
	}
}

func (h *Handler) handlePipelineNode(family string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope, key, ok := businessTarget(w, r, family)
		if !ok || !noQuery(w, r) {
			return
		}
		nodeID, ok := pathNodeID(w, r)
		if !ok {
			return
		}
		node, etag, err := h.provisioning.ReadPipelineNode(r.Context(), scope, key, nodeID)
		if err != nil {
			writeServiceError(r.Context(), w, err, "could not read node")
			return
		}
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, node)
	}
}

func (h *Handler) handlePutPipelineNode(family string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope, key, ok := businessTarget(w, r, family)
		if !ok || !noQuery(w, r) {
			return
		}
		nodeID, ok := pathNodeID(w, r)
		if !ok {
			return
		}
		condition, ok := commonCondition(w, r)
		if !ok {
			return
		}
		raw, ok := decodeBusiness(w, r, pipelineNodeRoute)
		if !ok {
			return
		}
		var update model.PipelineNodeUpdate
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&update) != nil {
			writeError(w, http.StatusBadRequest, codeInvalidJSON, "invalid representation")
			return
		}
		node, etag, err := h.provisioning.PutPipelineNode(r.Context(), scope, key, nodeID, condition, update)
		if err != nil {
			writeServiceError(r.Context(), w, err, "could not write node")
			return
		}
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, node)
	}
}

// pathNodeID reads the ID of a node, as the graph names it.
func pathNodeID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("nodeID")
	if id == "" || strings.TrimSpace(id) != id || len(id) > 200 {
		writeError(w, http.StatusBadRequest, codeInvalidParameter, "invalid node id")
		return "", false
	}
	return id, true
}
