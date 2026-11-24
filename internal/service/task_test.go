package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"pulse-engine/internal/domain"
	"pulse-engine/internal/service"
)

// inMemoryRepo is a lightweight fake that satisfies both service.TaskWriter and service.TaskReader.
// That's how we can verify real service behavior and error wrapping in milliseconds without starting PostgreSQL or Redis.
type inMemoryRepo struct {
	mu         sync.RWMutex
	tasks      map[string]*domain.Task
	lastLimit  int
	lastOffset int
	createErr  error
	listErr    error
}

func newInMemoryRepo() *inMemoryRepo {
	return &inMemoryRepo{
		tasks: make(map[string]*domain.Task),
	}
}

func (r *inMemoryRepo) Create(ctx context.Context, task *domain.Task) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.createErr != nil {
		return r.createErr
	}

	r.tasks[task.ID] = task
	return nil
}

func (r *inMemoryRepo) GetByID(ctx context.Context, id string) (*domain.Task, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	task, ok := r.tasks[id]
	if !ok {
		return nil, domain.ErrTaskNotFound
	}
	return task, nil
}

func (r *inMemoryRepo) List(ctx context.Context, limit, offset int) ([]*domain.Task, error) {
	// The reason why we acquire a write lock instead of RLock here is that we mutate lastLimit and lastOffset to record the clamped values passed by TaskService.
	r.mu.Lock()
	defer r.mu.Unlock()

	r.lastLimit = limit
	r.lastOffset = offset

	if r.listErr != nil {
		return nil, r.listErr
	}

	taskSlice := make([]*domain.Task, 0, len(r.tasks))
	for _, task := range r.tasks {
		taskSlice = append(taskSlice, task)
	}
	return taskSlice, nil
}

func TestTaskService_CreateTask(t *testing.T) {
	t.Run("creates task with default random id generator", func(t *testing.T) {
		// We allocate a fresh fake repository inside each subtest; therefore, stored tasks or injected errors from one case can never leak into another.
		repo := newInMemoryRepo()
		serv := service.NewTaskService(repo, repo, nil)

		task, err := serv.CreateTask(context.Background(), "run job")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(task.ID) != 32 {
			t.Fatalf("got ID length %d (%q), want 32", len(task.ID), task.ID)
		}

		// Furthermore, reading back from the fake store proves CreateTask persisted the record rather than only returning a struct in memory.
		stored, err := repo.GetByID(context.Background(), task.ID)
		if err != nil {
			t.Fatalf("expected task to be stored, got error: %v", err)
		}
		if stored.Payload != "run job" {
			t.Fatalf("got stored payload %q, want %q", stored.Payload, "run job")
		}
	})

	t.Run("creates task with custom id generator", func(t *testing.T) {
		repo := newInMemoryRepo()
		serv := service.NewTaskService(repo, repo, func() string { return "task-fixed" })

		task, err := serv.CreateTask(context.Background(), "run job")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if task.ID != "task-fixed" {
			t.Fatalf("got ID %q, want %q", task.ID, "task-fixed")
		}
	})

	t.Run("returns validation error on empty payload", func(t *testing.T) {
		repo := newInMemoryRepo()
		serv := service.NewTaskService(repo, repo, nil)

		task, err := serv.CreateTask(context.Background(), "   ")
		if err == nil {
			t.Fatal("expected validation error, got nil")
		}
		if task != nil {
			t.Fatalf("expected nil task on error, got %+v", task)
		}

		// Moreover, checking errors.As proves that service.CreateTask preserves *domain.ValidationError through %w wrapping.
		var valErr *domain.ValidationError
		if !errors.As(err, &valErr) {
			t.Fatalf("expected *domain.ValidationError, got %T (%v)", err, err)
		}
		if valErr.Field != "payload" {
			t.Fatalf("got validation field %q, want %q", valErr.Field, "payload")
		}
	})

	t.Run("wraps writer storage error", func(t *testing.T) {
		repo := newInMemoryRepo()
		dbErr := errors.New("db connection lost")
		repo.createErr = dbErr

		serv := service.NewTaskService(repo, repo, nil)

		task, err := serv.CreateTask(context.Background(), "valid payload")
		if err == nil {
			t.Fatal("expected storage error, got nil")
		}
		if task != nil {
			t.Fatalf("expected nil task on error, got %+v", task)
		}
		if !errors.Is(err, dbErr) {
			t.Fatalf("got error %v, want wrapped %v", err, dbErr)
		}
	})
}

