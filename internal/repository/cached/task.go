package cached

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	domain "pulse-engine/internal/domain"

	redis "github.com/redis/go-redis/v9"
	singleflight "golang.org/x/sync/singleflight"
)

// TaskRepository decorates a domain.TaskRepository with an in-memory Redis cache.
// That's why it satisfies the exact same domain interface while wrapping both the underlying database adapter and the Redis client.
type TaskRepository struct {
	underlying domain.TaskRepository
	rdb        *redis.Client
	ttl        time.Duration
	group      singleflight.Group
}

// NewTaskRepository initializes the caching decorator with a configurable TTL.
func NewTaskRepository(underlying domain.TaskRepository, rdb *redis.Client, ttl time.Duration) *TaskRepository {
	return &TaskRepository{
		underlying: underlying,
		rdb:        rdb,
		ttl:        ttl,
	}
}

// Create inserts the task into the primary database first.
// Furthermore, if the database write succeeds, we proactively write the record to Redis.
func (r *TaskRepository) Create(ctx context.Context, task *domain.Task) error {
	err := r.underlying.Create(ctx, task)
	if err != nil {
		return err
	}

	key := fmt.Sprintf("task:%s", task.ID)

	data, err := json.Marshal(task)
	if err != nil {
		// The reason why we do not return an error here is that PostgreSQL has already committed the transaction; therefore, a caching failure should only trigger a warning ratherthan aborting the creation flow.
		slog.Warn("serialization fail for caching", "error", err)
		return nil
	}

	_ = r.rdb.Set(ctx, key, data, r.ttl).Err()

	return nil
}

// GetByID implements Cache-Aside pattern.
// It checks Redis RAM first; moreover, if the key is missing or degraded, it transparently falls back to PostgreSQL on disk.
func (r *TaskRepository) GetByID(ctx context.Context, id string) (*domain.Task, error) {
	key := fmt.Sprintf("task:%s", id)

	// Step 1: Querying the in-memory cache
	val, err := r.rdb.Get(ctx, key).Result()
	if err == nil {
		var task domain.Task
		if unmarshalErr := json.Unmarshal([]byte(val), &task); unmarshalErr == nil {
			// Cache hit: the payload is valid, that's why we return immediately without querying PostgreSQL.
			return &task, nil
		} else {
			// If unmarshaling fails, the cached data is corrupted; therefore, we log a warning and fall through to disk.
			slog.Warn("failed to unmarshal cached task", "key", key, "error", unmarshalErr)
		}
	} else if !errors.Is(err, redis.Nil) {
		// The reason why we ignore redis.Nil is that a cache miss is normal; we only log when Redis suffers an unexpected network or socket error.
		slog.Warn("failed to read from cache", "error", err)
	}

	// Step 2: Coalesce concurrent cache misses for the same key into a single database lookup.
	result, err, _ := r.group.Do(key, func() (any, error) {
		// Furthermore, we detach cancellation from the caller's context so one client disconnecting does not abort the shared database query for other waiting goroutines.
		groupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()

		task, err := r.underlying.GetByID(groupCtx, id)
		if err != nil {
			return nil, fmt.Errorf("task does not exist or database failed: %w", err)
		}

		data, err := json.Marshal(task)
		if err != nil || data == nil {
			slog.Warn("failed to serialize task after databse fetch", "key", key, "error", err)
			return task, nil
		}

		if setErr := r.rdb.Set(groupCtx, key, data, r.ttl).Err(); setErr != nil {
			slog.Warn("failed to write task to redis cache", "key", key, "error", setErr)
		}

		return task, nil
	})
	if err != nil {
		return nil, err
	}

	task, ok := result.(*domain.Task)
	if !ok {
		return nil, fmt.Errorf("unexpected type for task: %T", result)
	}

	return task, nil
}

// ClaimPendingTask claims a task through pessimistic locking on disk.
// Because the task state transitions from PENDING to PROCESSING, we must invalidate the cache entry to prevent stale reads.
func (r *TaskRepository) ClaimPendingTask(ctx context.Context) (*domain.Task, error) {
	task, err := r.underlying.ClaimPendingTask(ctx)
	if err != nil || task == nil {
		return task, err
	}

	// Invalidate stale cache record
	key := fmt.Sprintf("task:%s", task.ID)
	if delErr := r.rdb.Del(ctx, key).Err(); delErr != nil {
		slog.Warn("failed to invalidate cached task after claim", "key", key, "error", delErr)
	}

	return task, nil
}
