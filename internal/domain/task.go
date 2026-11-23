package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// TaskStatus defines the operational state of a task.
// That's why we define it as a typed string rather than a raw primitive, ensuring compile-time safety across state machine transitions.
type TaskStatus string

const (
	StatusPending    TaskStatus = "PENDING"
	StatusProcessing TaskStatus = "PROCESSING"
	StatusCompleted  TaskStatus = "COMPLETED"
	StatusFailed     TaskStatus = "FAILED"
)

var (
	ErrTaskNotFound      = errors.New("task not found")
	ErrInvalidTransition = errors.New("invalid state transition")
)

// Task represents the core business entity of our ingestion engine.
// The reason why this struct contains zero database tags (such as db or json) is to keep our domain pure; furthermore, Clean Architecture rules dictate that domain entities must not depend on external storage or transport details.
type Task struct {
	ID        string
	Payload   string
	Status    TaskStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation error field: %s, message: %s", e.Field, e.Message)
}

func NewTask(id, payload string) (*Task, error) {
	trimmedID := strings.TrimSpace(id)
	trimmedPayload := strings.TrimSpace(payload)
	if trimmedID == "" {
		return nil, &ValidationError{Field: "id", Message: "id field is empty"}
	}
	if trimmedPayload == "" {
		return nil, &ValidationError{Field: "payload", Message: "payload field is empty"}
	}

	now := time.Now().UTC()

	return &Task{
		ID:        trimmedID,
		Payload:   trimmedPayload,
		Status:    StatusPending,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func (t *Task) TransitionTo(next TaskStatus) error {
	var valid bool

	switch t.Status {
	case StatusPending:
		valid = next == StatusProcessing
	case StatusProcessing:
		valid = next == StatusCompleted || next == StatusFailed
	}

	if !valid {
		return fmt.Errorf("cannot transition from %s to %s: %w", t.Status, next, ErrInvalidTransition)
	}

	t.Status = next
	t.UpdatedAt = time.Now().UTC()
	return nil
}
