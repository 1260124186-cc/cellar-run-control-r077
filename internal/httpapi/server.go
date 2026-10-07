package httpapi

import (
	"net/http"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
)

func NewHandler(app *service.Service) http.Handler {
	mux := http.NewServeMux()
	registerFormulaRoutes(mux, app)
	registerVesselRoutes(mux, app)
	registerRunRoutes(mux, app)
	registerRuntimeConfigRoutes(mux, app)
	mux.HandleFunc("GET /healthz", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusOK, map[string]any{
			"status":                 "ok",
			"revision":               app.Revision(),
			"runtime_config_version": app.CurrentRuntimeConfig().Version,
		})
	})

	// The pinning middleware is outermost so every layer below reads the same
	// committed version for the whole request lifetime.
	limiter := &inFlightLimiter{}
	handler := http.Handler(withRequestLimit(limiter, mux))
	handler = withRequestTimeout(handler)
	handler = withRequestLogging(handler)
	handler = withRecovery(handler)
	handler = withRuntimeConfigPinning(app, handler)
	return handler
}

func withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				writeJSON(response, http.StatusInternalServerError, errorResponse{
					Error: errorBody{
						Code:   "internal_failure",
						Detail: "request could not be completed",
					},
				})
			}
		}()
		next.ServeHTTP(response, request)
	})
}
