package httpapi

import (
	"net/http"
	"strings"

	"github.com/ThalesMMS/CodeAtlas/internal/readiness"
)

// diagnosticAPIPaths is the explicit allowlist of API routes that answer
// normally before the application is READY. Everything else under /api/ is a
// functional route and is gated. Non-API paths (the static UI) are always
// allowed so the diagnostic UI can load.
var diagnosticAPIPaths = map[string]struct{}{
	"/api/health":             {},
	"/api/health/live":        {},
	"/api/health/ready":       {},
	"/api/capabilities":       {},
	"/api/stats":              {},
	"/api/settings":           {},
	"/api/settings/overrides": {},
}

// allowedBeforeReady reports whether a request path may be served before READY.
// It is an explicit allowlist: any new /api/ route is gated by default.
func allowedBeforeReady(path string) bool {
	if _, ok := diagnosticAPIPaths[path]; ok {
		return true
	}
	return !strings.HasPrefix(path, "/api/")
}

// readinessGate blocks functional routes until the coordinator reports READY,
// returning a stable 503 APP_NOT_READY envelope. Diagnostic endpoints and static
// assets remain available throughout BOOTING, PROBING_CAPABILITIES, INDEXING,
// FAILED and SHUTTING_DOWN.
func (s *Server) readinessGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if s.readiness == nil || s.readiness.CurrentState() == readiness.StateReady || allowedBeforeReady(request.URL.Path) {
			next.ServeHTTP(response, request)
			return
		}
		writeNotReady(response, s.readiness.CurrentState())
	})
}

// writeNotReady emits the stable 503 envelope for gated routes.
func writeNotReady(response http.ResponseWriter, state readiness.State) {
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusServiceUnavailable, map[string]any{
		"error": map[string]any{
			"code":      "APP_NOT_READY",
			"message":   "CodeAtlas has not finished initializing yet",
			"retryable": true,
			"state":     string(state),
		},
	})
}
