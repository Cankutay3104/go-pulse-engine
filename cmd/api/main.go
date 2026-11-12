package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	domain "pulse-engine/internal/domain"
	handler "pulse-engine/internal/handler"
	cached "pulse-engine/internal/repository/cached"
	postgres "pulse-engine/internal/repository/postgres"

	pgx "github.com/jackc/pgx/v5"
	pgxpool "github.com/jackc/pgx/v5/pgxpool"
	godotenv "github.com/joho/godotenv"
	redis "github.com/redis/go-redis/v9"
)

func main() {
	// Configure structured JSON logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	// We parse .env and if the file is absent in production containers, we proceed safely.
	_ = godotenv.Load()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		slog.Error("DATABASE_URL environment variable is not set")
		os.Exit(1)
	}

	config, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		slog.Error("failed to parse database configuration", "error", err)
		os.Exit(1)
	}

	// Connection pool tuning
	config.MaxConns = 15
	config.MaxConnIdleTime = 15 * time.Minute
	config.MaxConnLifetime = 1 * time.Hour
	config.MinConns = 2

	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Initialize connection pool and verify network connectivity immediately
	pool, err := pgxpool.NewWithConfig(pingCtx, config)
	if err != nil {
		slog.Error("failed to allocate connection pool", "error", err)
		os.Exit(1)
	}
	if err = pool.Ping(pingCtx); err != nil {
		slog.Error("failed to ping database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	repo := postgres.NewTaskRepository(pool)

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		slog.Error("REDIS_ADDR environment variable is not set")
		os.Exit(1)
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:         redisAddr,
		PoolSize:     10,
		MinIdleConns: 2,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	})

	redisPingCtx, redisCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer redisCancel()

	if err := rdb.Ping(redisPingCtx).Err(); err != nil {
		slog.Error("cannot ping redis", "error", err)
		os.Exit(1)
	}
	defer rdb.Close()

	cacheTestTask := &domain.Task{
		ID:        "cache-test-001",
		Payload:   `{"job":"cache_test"}`,
		Status:    domain.StatusPending,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}

	ctx := context.Background()

	_, err = pool.Exec(ctx, `DELETE FROM tasks WHERE id = 'cache-test-001';`)
	if err != nil {
		slog.Error("failed to reset the database for cache test", "error", err)
		os.Exit(1)
	}
	err = rdb.Del(ctx, "task:cache-test-001").Err()
	if err != nil {
		slog.Error("failed to delete task instance from database for cache test", "error", err)
		os.Exit(1)
	}

	err = repo.Create(ctx, cacheTestTask)
	if err != nil {
		slog.Error("failed to add task into the database", "error", err)
		os.Exit(1)
	}
	slog.Info("seeded cold task in postgres", "id", cacheTestTask.ID)

	cachedRepo := cached.NewTaskRepository(repo, rdb, 5*time.Minute)

	// Set up HTTP routing with structured request correlation middleware
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handler.HealthHandler)
	mux.HandleFunc("/tasks", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if id == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		start := time.Now()

		task, err := cachedRepo.GetByID(r.Context(), id)
		if err != nil {
			slog.Error("failed to get task from cache", "error", err)
			if errors.Is(err, pgx.ErrNoRows) {
				w.WriteHeader(http.StatusNotFound)
			} else {
				w.WriteHeader(http.StatusInternalServerError)
			}
			return
		}

		latency := time.Since(start)
		slog.InfoContext(r.Context(), "task fetched", "task_id", task.ID, "latency_ms", float64(latency.Microseconds())/1000.0)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		encodeErr := json.NewEncoder(w).Encode(task)
		if encodeErr != nil {
			slog.Error("failed during encoding process", "error", encodeErr)
			return
		}
	})
	wrappedMux := handler.TraceMiddleware(mux)

	srv := &http.Server{
		Addr:         ":8080",
		Handler:      wrappedMux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	if err = srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server fatal crash", "error", err)
		os.Exit(1)
	}
}
