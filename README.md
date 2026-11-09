# PulseEngine

PulseEngine is a concurrent task ingestion and processing service implemented in Go. Built as an exploratory systems project, it focuses on Clean Architecture boundaries, connection pool management, and deterministic concurrency control using PostgreSQL row-level locks.

The primary objective of this project is to implement, test, and document backend systems patterns step-by-step—moving from relational database persistence to in-memory caching and bounded worker pools—while verifying every synchronization guarantee through reproducible tests.

---

## Current Architecture

```
      Client Sockets (HTTP)
               │
               ▼
   ┌───────────────────────┐
   │    TraceMiddleware    │ ──► Generates/Extracts X-Request-ID
   └───────────┬───────────┘     Emits structured JSON logs (log/slog)
               │
               ▼
   ┌───────────────────────┐
   │     HTTP Router       │ ──► Exposes /healthz liveness probe
   └───────────┬───────────┘
               │
               ▼
   ┌───────────────────────┐
   │ TaskRepository (pgx)  │ ──► Manages connection pool (pgxpool)
   └───────────┬───────────┘
               │ Extended Query Protocol ($1..$5)
               ▼
   [ PostgreSQL 18 Database ]
     - Schema: Versioned DDL migrations
     - Concurrency: SELECT ... FOR UPDATE SKIP LOCKED
```

---

## Verified Engineering Patterns

### 1. Clean Architecture Boundaries
- The core business domain (`internal/domain`) defines data structures and storage interfaces with zero third-party dependencies (importing only standard library `context` and `time`).
- The persistence layer (`internal/repository/postgres`) implements these interfaces using raw SQL, decoupling domain logic from the database driver.

### 2. Pessimistic Concurrency Control (Row-Level Locking)
- In concurrent background workers, naive polling (`SELECT WHERE status = 'PENDING'`) leads to duplicate processing anomalies.
- PulseEngine uses an atomic transaction pattern:
  1. Opens an isolated transaction via `tx.Begin(ctx)`.
  2. Acquires and locks a single pending record using `SELECT ... FOR UPDATE SKIP LOCKED`.
  3. Mutates state to `PROCESSING` and commits.
  4. Releases locks automatically on failure via deferred `tx.Rollback(ctx)`.

### 3. Concurrency Verification Gate
The repository includes an automated concurrency test executed directly during startup:
- 5 distinct pending tasks are seeded into a clean relation.
- 20 concurrent worker goroutines compete simultaneously to claim tasks using a bounded connection pool (`MaxConns=15`).
- Results are piped through a thread-safe channel and audited for uniqueness:

```json
{"time":"2026-11-09T19:25:05.7218688+02:00","level":"INFO","msg":"task claimed","worker_id":0,"task_id":"task-1794245105673182000-1"}
{"time":"2026-11-09T19:25:05.7379179+02:00","level":"INFO","msg":"task claimed","worker_id":1,"task_id":"task-1794245105665051700-0"}
{"time":"2026-11-09T19:25:05.7421414+02:00","level":"INFO","msg":"task claimed","worker_id":3,"task_id":"task-1794245105675392800-2"}
{"time":"2026-11-09T19:25:05.7502137+02:00","level":"INFO","msg":"task claimed","worker_id":14,"task_id":"task-1794245105678148000-3"}
{"time":"2026-11-09T19:25:05.7604236+02:00","level":"INFO","msg":"task claimed","worker_id":8,"task_id":"task-1794245105680321900-4"}
{"time":"2026-11-09T19:25:05.7855065+02:00","level":"INFO","msg":"CONCURRENCY GATE PASSED: exactly 5 unique tasks claimed by 20 workers"}
```

Verification Verdict: Exactly 5 workers claim unique tasks; 15 workers receive an empty queue (`nil, nil`). Zero race conditions or duplicate claims detected.

---

## Local Setup & Execution

### Prerequisites
- Go 1.23+
- Docker & Docker Compose

### 1. Configuration
Copy the sample environment file:
```bash
cp .env.example .env
```

### 2. Start PostgreSQL
```bash
docker compose up -d
```

### 3. Run Application
```bash
go run ./cmd/api
```

### 4. Health Check
In a separate terminal:
```bash
curl.exe -i http://localhost:8080/healthz
```

---

## Project Roadmap

- [x] Phase 1: Clean Architecture scaffolding, structured JSON logging (`log/slog`), and request correlation.
- [x] Phase 2: Relational persistence with `pgxpool`, schema migrations, and pessimistic concurrency control (`SKIP LOCKED`).
- [ ] Phase 3: Redis in-memory cache-aside layer and cache invalidation strategies.
- [ ] Phase 4: Atomic distributed rate limiting via Redis Lua scripts (`EVALSHA`).
- [ ] Phase 5: Bounded background worker pools with channel-based backpressure.
- [ ] Phase 6: Production containerization, synthetic load testing (`k6`), and `pprof` runtime profiling.