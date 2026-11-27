package handler

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"pulse-engine/internal/domain"
)

// errorDetail defines the consistent error payload returned to HTTP clients.
type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

// apiResponse wraps every HTTP response in a predictable JSON envelope.
// Furthermore, embedding RequestID directly in the payload helps clients report the exact trace ID when debugging failures.
type apiResponse struct {
	Data      any          `json:"data,omitempty"`
	Error     *errorDetail `json:"error,omitempty"`
	RequestID string       `json:"request_id,omitempty"`
}

// writeEnvelope marshals the response into memory before writing HTTP headers.
// The reason why we avoid json.NewEncoder(w).Encode directly is that writing headers first would leave the client with a 200 status and a broken body if marshaling failed midway.
func writeEnvelope(w http.ResponseWriter, status int, env apiResponse) {
	payload, err := json.Marshal(env)
	if err != nil {
		slog.Error("failed to marshal response", "error", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)

		var fallbackJSONError = []byte(`{"error":{"code":"INTERNAL_ERROR","message":"internal server error"}}`)
		_, _ = w.Write(fallbackJSONError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}

// writeJSON sends a successful JSON response envelope carrying the request's correlation ID.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, data any) {
	resp := apiResponse{
		Data:      data,
		RequestID: RequestIDFromContext(r.Context()),
	}

	writeEnvelope(w, status, resp)
}

// writeError translates wrapped domain errors into HTTP status codes at a single boundary.
// Moreover, unexpected infrastructure errors are logged server-side while clients only receive a generic 500 message; that's why internal SQL or driver details never leak over HTTP.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	reqID := RequestIDFromContext(r.Context())

	var valErr *domain.ValidationError

	if errors.As(err, &valErr) {
		response := apiResponse{
			Error: &errorDetail{
				Code:    "VALIDATION_ERROR",
				Field:   valErr.Field,
				Message: valErr.Message,
			},
			RequestID: reqID,
		}
		writeEnvelope(w, http.StatusUnprocessableEntity, response)
		return
	}

	if errors.Is(err, domain.ErrTaskNotFound) {
		response := apiResponse{
			Error: &errorDetail{
				Code:    "NOT_FOUND",
				Message: "task not found",
			},
			RequestID: reqID,
		}
		writeEnvelope(w, http.StatusNotFound, response)
		return
	}

	if errors.Is(err, domain.ErrInvalidTransition) {
		response := apiResponse{
			Error: &errorDetail{
				Code:    "CONFLICT",
				Message: "invalid task state transition",
			},
			RequestID: reqID,
		}
		writeEnvelope(w, http.StatusConflict, response)
		return
	}

	slog.ErrorContext(r.Context(), "internal server error", "request_id", reqID, "error", err)

	response := apiResponse{
		Error: &errorDetail{
			Code:    "INTERNAL_ERROR",
			Message: "internal server error",
		},
		RequestID: reqID,
	}
	writeEnvelope(w, http.StatusInternalServerError, response)
}
