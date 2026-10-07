package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// defaultMaxRequestBody applies only when a request reaches a decoding
// handler without a pinned runtime configuration (all real requests are
// wrapped by the runtime configuration middleware).
const defaultMaxRequestBody = 1 << 20

type errorBody struct {
	Code   domain.Code `json:"code"`
	Detail string      `json:"detail"`
	Field  string      `json:"field,omitempty"`
}

type errorResponse struct {
	Error errorBody `json:"error"`
}

// decodeJSON reads exactly one JSON object using the request body limit of
// the runtime configuration revision pinned for this request.
func decodeJSON[T any](response http.ResponseWriter, request *http.Request) (T, error) {
	var value T
	if request.Body == nil {
		return value, domain.NewError(domain.CodeInvalidInput, "request body is required")
	}
	decoder := newLimitedJSONDecoder(response, request)
	if err := decoder.Decode(&value); err != nil {
		return value, decodeError(err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return value, domain.NewError(domain.CodeInvalidInput,
				"request body must contain one JSON object")
		}
		return value, decodeError(err)
	}
	return value, nil
}

// decodeOptionalJSON behaves like decodeJSON but tolerates an absent or empty
// body. hadBody is false when no JSON object was present.
func decodeOptionalJSON[T any](response http.ResponseWriter, request *http.Request) (value T, hadBody bool, err error) {
	if request.Body == nil {
		return value, false, nil
	}
	decoder := newLimitedJSONDecoder(response, request)
	if decodeErr := decoder.Decode(&value); decodeErr != nil {
		if isEmptyBodyError(decodeErr) {
			return value, false, nil
		}
		return value, false, decodeError(decodeErr)
	}
	var extra any
	if decodeErr := decoder.Decode(&extra); !errors.Is(decodeErr, io.EOF) {
		if decodeErr == nil {
			return value, false, domain.NewError(domain.CodeInvalidInput,
				"request body must contain one JSON object")
		}
		return value, false, decodeError(decodeErr)
	}
	return value, true, nil
}

func newLimitedJSONDecoder(response http.ResponseWriter, request *http.Request) *json.Decoder {
	limit := configViewFrom(request).MaxRequestBodyBytes
	if limit <= 0 {
		limit = defaultMaxRequestBody
	}
	body := http.MaxBytesReader(response, request.Body, limit)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	return decoder
}

func isEmptyBodyError(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func decodeError(err error) error {
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		return domain.NewError(domain.CodeInvalidInput, "request body exceeds the configured limit")
	}
	return domain.FieldError(domain.CodeInvalidInput, "body",
		"must be valid JSON with known fields")
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeDomainError(response http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	body := errorBody{
		Code:   domain.CodeStorage,
		Detail: "internal service failure",
	}
	var domainErr *domain.Error
	if errors.As(err, &domainErr) {
		body.Code = domainErr.Code
		body.Detail = domainErr.Message
		body.Field = domainErr.Field
		status = statusForCode(domainErr.Code)
	}
	writeJSON(response, status, errorResponse{Error: body})
}

func statusForCode(code domain.Code) int {
	switch code {
	case domain.CodeInvalidInput:
		return http.StatusBadRequest
	case domain.CodeFormulaNotFound, domain.CodeVesselNotFound, domain.CodeRunNotFound:
		return http.StatusNotFound
	case domain.CodeFormulaNameConflict, domain.CodeFormulaState,
		domain.CodeFormulaInUse, domain.CodeVesselConflict,
		domain.CodeVesselUnavailable, domain.CodeVesselState,
		domain.CodeRunConflict, domain.CodeRunState,
		domain.CodeObservationConflict, domain.CodeConfigNoPrevious:
		return http.StatusConflict
	case domain.CodeRevisionConflict:
		return http.StatusPreconditionFailed
	case domain.CodeStorage:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

func expectedVersion(request *http.Request) (*int64, error) {
	value := request.Header.Get("X-Expected-Version")
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 1 {
		return nil, domain.FieldError(domain.CodeInvalidInput,
			"X-Expected-Version", "must be a positive integer")
	}
	return &parsed, nil
}

func handleDecode(response http.ResponseWriter, err error) bool {
	if err == nil {
		return true
	}
	writeDomainError(response, err)
	return false
}