func TestTaskService_GetTask(t *testing.T) {
	t.Run("returns task and trims lookup id", func(t *testing.T) {
		repo := newInMemoryRepo()
		serv := service.NewTaskService(repo, repo, func() string { return "task-1" })

		if _, err := serv.CreateTask(context.Background(), "run job"); err != nil {
			t.Fatalf("failed to seed task: %v", err)
		}

		// Passing whitespace around the ID verifies that GetTask trims input before querying the repository.
		got, err := serv.GetTask(context.Background(), "  task-1  ")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.ID != "task-1" {
			t.Fatalf("got ID %q, want %q", got.ID, "task-1")
		}
	})

	t.Run("empty id returns validation error", func(t *testing.T) {
		repo := newInMemoryRepo()
		serv := service.NewTaskService(repo, repo, nil)

		got, err := serv.GetTask(context.Background(), "   ")
		if err == nil {
			t.Fatal("expected validation error, got nil")
		}
		if got != nil {
			t.Fatalf("expected nil task on error, got %+v", got)
		}

		var valErr *domain.ValidationError
		if !errors.As(err, &valErr) {
			t.Fatalf("expected *domain.ValidationError, got %T (%v)", err, err)
		}
		if valErr.Field != "id" {
			t.Fatalf("got validation field %q, want %q", valErr.Field, "id")
		}
	})

	t.Run("missing task unwraps ErrTaskNotFound", func(t *testing.T) {
		repo := newInMemoryRepo()
		serv := service.NewTaskService(repo, repo, nil)

		got, err := serv.GetTask(context.Background(), "does-not-exist")
		if err == nil {
			t.Fatal("expected not found error, got nil")
		}
		if got != nil {
			t.Fatalf("expected nil task on error, got %+v", got)
		}
		if !errors.Is(err, domain.ErrTaskNotFound) {
			t.Fatalf("got error %v, want %v", err, domain.ErrTaskNotFound)
		}
	})
}

func TestTaskService_ListTasks(t *testing.T) {
	// We table-test every clamping boundary; that's why abusive pagination parameters are guaranteed to stay bounded before hitting PostgreSQL.
	tests := []struct {
		name       string
		limit      int
		offset     int
		wantLimit  int
		wantOffset int
	}{
		{name: "zero limit defaults to 20", limit: 0, offset: 0, wantLimit: 20, wantOffset: 0},
		{name: "negative limit and offset clamp to defaults", limit: -5, offset: -10, wantLimit: 20, wantOffset: 0},
		{name: "excessive limit caps at 100", limit: 500, offset: 5, wantLimit: 100, wantOffset: 5},
		{name: "valid pagination passes through unchanged", limit: 15, offset: 10, wantLimit: 15, wantOffset: 10},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newInMemoryRepo()
			serv := service.NewTaskService(repo, repo, func() string { return "task-1" })

			if _, err := serv.CreateTask(context.Background(), "run job"); err != nil {
				t.Fatalf("failed to seed task: %v", err)
			}

			tasks, err := serv.ListTasks(context.Background(), tc.limit, tc.offset)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(tasks) != 1 {
				t.Fatalf("got %d tasks, want 1", len(tasks))
			}
			if repo.lastLimit != tc.wantLimit {
				t.Fatalf("got clamped limit %d, want %d", repo.lastLimit, tc.wantLimit)
			}
			if repo.lastOffset != tc.wantOffset {
				t.Fatalf("got clamped offset %d, want %d", repo.lastOffset, tc.wantOffset)
			}
		})
	}

	t.Run("wraps reader list error", func(t *testing.T) {
		repo := newInMemoryRepo()
		dbErr := errors.New("read replica unavailable")
		repo.listErr = dbErr

		serv := service.NewTaskService(repo, repo, nil)

		tasks, err := serv.ListTasks(context.Background(), 10, 0)
		if err == nil {
			t.Fatal("expected list error, got nil")
		}
		if tasks != nil {
			t.Fatalf("expected nil slice on error, got %+v", tasks)
		}
		if !errors.Is(err, dbErr) {
			t.Fatalf("got error %v, want wrapped %v", err, dbErr)
		}
	})
}
