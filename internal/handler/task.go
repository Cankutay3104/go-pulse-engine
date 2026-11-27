package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"pulse-engine/internal/domain"
)

type TaskCreator interface {
	CreateTask(ctx context.Context, payload string) (*domain.Task, error)
}

type TaskReader interface {
	GetTask(ctx context.Context, id string) (*domain.Task, error)
	ListTasks(ctx context.Context, limit, offset int) ([]*domain.Task, error)
}

type TaskHandler struct {
	creator TaskCreator
	reader  TaskReader
}

func NewTaskHandler(creator TaskCreator, reader TaskReader) *TaskHandler {
	return &TaskHandler{
		creator: creator,
		reader:  reader,
	}
}

type createTaskRequest struct {
	Payload string `json:"payload"`
}

func (h *TaskHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/tasks", h.handleCreateTask)
	mux.HandleFunc("GET /v1/tasks/{id}", h.handleGetTask)
	mux.HandleFunc("GET /v1/tasks", h.handleListTasks)
}

func (h *TaskHandler) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req createTaskRequest

	err := dec.Decode(&req)
	if err != nil {
		writeEnvelope(w, http.StatusBadRequest, apiResponse{
			Error: &errorDetail{
				Code:    "INVALID_REQUEST",
				Message: "invalid JSON request body",
			},
			RequestID: RequestIDFromContext(r.Context()),
		})
		return
	}

	task, err := h.creator.CreateTask(r.Context(), req.Payload)
	if err != nil {
		writeError(w, r, err)
		return
	}

	writeJSON(w, r, http.StatusCreated, task)
}

func (h *TaskHandler) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	task, err := h.reader.GetTask(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}

	writeJSON(w, r, http.StatusOK, task)
}

func (h *TaskHandler) handleListTasks(w http.ResponseWriter, r *http.Request) {
	var limit, offset int
	var err error

	q := r.URL.Query()

	if rawLimit := q.Get("limit"); rawLimit != "" {
		limit, err = strconv.Atoi(rawLimit)
		if err != nil {
			writeEnvelope(w, http.StatusBadRequest, apiResponse{
				Error: &errorDetail{
					Code:    "INVALID_REQUEST",
					Message: "invalid limit in request",
				},
				RequestID: RequestIDFromContext(r.Context()),
			})
			return
		}
	}

	if rawOffset := q.Get("offset"); rawOffset != "" {
		offset, err = strconv.Atoi(rawOffset)
		if err != nil {
			writeEnvelope(w, http.StatusBadRequest, apiResponse{
				Error: &errorDetail{
					Code:    "INVALID_REQUEST",
					Message: "invalid offset in request",
				},
				RequestID: RequestIDFromContext(r.Context()),
			})
			return
		}
	}

	tasks, err := h.reader.ListTasks(r.Context(), limit, offset)
	if err != nil {
		writeError(w, r, err)
		return
	}

	if tasks == nil {
		tasks = []*domain.Task{}
	}

	writeJSON(w, r, http.StatusOK, tasks)
}
