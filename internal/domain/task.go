package domain

import (
	"context"
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

// Task represents the core business entity of our ingestion engine.
// The reason why this struct contains zero database tags (such as db or json) is to keep our domain pure; furthermore, Clean Architecture rules dictate that domain entities must not depend on external storage or transport details.
type Task struct {
	ID        string
	Payload   string
	Status    TaskStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TaskRepository establishes the storage contract for our domain.
// Therefore, any underlying persistence engine (PostgreSQL, Redis, or memory) must satisfy these method signatures without leaking its driver implementation.
type TaskRepository interface {
	Create(ctx context.Context, task *Task) error
	GetByID(ctx context.Context, id string) (*Task, error)
	ClaimPendingTask(ctx context.Context) (*Task, error)
}
