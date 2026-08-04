package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// writeJSON serialises v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Headers are already sent, so this can only be logged.
		slog.Error("encode response", "err", err)
	}
}

type errorBody struct {
	Error string `json:"error"`
}

// writeError returns a machine-readable error body.
// Internal error details are logged, never leaked to the client.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}
