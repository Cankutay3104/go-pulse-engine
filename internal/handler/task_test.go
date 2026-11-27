package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pulse-engine/internal/domain"
	"pulse-engine/internal/handler"
)

// fakeTaskService satisfies both handler.TaskCreator and handler.TaskReader.
// The reason why we store function fields is that each subtest can inject a specific return value or wrapped error without defining separate fake structs.
type fakeTaskService struct {
	createFunc func(ctx context.Context, payload string) (*domain.Task, error)
	getFunc    func(ctx context.Context, id string) (*domain.Task, error)
	listFunc   func(ctx context.Context, limit, offset int) ([]*domain.Task, error)
}

func (f *fakeTaskService) CreateTask(ctx context.Context, payload string) (*domain.Task, error) {
	return f.createFunc(ctx, payload)
}

func (f *fakeTaskService) GetTask(ctx context.Context, id string) (*domain.Task, error) {
	return f.getFunc(ctx, id)
}

func (f *fakeTaskService) ListTasks(ctx context.Context, limit, offset int) ([]*domain.Task, error) {
	return f.listFunc(ctx, limit, offset)
}

// testResponse mirrors the unexported JSON envelope from response.go for black-box assertions.
// Furthermore, using json.RawMessage for Data allows one struct to decode both single-task objects and task-list arrays cleanly.
type testResponse struct {
	Data  json.RawMessage `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Field   string `json:"field"`
	} `json:"error"`
	RequestID string `json:"request_id"`
}

// newTestApp wires the handler and middleware chain identically to cmd/api/main.go; therefore, every test exercises real routing, RequestID, Logger, and Recover.
func newTestApp(serv *fakeTaskService) http.Handler {
	mux := http.NewServeMux()
	h := handler.NewTaskHandler(serv, serv)
	h.RegisterRoutes(mux)
	return handler.Chain(mux, handler.RequestID, handler.Logger, handler.Recover)
}

func decodeResponse(t *testing.T, body []byte) testResponse {
	t.Helper()
	var env testResponse
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("failed to decode JSON response %q: %v", string(body), err)
	}
	return env
}

func TestTaskHandler_CreateTask(t *testing.T) {
	t.Run("valid json returns 201 and echoes request id", func(t *testing.T) {
		serv := &fakeTaskService{
			createFunc: func(ctx context.Context, payload string) (*domain.Task, error) {
				return &domain.Task{ID: "task-1", Payload: payload, Status: domain.StatusPending}, nil
			},
		}
		app := newTestApp(serv)

		req := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(`{"payload":"run"}`))
		req.Header.Set("X-Request-ID", "req-test-123")
		rec := httptest.NewRecorder()

		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("got status %d, want %d (body: %s)", rec.Code, http.StatusCreated, rec.Body.String())
		}
		if gotHeader := rec.Header().Get("X-Request-ID"); gotHeader != "req-test-123" {
			t.Fatalf("got X-Request-ID header %q, want %q", gotHeader, "req-test-123")
		}

		envelope := decodeResponse(t, rec.Body.Bytes())
		if envelope.Error != nil {
			t.Fatalf("expected nil error, got %+v", envelope.Error)
		}
		if envelope.RequestID != "req-test-123" {
			t.Fatalf("got body request_id %q, want %q", envelope.RequestID, "req-test-123")
		}

		var task domain.Task
		if err := json.Unmarshal(envelope.Data, &task); err != nil {
			t.Fatalf("failed to decode task data: %v", err)
		}
		if task.ID != "task-1" {
			t.Fatalf("got task ID %q, want %q", task.ID, "task-1")
		}
	})

	t.Run("unknown json field returns 400", func(t *testing.T) {
		// Leaving createFunc nil ensures the test panics if the handler mistakenly calls the service on malformed input.
		serv := &fakeTaskService{}
		app := newTestApp(serv)

		req := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(`{"wrong":"x"}`))
		rec := httptest.NewRecorder()

		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("got status %d, want %d (body: %s)", rec.Code, http.StatusBadRequest, rec.Body.String())
		}

		envelope := decodeResponse(t, rec.Body.Bytes())
		if envelope.Error == nil {
			t.Fatal("expected error object, got nil")
		}
		if envelope.Error.Code != "INVALID_REQUEST" {
			t.Fatalf("got error code %q, want %q", envelope.Error.Code, "INVALID_REQUEST")
		}
	})

	t.Run("wrapped validation error returns 422", func(t *testing.T) {
		serv := &fakeTaskService{
			createFunc: func(ctx context.Context, payload string) (*domain.Task, error) {
				return nil, fmt.Errorf("service create: %w", &domain.ValidationError{
					Field:   "payload",
					Message: "payload cannot be empty",
				})
			},
		}
		app := newTestApp(serv)

		req := httptest.NewRequest(http.MethodPost, "/v1/tasks", strings.NewReader(`{"payload":""}`))
		rec := httptest.NewRecorder()

		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("got status %d, want %d (body: %s)", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
		}

		envelope := decodeResponse(t, rec.Body.Bytes())
		if envelope.Error == nil {
			t.Fatal("expected error object, got nil")
		}
		if envelope.Error.Code != "VALIDATION_ERROR" {
			t.Fatalf("got error code %q, want %q", envelope.Error.Code, "VALIDATION_ERROR")
		}
		if envelope.Error.Field != "payload" {
			t.Fatalf("got error field %q, want %q", envelope.Error.Field, "payload")
		}
	})
}

func TestTaskHandler_GetTask(t *testing.T) {
	t.Run("returns 200 with task", func(t *testing.T) {
		serv := &fakeTaskService{
			getFunc: func(ctx context.Context, id string) (*domain.Task, error) {
				if id != "task-42" {
					return nil, fmt.Errorf("got id %q, want %q", id, "task-42")
				}
				return &domain.Task{ID: id, Payload: "run", Status: domain.StatusPending}, nil
			},
		}
		app := newTestApp(serv)

		req := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-42", nil)
		rec := httptest.NewRecorder()

		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
		}

		envelope := decodeResponse(t, rec.Body.Bytes())
		if envelope.Error != nil {
			t.Fatalf("expected nil error, got %+v", envelope.Error)
		}

		var task domain.Task
		if err := json.Unmarshal(envelope.Data, &task); err != nil {
			t.Fatalf("failed to decode task data: %v", err)
		}
		if task.ID != "task-42" {
			t.Fatalf("got task ID %q, want %q", task.ID, "task-42")
		}
	})

	// We table-test every wrapped error translation; that's why 404, 409, and 500 boundaries are guaranteed to stay consistent.
	tests := []struct {
		name       string
		servError  error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "wrapped ErrTaskNotFound returns 404",
			servError:  fmt.Errorf("service get: %w", domain.ErrTaskNotFound),
			wantStatus: http.StatusNotFound,
			wantCode:   "NOT_FOUND",
		},
		{
			name:       "wrapped ErrInvalidTransition returns 409",
			servError:  fmt.Errorf("service transition: %w", domain.ErrInvalidTransition),
			wantStatus: http.StatusConflict,
			wantCode:   "CONFLICT",
		},
		{
			name:       "unexpected error returns 500 without leaking internals",
			servError:  errors.New("pgx: secret password failed"),
			wantStatus: http.StatusInternalServerError,
			wantCode:   "INTERNAL_ERROR",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			serv := &fakeTaskService{
				getFunc: func(ctx context.Context, id string) (*domain.Task, error) {
					return nil, tc.servError
				},
			}
			app := newTestApp(serv)

			req := httptest.NewRequest(http.MethodGet, "/v1/tasks/task-1", nil)
			rec := httptest.NewRecorder()

			app.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("got status %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}

			envelope := decodeResponse(t, rec.Body.Bytes())
			if envelope.Error == nil {
				t.Fatal("expected error object, got nil")
			}
			if envelope.Error.Code != tc.wantCode {
				t.Fatalf("got error code %q, want %q", envelope.Error.Code, tc.wantCode)
			}

			// Moreover, verifying the raw response body proves internal SQL or driver strings are never exposed on a 500.
			if tc.wantStatus == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("500 response leaked internal error details: %s", rec.Body.String())
			}
		})
	}
}

func TestTaskHandler_ListTasks(t *testing.T) {
	t.Run("parses limit and offset query params", func(t *testing.T) {
		var gotLimit, gotOffset int
		serv := &fakeTaskService{
			listFunc: func(ctx context.Context, limit, offset int) ([]*domain.Task, error) {
				gotLimit = limit
				gotOffset = offset
				return []*domain.Task{{ID: "task-1", Payload: "run", Status: domain.StatusPending}}, nil
			},
		}
		app := newTestApp(serv)

		req := httptest.NewRequest(http.MethodGet, "/v1/tasks?limit=15&offset=5", nil)
		rec := httptest.NewRecorder()

		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
		}
		if gotLimit != 15 || gotOffset != 5 {
			t.Fatalf("got pagination (%d, %d), want (15, 5)", gotLimit, gotOffset)
		}

		envelope := decodeResponse(t, rec.Body.Bytes())
		if envelope.Error != nil {
			t.Fatalf("expected nil error, got %+v", envelope.Error)
		}

		var tasks []domain.Task
		if err := json.Unmarshal(envelope.Data, &tasks); err != nil {
			t.Fatalf("failed to decode task list: %v", err)
		}
		if len(tasks) != 1 || tasks[0].ID != "task-1" {
			t.Fatalf("unexpected tasks payload: %+v", tasks)
		}
	})

	t.Run("nil slice serializes as empty json array", func(t *testing.T) {
		serv := &fakeTaskService{
			listFunc: func(ctx context.Context, limit, offset int) ([]*domain.Task, error) {
				return nil, nil
			},
		}
		app := newTestApp(serv)

		req := httptest.NewRequest(http.MethodGet, "/v1/tasks", nil)
		rec := httptest.NewRecorder()

		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("got status %d, want %d", rec.Code, http.StatusOK)
		}

		envelope := decodeResponse(t, rec.Body.Bytes())
		if string(envelope.Data) != "[]" {
			t.Fatalf("got data %q, want %q", string(envelope.Data), "[]")
		}
	})

	t.Run("invalid query params return 400", func(t *testing.T) {
		urls := []string{
			"/v1/tasks?limit=abc",
			"/v1/tasks?offset=xyz",
		}

		for _, target := range urls {
			app := newTestApp(&fakeTaskService{})
			req := httptest.NewRequest(http.MethodGet, target, nil)
			rec := httptest.NewRecorder()

			app.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("url %s: got status %d, want %d", target, rec.Code, http.StatusBadRequest)
			}

			envelope := decodeResponse(t, rec.Body.Bytes())
			if envelope.Error == nil || envelope.Error.Code != "INVALID_REQUEST" {
				t.Fatalf("url %s: expected INVALID_REQUEST error, got %+v", target, envelope.Error)
			}
		}
	})

	t.Run("service list error returns 500", func(t *testing.T) {
		serv := &fakeTaskService{
			listFunc: func(ctx context.Context, limit, offset int) ([]*domain.Task, error) {
				return nil, errors.New("read replica down")
			},
		}
		app := newTestApp(serv)

		req := httptest.NewRequest(http.MethodGet, "/v1/tasks", nil)
		rec := httptest.NewRecorder()

		app.ServeHTTP(rec, req)

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("got status %d, want %d", rec.Code, http.StatusInternalServerError)
		}
	})
}

func TestMiddleware_Recover(t *testing.T) {
	// We wire a dedicated panicking route through the full middleware chain; therefore, this test proves both panic recovery and auto-generated request IDs work together.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /panic", func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})
	app := handler.Chain(mux, handler.RequestID, handler.Logger, handler.Recover)

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()

	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("got status %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	reqID := rec.Header().Get("X-Request-ID")
	if len(reqID) != 32 {
		t.Fatalf("got auto-generated X-Request-ID length %d (%q), want 32", len(reqID), reqID)
	}

	envelope := decodeResponse(t, rec.Body.Bytes())
	if envelope.Error == nil || envelope.Error.Code != "INTERNAL_ERROR" {
		t.Fatalf("expected INTERNAL_ERROR envelope, got %+v", envelope.Error)
	}
	if envelope.RequestID != reqID {
		t.Fatalf("got body request_id %q, want header request_id %q", envelope.RequestID, reqID)
	}
}
