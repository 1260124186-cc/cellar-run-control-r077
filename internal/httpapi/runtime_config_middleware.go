package httpapi

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
)

const runtimeConfigVersionHeader = "X-Runtime-Config-Version"

type runtimeConfigContextKey struct{}

// withRuntimeConfigPinning pins exactly one committed config version to each
// request. Every runtime decision for the request (timeout, log level,
// in-flight limit) reads this pinned copy, so a publish that lands
// mid-request never changes what the in-flight request observes.
func withRuntimeConfigPinning(app *service.Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		pinned := app.CurrentRuntimeConfig()
		response.Header().Set(runtimeConfigVersionHeader, strconv.FormatInt(pinned.Version, 10))
		ctx := context.WithValue(request.Context(), runtimeConfigContextKey{}, pinned)
		next.ServeHTTP(response, request.WithContext(ctx))
	})
}

func pinnedRuntimeConfig(request *http.Request) domain.RuntimeConfigVersion {
	if pinned, ok := request.Context().Value(runtimeConfigContextKey{}).(domain.RuntimeConfigVersion); ok {
		return pinned
	}
	return domain.RuntimeConfigVersion{Config: domain.DefaultRuntimeConfig()}
}

// inFlightLimiter is a counting semaphore whose capacity is supplied per
// acquire from the request's pinned config version.
type inFlightLimiter struct {
	mu       sync.Mutex
	inFlight int
}

func (l *inFlightLimiter) acquire(limit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight >= limit {
		return false
	}
	l.inFlight++
	return true
}

func (l *inFlightLimiter) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inFlight--
}

func withRequestLimit(limiter *inFlightLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		pinned := pinnedRuntimeConfig(request)
		if !limiter.acquire(pinned.Config.MaxInFlightRequests) {
			writeJSON(response, http.StatusTooManyRequests, errorResponse{
				Error: errorBody{
					Code:   domain.CodeRequestLimitExceeded,
					Detail: "too many requests in flight for the current runtime config",
				},
			})
			return
		}
		defer limiter.release()
		next.ServeHTTP(response, request)
	})
}

func withRequestLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: response, status: http.StatusOK}
		next.ServeHTTP(recorder, request)
		pinned := pinnedRuntimeConfig(request)
		if shouldLogRequest(pinned.Config.LogLevel, recorder.status) {
			log.Printf("request completed method=%s path=%s status=%d duration_ms=%d config_version=%d",
				request.Method, request.URL.Path, recorder.status,
				time.Since(started).Milliseconds(), pinned.Version)
		}
	})
}

func shouldLogRequest(level string, status int) bool {
	switch level {
	case domain.LogLevelDebug, domain.LogLevelInfo:
		return true
	case domain.LogLevelWarn:
		return status >= http.StatusBadRequest
	case domain.LogLevelError:
		return status >= http.StatusInternalServerError
	default:
		return true
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.wrote {
		return
	}
	r.wrote = true
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(payload []byte) (int, error) {
	if !r.wrote {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(payload)
}

// withRequestTimeout enforces the pinned version's request timeout. The
// handler runs with a deadline-bound context; if it does not finish in time
// the request fails with a stable timeout error and late handler writes are
// discarded.
func withRequestTimeout(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		pinned := pinnedRuntimeConfig(request)
		timeout := time.Duration(pinned.Config.RequestTimeoutMs) * time.Millisecond
		ctx, cancel := context.WithTimeout(request.Context(), timeout)
		defer cancel()

		buffered := &bufferedResponseWriter{header: make(http.Header)}
		finished := make(chan panicOutcome, 1)
		go func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					finished <- panicOutcome{recovered: recovered}
					return
				}
			}()
			next.ServeHTTP(buffered, request.WithContext(ctx))
			finished <- panicOutcome{}
		}()

		select {
		case outcome := <-finished:
			if outcome.recovered != nil {
				panic(outcome.recovered)
			}
			copyHeaders(response.Header(), buffered.header)
			response.WriteHeader(buffered.statusCode())
			_, _ = io.Copy(response, &buffered.body)
		case <-ctx.Done():
			writeJSON(response, http.StatusServiceUnavailable, errorResponse{
				Error: errorBody{
					Code:   domain.CodeRequestTimeout,
					Detail: "request exceeded the configured timeout",
				},
			})
		}
	})
}

type panicOutcome struct {
	recovered any
}

type bufferedResponseWriter struct {
	header http.Header
	body   bytes.Buffer
	status int
	wrote  bool
}

func (w *bufferedResponseWriter) Header() http.Header {
	return w.header
}

func (w *bufferedResponseWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.wrote = true
	w.status = status
}

func (w *bufferedResponseWriter) Write(payload []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(payload)
}

func (w *bufferedResponseWriter) statusCode() int {
	if !w.wrote {
		return http.StatusOK
	}
	return w.status
}

func copyHeaders(destination, source http.Header) {
	for key, values := range source {
		copied := make([]string, len(values))
		copy(copied, values)
		destination[key] = copied
	}
}
