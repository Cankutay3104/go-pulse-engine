package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	domain "pulse-engine/internal/domain"
)

const (
	defaultListLimit = 20
	maxListLimit     = 100
)

// TaskWriter defines the narrow consumer interface for persisting new tasks.
// That's why the service package owns this 1-method contract instead of importing concrete PostgreSQL or Redis adapters.
type TaskWriter interface {
	Create(ctx context.Context, task *domain.Task) error
}

// TaskReader defines the narrow consumer interface for querying tasks.
// Furthermore, keeping read and write interfaces segregated excludes worker-only methods like ClaimPendingTask from the service boundary.
type TaskReader interface {
	GetByID(ctx context.Context, id string) (*domain.Task, error)
	List(ctx context.Context, limit, offset int) ([]*domain.Task, error)
}

// TaskService orchestrates domain validation, ID generation, and storage calls.
// Moreover, it stays completely unaware of HTTP transport or caching internals; therefore, the same service can later back both REST and gRPC handlers.
type TaskService struct {
	writer TaskWriter
	reader TaskReader
	idGen  func() string
}

// NewTaskService constructs a TaskService with its required storage contracts.
// The reason why idGen is injectable is so unit tests can supply deterministic IDs; furthermore, passing nil falls back to a 128-bit cryptographically random hex generator.
func NewTaskService(writer TaskWriter, reader TaskReader, idGen func() string) *TaskService {
	if idGen == nil {
		idGen = func() string {
			var buf [16]byte

			_, _ = rand.Read(buf[:])

			return hex.EncodeToString(buf[:])
		}
	}

	return &TaskService{
		writer: writer,
		reader: reader,
		idGen:  idGen,
	}

}

// CreateTask generates an ID, enforces domain invariants via domain.NewTask, and persists the task.
func (s *TaskService) CreateTask(ctx context.Context, payload string) (*domain.Task, error) {
	id := s.idGen()

	task, err := domain.NewTask(id, payload)
	if err != nil {
		return nil, fmt.Errorf("create task in service: %w", err)
	}

	err = s.writer.Create(ctx, task)
	if err != nil {
		return nil, fmt.Errorf("create task writer: %w", err)
	}

	return task, nil
}

// GetTask validates the lookup ID and retrieves the matching task from the reader.
// Moreover, wrapping storage errors with %w preserves sentinel errors like domain.ErrTaskNotFound for upstream handlers.
func (s *TaskService) GetTask(ctx context.Context, id string) (*domain.Task, error) {
	trimmedID := strings.TrimSpace(id)
	if trimmedID == "" {
		return nil, &domain.ValidationError{Field: "id", Message: "id field cannot be empty"}
	}

	task, err := s.reader.GetByID(ctx, trimmedID)
	if err != nil {
		return nil, fmt.Errorf("get task in service: %w", err)
	}

	return task, nil
}

// ListTasks enforces safe pagination bounds before querying storage.
// The reason why we clamp limit and offset here is to prevent abusive requests from triggering unbounded scans in PostgreSQL.
func (s *TaskService) ListTasks(ctx context.Context, limit, offset int) ([]*domain.Task, error) {
	if limit <= 0 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}

	if offset < 0 {
		offset = 0
	}

	tasks, err := s.reader.List(ctx, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list tasks in service: %w", err)
	}

	return tasks, nil
}
