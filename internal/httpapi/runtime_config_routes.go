package httpapi

import (
	"net/http"
	"strconv"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
)

type runtimeConfigPayload struct {
	RequestTimeoutMs    int    `json:"request_timeout_ms"`
	LogLevel            string `json:"log_level"`
	MaxInFlightRequests int    `json:"max_in_flight_requests"`
}

type publishRuntimeConfigRequest struct {
	Config      runtimeConfigPayload `json:"config"`
	PublishedBy string               `json:"published_by"`
}

type rollbackRuntimeConfigRequest struct {
	PublishedBy string `json:"published_by"`
}

func registerRuntimeConfigRoutes(mux *http.ServeMux, app *service.Service) {
	mux.HandleFunc("GET /v1/runtime-config", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusOK, app.RuntimeConfigStatus())
	})

	mux.HandleFunc("GET /v1/runtime-config/versions", func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusOK, map[string]any{"versions": app.ListRuntimeConfigVersions()})
	})

	mux.HandleFunc("POST /v1/runtime-config/publish", func(response http.ResponseWriter, request *http.Request) {
		body, err := decodeJSON[publishRuntimeConfigRequest](response, request)
		if !handleDecode(response, err) {
			return
		}
		expected, err := expectedConfigVersion(request)
		if !handleDecode(response, err) {
			return
		}
		published, err := app.PublishRuntimeConfig(service.PublishRuntimeConfigInput{
			ExpectedVersion: expected,
			Config: domain.RuntimeConfig{
				RequestTimeoutMs:    body.Config.RequestTimeoutMs,
				LogLevel:            body.Config.LogLevel,
				MaxInFlightRequests: body.Config.MaxInFlightRequests,
			},
			PublishedBy: body.PublishedBy,
		})
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusCreated, published)
	})

	mux.HandleFunc("POST /v1/runtime-config/rollback", func(response http.ResponseWriter, request *http.Request) {
		var body rollbackRuntimeConfigRequest
		if request.Body != nil && request.ContentLength != 0 {
			decoded, err := decodeJSON[rollbackRuntimeConfigRequest](response, request)
			if !handleDecode(response, err) {
				return
			}
			body = decoded
		}
		expected, err := expectedConfigVersion(request)
		if !handleDecode(response, err) {
			return
		}
		published, err := app.RollbackRuntimeConfig(service.RollbackRuntimeConfigInput{
			ExpectedVersion: expected,
			PublishedBy:     body.PublishedBy,
		})
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, published)
	})
}

// expectedConfigVersion mirrors expectedVersion but also accepts 0, the
// built-in default config version.
func expectedConfigVersion(request *http.Request) (*int64, error) {
	value := request.Header.Get("X-Expected-Version")
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return nil, domain.FieldError(domain.CodeInvalidInput,
			"X-Expected-Version", "must be a non-negative integer")
	}
	return &parsed, nil
}
