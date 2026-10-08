package v1

import (
	"log/slog"
	"net/http"

	"github.com/bornholm/go-x/slogx"
)

type ownershipDTO struct {
	Families map[string]string `json:"families"`
}

// handleOwnership returns the write authority of each family, as every
// replica enforces it.
func (h *Handler) handleOwnership(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	families := map[string]string{}
	for family, owner := range h.provisioning.OwnershipPolicy() {
		families[family] = string(owner)
	}
	writeJSON(w, http.StatusOK, ownershipDTO{Families: families})
}

// handleAdoptionExport streams the adoption export. Once the first line is
// sent, a failure can only cut the stream: the export then lacks its trailer
// and verification rejects it.
func (h *Handler) handleAdoptionExport(w http.ResponseWriter, r *http.Request) {
	if !noQuery(w, r) {
		return
	}
	stream := &lazyHeaderWriter{w: w}
	if err := h.provisioning.ExportInventory(r.Context(), stream); err != nil {
		if !stream.started {
			writeServiceError(r.Context(), w, err, "could not export inventory")
			return
		}
		slog.ErrorContext(r.Context(), "adoption export interrupted", slogx.Error(err))
	}
}

// lazyHeaderWriter sends the success headers with the first bytes, so an
// error before them still gets an ordinary error response.
type lazyHeaderWriter struct {
	w       http.ResponseWriter
	started bool
}

func (l *lazyHeaderWriter) Write(p []byte) (int, error) {
	if !l.started {
		l.started = true
		header := l.w.Header()
		header.Set("Content-Type", "application/x-ndjson")
		header.Set("Cache-Control", "no-store")
		header.Set("Content-Disposition", `attachment; filename="xolo-adoption.ndjson"`)
		l.w.WriteHeader(http.StatusOK)
	}
	return l.w.Write(p)
}
