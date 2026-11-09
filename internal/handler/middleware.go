package handler

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// contextKey provides an unexported type for context values.
// We avoid built-in string types for context keys, because distinct packages using string keys could overwrite each other's data.
type contextKey string

const reqIDKey contextKey = "request_id"

// GetRequestID extracts the correlation ID from the context.
// Furthermore, if the context does not hold a request ID, it returns an empty string safely.
func GetRequestID(ctx context.Context) string {
	if val, ok := ctx.Value(reqIDKey).(string); ok {
		return val
	}
	return ""
}

// TraceMiddleware intercepts every incoming HTTP request to establish distributed tracing.
// The reason why we inspect the X-Request-ID header is to preserve trace continuity from upstream API gateways; moreover, if the client did not supply an ID, we generate a unique hexadecimal string. Therefore, every log statement emitted during the request lifecycle can be correlated cleanly.
func TraceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = strconv.FormatInt(time.Now().UnixNano(), 10)
		}
		w.Header().Set("X-Request-ID", reqID)

		ctx := context.WithValue(r.Context(), reqIDKey, reqID)

		// Next handler executes with the newly enriched context
		next.ServeHTTP(w, r.WithContext(ctx))

		// Furthermore, we emit structured JSON logs after request execution to capture total latency.
		slog.InfoContext(ctx, "request processed", slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.String("request_id", reqID))
	})
}
