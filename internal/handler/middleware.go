package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// ctxKey is an unexported type used for context keys.
// The reason why we do not use a raw string key is that another package using the same string would silently overwrite our value in the context.
type ctxKey string

const requestIDKey ctxKey = "request_id"

// RequestIDFromContext extracts the correlation ID stored by the RequestID middleware.
// Furthermore, if no ID is present in the context, it returns an empty string safely without panicking.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// statusRecorder embeds http.ResponseWriter so it automatically satisfies the full interface while intercepting the outgoing status code.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

// WriteHeader records the first status code written and guards against duplicate header writes.
func (r *statusRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}

	r.status = code
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(code)
}

// Write ensures an implicit 200 OK is recorded if a handler writes a body without calling WriteHeader first.
func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}

// RequestID preserves an incoming X-Request-ID header or generates a 128-bit random hex ID.
// Therefore, every downstream log line and JSON response can be traced back to the exact HTTP request.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Request-ID"))

		if id == "" {
			var buf [16]byte
			_, _ = rand.Read(buf[:])
			id = hex.EncodeToString(buf[:])
		}

		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Logger wraps the ResponseWriter in a statusRecorder and emits one structured access log after the request completes.
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{
			ResponseWriter: w,
			status:         http.StatusOK,
		}

		next.ServeHTTP(rec, r)

		slog.InfoContext(r.Context(), "http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", RequestIDFromContext(r.Context()),
		)
	})
}

// Recover catches panics in downstream handlers so one faulty request never crashes the entire server process.
// Moreover, by placing Recover inside Logger in the middleware chain, recovered panics still record status 500 in the access logs.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}

			reqID := RequestIDFromContext(r.Context())
			slog.ErrorContext(r.Context(), "panic recovered",
				"panic", rec,
				"stack", string(debug.Stack()),
				"request_id", reqID,
			)

			writeEnvelope(w, http.StatusInternalServerError, apiResponse{
				Error: &errorDetail{
					Code:    "INTERNAL_ERROR",
					Message: "internal server error",
				},
				RequestID: reqID,
			})
		}()

		next.ServeHTTP(w, r)
	})
}

// Chain applies middlewares around a base handler in reverse order.
// That's why calling Chain(mux, RequestID, Logger, Recover) executes RequestID first, then Logger, then Recover, and finally mux.
func Chain(h http.Handler, middlewares ...func(http.Handler) http.Handler) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}
