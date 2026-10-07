package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/logging"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/runtimeconfig"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
)

type configViewContextKey struct{}

const runtimeRevisionHeader = "X-Runtime-Config-Revision"

// fallbackView protects callers that exercise the mux without the runtime
// configuration middleware (for example, narrowly scoped diagnostics).
func fallbackConfigView() runtimeconfig.View {
	return runtimeconfig.View{
		Revision:            1,
		RequestTimeout:      20 * time.Second,
		MaxRequestBodyBytes: 1 << 20,
		LogLevel:            runtimeconfig.LevelInfo,
	}
}

func configViewFrom(request *http.Request) runtimeconfig.View {
	if view, ok := request.Context().Value(configViewContextKey{}).(runtimeconfig.View); ok {
		return view
	}
	return fallbackConfigView()
}

// statusRecorder captures the response status for request log lines.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(payload []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(payload)
}

// withRuntimeConfig pins one configuration revision for the entire request.
// The timeout, request body limit, and log level are all taken from a single
// CurrentView snapshot captured at entry; a publish committed while the
// request is in flight is invisible to it.
func withRuntimeConfig(app *service.Service, logger *logging.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		view := app.CurrentRuntimeConfig()
		ctx, cancel := context.WithTimeout(request.Context(), view.RequestTimeout)
		defer cancel()

		bound := logger.At(logging.Level(view.LogLevel))
		ctx = context.WithValue(ctx, configViewContextKey{}, view)
		ctx = logging.ContextWith(ctx, bound)
		request = request.WithContext(ctx)

		response.Header().Set(runtimeRevisionHeader, formatRevision(view.Revision))
		recorder := &statusRecorder{ResponseWriter: response}

		started := time.Now()
		next.ServeHTTP(recorder, request)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		bound.Debugf("request completed method=%s path=%s status=%d duration=%s config_revision=%d",
			request.Method, request.URL.Path, status, time.Since(started), view.Revision)
	})
}

func formatRevision(revision int64) string {
	return strconv.FormatInt(revision, 10)
}
