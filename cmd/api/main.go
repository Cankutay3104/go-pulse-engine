package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	handler "pulse-engine/internal/handler"
	cached "pulse-engine/internal/repository/cached"
	postgres "pulse-engine/internal/repository/postgres"
	service "pulse-engine/internal/service"

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

	// Derive a dedicated timeout for Redis so previous database startup time does not eat into its deadline.
	redisPingCtx, redisCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer redisCancel()

	if err := rdb.Ping(redisPingCtx).Err(); err != nil {
		slog.Error("cannot ping redis", "error", err)
		os.Exit(1)
	}
	defer rdb.Close()

	// Wrap the PostgreSQL repository with our Redis Cache-Aside decorator.
	cachedRepo := cached.NewTaskRepository(repo, rdb, 5*time.Minute)

	taskServ := service.NewTaskService(cachedRepo, cachedRepo, nil)
	taskHandler := handler.NewTaskHandler(taskServ, taskServ)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handler.HealthHandler)
	taskHandler.RegisterRoutes(mux)

	wrappedMux := handler.Chain(mux, handler.RequestID, handler.Logger, handler.Recover)

	srv := &http.Server{
		Addr:         ":8080",
		Handler:      wrappedMux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	slog.Info("starting pulse-engine api", "addr", srv.Addr)
	if err = srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server fatal crash", "error", err)
		os.Exit(1)
	}
}
