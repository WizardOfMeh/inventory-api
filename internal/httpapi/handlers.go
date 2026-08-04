package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/WizardOfMeh/inventory-api/internal/store"
)

const (
	defaultLimit = 20
	maxLimit     = 100
)

// API holds the dependencies shared by all handlers.
type API struct {
	Store *store.Store
	// Ping reports whether the database is reachable; used by /readyz.
	Ping func(context.Context) error
}

// getNodeInventory handles GET /api/v1/nodes/{id}/inventory.
func (a *API) getNodeInventory(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "id must be a positive integer")
		return
	}

	node, err := a.Store.NodeInventory(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "node not found")
		return
	case err != nil:
		slog.Error("node inventory", "err", err, "node_id", id)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, node)
}

// listVMs handles GET /api/v1/vms.
func (a *API) listVMs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit := defaultLimit
	if raw := q.Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v <= 0 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		// Capped so a single request cannot ask for the whole table.
		if v > maxLimit {
			v = maxLimit
		}
		limit = v
	}

	offset := 0
	if raw := q.Get("offset"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
			return
		}
		offset = v
	}

	sort, ok := store.ParseSortOrder(q.Get("sort"))
	if !ok {
		writeError(w, http.StatusBadRequest, "sort must be one of: id_asc, ram_asc, ram_desc, name_asc")
		return
	}

	vms, err := a.Store.ListVMs(r.Context(), store.ListVMsParams{
		Limit:  limit,
		Offset: offset,
		Sort:   sort,
	})
	if err != nil {
		slog.Error("list vms", "err", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items": vms,
		"limit": limit,
		"count": len(vms),
	})
}

// healthz reports that the process is alive. Kubernetes liveness probe.
func (a *API) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz reports that the process can serve traffic. Kubernetes readiness
// probe: alive but with a dead database means "do not send me requests".
func (a *API) readyz(w http.ResponseWriter, r *http.Request) {
	if err := a.Ping(r.Context()); err != nil {
		slog.Warn("readiness check failed", "err", err)
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
