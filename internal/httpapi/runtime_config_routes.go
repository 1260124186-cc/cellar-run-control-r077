package httpapi

import (
	"net/http"
	"strings"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/runtimeconfig"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
)

type publishRuntimeConfigRequest struct {
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
	MaxRequestBodyBytes   int64  `json:"max_request_body_bytes"`
	LogLevel              string `json:"log_level"`
	Note                  string `json:"note"`
}

type rollbackRuntimeConfigRequest struct {
	Note string `json:"note"`
}

type runtimeConfigResponse struct {
	Current  configDocumentView  `json:"current"`
	Previous *configDocumentView `json:"previous,omitempty"`
}

type configDocumentView struct {
	Revision              int64     `json:"revision"`
	RequestTimeoutSeconds int       `json:"request_timeout_seconds"`
	MaxRequestBodyBytes   int64     `json:"max_request_body_bytes"`
	LogLevel              string    `json:"log_level"`
	Note                  string    `json:"note,omitempty"`
	PublishedAt           time.Time `json:"published_at"`
}

func configViewOf(document runtimeconfig.Document) configDocumentView {
	return configDocumentView{
		Revision:              document.Revision,
		RequestTimeoutSeconds: document.RequestTimeoutSeconds,
		MaxRequestBodyBytes:   document.MaxRequestBodyBytes,
		LogLevel:              document.LogLevel,
		Note:                  document.Note,
		PublishedAt:           document.PublishedAt,
	}
}

func configViewPointer(document *runtimeconfig.Document) *configDocumentView {
	if document == nil {
		return nil
	}
	view := configViewOf(*document)
	return &view
}

func registerRuntimeConfigRoutes(mux *http.ServeMux, app *service.Service) {
	mux.HandleFunc("GET /v1/runtime-config", func(response http.ResponseWriter, _ *http.Request) {
		history := app.RuntimeConfigHistory()
		writeJSON(response, http.StatusOK, runtimeConfigResponse{
			Current:  configViewOf(history.Current),
			Previous: configViewPointer(history.Previous),
		})
	})

	mux.HandleFunc("POST /v1/runtime-config/publish", func(response http.ResponseWriter, request *http.Request) {
		body, err := decodeJSON[publishRuntimeConfigRequest](response, request)
		if !handleDecode(response, err) {
			return
		}
		expected, err := expectedVersion(request)
		if !handleDecode(response, err) {
			return
		}
		document, err := app.PublishRuntimeConfig(service.PublishConfigInput{
			ExpectedRevision:    expected,
			RequestTimeoutSecs:  body.RequestTimeoutSeconds,
			MaxRequestBodyBytes: body.MaxRequestBodyBytes,
			LogLevel:            body.LogLevel,
			Note:                strings.TrimSpace(body.Note),
		})
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeConfigResult(response, app, document)
	})

	mux.HandleFunc("POST /v1/runtime-config/rollback", func(response http.ResponseWriter, request *http.Request) {
		// The body is optional: empty body, {}, and {"note": "..."} are all
		// accepted. The configured request body limit still applies.
		body, hadBody, err := decodeOptionalJSON[rollbackRuntimeConfigRequest](response, request)
		if err != nil {
			writeDomainError(response, err)
			return
		}
		note := ""
		if hadBody {
			note = strings.TrimSpace(body.Note)
		}
		expected, err := expectedVersion(request)
		if !handleDecode(response, err) {
			return
		}
		document, err := app.RollbackRuntimeConfig(expected, note)
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeConfigResult(response, app, document)
	})
}

func writeConfigResult(response http.ResponseWriter, app *service.Service, document runtimeconfig.Document) {
	history := app.RuntimeConfigHistory()
	writeJSON(response, http.StatusOK, runtimeConfigResponse{
		Current:  configViewOf(document),
		Previous: configViewPointer(history.Previous),
	})
}
