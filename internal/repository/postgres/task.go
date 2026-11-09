package postgres

import (
	"context"
	"errors"
	"fmt"
	domain "pulse-engine/internal/domain"
	"time"

	pgx "github.com/jackc/pgx/v5"
	pgxpool "github.com/jackc/pgx/v5/pgxpool"
)

// TaskRepository implements domain.TaskRepository using PostgreSQL.
// It manages interactions through a bounded connection pool; that's why multiple concurrent workers can execute queries without exhausting OS file descriptors.
type TaskRepository struct {
	pool *pgxpool.Pool
}

func NewTaskRepository(pool *pgxpool.Pool) *TaskRepository {
	return &TaskRepository{
		pool: pool,
	}
}

// Create inserts a new task tuple into the database heap.
// The reason why we use parameterized bind variables ($1 through $5) is to leverage PostgreSQL's Extended Query Protocol so that the values are transmitted separately from the query tree, preventing SQL injection vulnerabilities.
func (r *TaskRepository) Create(ctx context.Context, task *domain.Task) error {
	query := `
	INSERT INTO tasks (id, payload, status, created_at, updated_at) VALUES ($1, $2, $3, $4, $5);`

	_, err := r.pool.Exec(ctx, query, task.ID, task.Payload, task.Status, task.CreatedAt, task.UpdatedAt)

	if err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	return nil
}

// GetByID fetches a specific task by its primary key.
// Moreover, if the record does not exist on disk, we explicitly return nil alongside pgx.ErrNoRows so that the caller can distinguish between missing recordsand network faults.
func (r *TaskRepository) GetByID(ctx context.Context, id string) (*domain.Task, error) {
	query := `SELECT id, payload, status, created_at, updated_at FROM tasks WHERE id = $1;`

	row := r.pool.QueryRow(ctx, query, id)

	var task domain.Task

	err := row.Scan(&task.ID, &task.Payload, &task.Status, &task.CreatedAt, &task.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("get task by id: %w", err)
	}
	return &task, nil
}

// ClaimPendingTask locks and transitions a pending task using pessimistic concurrency control.
// The reason why we execute this within an explicit transaction is to ensure that row locking and status mutation occur atomically; furthermore, FOR UPDATE writes an exclusive lock inn PostgreSQL shared memory, while SKIP LOCKED instructs the engine to bypass rows currently held by concurrent workers. Therefore, multiple workers polling simultaneously will never process the same task twice.
func (r *TaskRepository) ClaimPendingTask(ctx context.Context) (*domain.Task, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	// Defensive rollback: if tx.Commit succeeds later, this becomes a no-op. This is important because any early error return garantees acquired locks are released immediately.
	defer tx.Rollback(ctx)

	query := `
	SELECT id, payload, status, created_at, updated_at
	FROM tasks
	WHERE status = 'PENDING'
	ORDER BY created_at ASC
	LIMIT 1
	FOR UPDATE SKIP LOCKED;`

	row := tx.QueryRow(ctx, query)

	var task domain.Task

	err = row.Scan(&task.ID, &task.Payload, &task.Status, &task.CreatedAt, &task.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Queue is empty or all pending tasks are locked, returning nil cleanly.
			return nil, nil
		}
		return nil, fmt.Errorf("claim pending task: %w", err)
	}

	updateQuery := `UPDATE tasks SET status = $1, updated_at = $2 WHERE id = $3;`
	now := time.Now().UTC()

	_, err = tx.Exec(ctx, updateQuery, domain.StatusProcessing, now, task.ID)
	if err != nil {
		return nil, fmt.Errorf("update task status: %w", err)
	}

	// Update struct state to match disk modifications
	task.Status = domain.StatusProcessing
	task.UpdatedAt = now

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit claim task: %w", err)
	}

	return &task, nil
}
