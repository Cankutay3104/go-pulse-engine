package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	domain "pulse-engine/internal/domain"
	handler "pulse-engine/internal/handler"
	postgres "pulse-engine/internal/repository/postgres"

	pgxpool "github.com/jackc/pgx/v5/pgxpool"
	godotenv "github.com/joho/godotenv"
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

	// Verification: Truncate table to guarantee derterministic test execution
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "TRUNCATE TABLE tasks;"); err != nil {
		slog.Error("failed to truncate tasks table", "error", err)
		os.Exit(1)
	}

	// Seed exactly 5 distinct tasks to prepare for the concurrency test
	for i := range 5 {
		newTask := &domain.Task{
			ID:        fmt.Sprintf("task-%d-%d", time.Now().UnixNano(), i),
			Payload:   `{"job":"concurrency_test"}`,
			Status:    domain.StatusPending,
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}

		if err := repo.Create(context.Background(), newTask); err != nil {
			slog.Error("failed to seed task", "task_id", newTask.ID, "error", err)
			os.Exit(1)
		}
	}

	// Launch 20 concurrent worker routines competing for the 5 seeded tasks
	var wg sync.WaitGroup
	claimedChan := make(chan string, 20)

	for i := 0; i < 20; i++ {
		wg.Add(1)

		go func(workerID int) {
			defer wg.Done()

			task, err := repo.ClaimPendingTask(context.Background())
			if err != nil {
				slog.Error("worker failed", "worker_id", workerID, "error", err)
				return
			}

			if task != nil {
				claimedChan <- task.ID
				slog.Info("task claimed", "worker_id", workerID, "task_id", task.ID)
			}
		}(i)
	}

	wg.Wait()
	close(claimedChan)

	// Audit claimed tasks: assert strict deduplication and count matching
	taskCount := 0
	idMap := make(map[string]int)

	for id := range claimedChan {
		taskCount++
		idMap[id]++

		if idMap[id] > 1 {
			slog.Error("duplicate task claimed", "task_id", id)
			os.Exit(1)
		}
	}

	if taskCount != 5 || len(idMap) != 5 {
		slog.Error("concurrency gate failed", "claimed", taskCount)
		os.Exit(1)
	}

	slog.Info("CONCURRENCY GATE PASSED: exactly 5 unique tasks claimed by 20 workers")

	// Set up HTTP routing with structured request correlation middleware
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handler.HealthHandler)
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
