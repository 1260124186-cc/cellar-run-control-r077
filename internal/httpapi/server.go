package httpapi

import (
	"net/http"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/logging"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
)

// NewHandler builds the application mux. A nil logger discards request log
// output, which keeps the bounded workflow checks quiet.
func NewHandler(app *service.Service, logger *logging.Logger) http.Handler {
	mux := http.NewServeMux()
	registerFormulaRoutes(mux, app)
	registerVesselRoutes(mux, app)
	registerRunRoutes(mux, app)
	registerRuntimeConfigRoutes(mux, app)
	mux.HandleFunc("GET /healthz", func(response http.ResponseWriter, request *http.Request) {
		view := configViewFrom(request)
		writeJSON(response, http.StatusOK, map[string]any{
			"status":                  "ok",
			"revision":                app.Revision(),
			"runtime_config_revision": view.Revision,
		})
	})
	if logger == nil {
		logger = logging.Discard()
	}
	return withRecovery(withRuntimeConfig(app, logger, mux))
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
