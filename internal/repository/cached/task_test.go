package cached_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domain "pulse-engine/internal/domain"
	"pulse-engine/internal/repository/cached"

	redis "github.com/redis/go-redis/v9"
)

type stubTaskRepo struct {
	calls atomic.Int64
}

func (s *stubTaskRepo) Create(ctx context.Context, task *domain.Task) error {
	return nil
}

func (s *stubTaskRepo) ClaimPendingTask(ctx context.Context) (*domain.Task, error) {
	return nil, nil
}

func (s *stubTaskRepo) GetByID(ctx context.Context, id string) (*domain.Task, error) {
	s.calls.Add(1)

	time.Sleep(50 * time.Millisecond)

	return &domain.Task{
		ID:      id,
		Payload: `{"job": "stampede"}`,
		Status:  domain.StatusPending,
	}, nil
}

func TestGetByID_SingleflightCoalescing(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})

	pingCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := rdb.Ping(pingCtx).Err(); err != nil {
		t.Fatalf("redis unavailable %v", err)
	}

	t.Cleanup(func() { _ = rdb.Close() })
	err := rdb.Del(pingCtx, "task:stampede-1").Err()
	if err != nil {
		t.Fatalf("test key could not be cleared: %v", err)
	}

	stub := &stubTaskRepo{}
	repo := cached.NewTaskRepository(stub, rdb, 5*time.Minute)

	errCh := make(chan error, 50)

	var wg sync.WaitGroup

	for range 50 {
		wg.Go(func() {
			task, err := repo.GetByID(pingCtx, "stampede-1")
			if err != nil {
				errCh <- err
				return
			}
			if task == nil || task.ID != "stampede-1" {
				errCh <- fmt.Errorf("task contents are invalid")
			}
		})
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("worker error: %v", err)
	}

	got := stub.calls.Load()
	if got != 1 {
		t.Fatalf("expected 1 database call, got %d", got)
	}

}
