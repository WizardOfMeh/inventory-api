package httpapi

import "net/http"

// Routes builds the HTTP handler.
//
// Probes are mounted outside the auth middleware because kubelet does
// not send an Authorization header, so protected probes would return 401 and
// the pod would be restarted forever.
func Routes(api *API, token string) http.Handler {
	protected := http.NewServeMux()
	protected.HandleFunc("GET /api/v1/nodes/{id}/inventory", api.getNodeInventory)
	protected.HandleFunc("GET /api/v1/vms", api.listVMs)

	root := http.NewServeMux()
	root.HandleFunc("GET /healthz", api.healthz)
	root.HandleFunc("GET /readyz", api.readyz)
	root.Handle("/api/", BearerAuth(token)(protected))

	return Chain(root, RequestID, Logger, Recover)
}
