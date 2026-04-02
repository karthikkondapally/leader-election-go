# pgelect

[![CI](https://github.com/pgelect/pgelect/actions/workflows/ci.yml/badge.svg)](https://github.com/pgelect/pgelect/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/pgelect/pgelect.svg)](https://pkg.go.dev/github.com/pgelect/pgelect)
[![Go Report Card](https://goreportcard.com/badge/github.com/pgelect/pgelect)](https://goreportcard.com/report/github.com/pgelect/pgelect)

Distributed leader election for Go, backed by PostgreSQL session-scoped advisory locks.

If you already run PostgreSQL, you don't need Redis, ZooKeeper, or etcd for leader election.

---

## Features

- **Zero crash-safety configuration** — when the leader process dies, Postgres automatically releases the advisory lock. No watchdogs, no TTLs to tune
- **PgBouncer safe** — one pinned `*sql.Conn` holds the lock; it is never returned to the pool while leadership is held, making it safe with any PgBouncer pooling mode
- **Three DB input modes** — pass a DSN string, explicit fields, or your existing `*sql.DB` pool
- **Full environment variable configuration** — every field settable via `PGELECT_*` vars
- **Pluggable logger** — implement a 4-method interface; built-in adapters for `slog`, `log.Logger`, any `io.Writer`
- **uber/fx ready** — `fxpgelect.Module` for lifecycle-managed DI applications
- **pgx and GORM adapters** included
- **Zero mandatory external deps** in the core package — only stdlib

---

## Installation

```bash
go get github.com/pgelect/pgelect
```

Sub-packages (import only what you use):

```bash
go get github.com/pgelect/pgelect/fxpgelect    # uber/fx integration
go get github.com/pgelect/pgelect/pgxadapter   # pgx/v5 pool
go get github.com/pgelect/pgelect/gormadapter  # GORM
```

---

## Quick start

### With a DSN (pgelect owns the pool)

```go
el, err := pgelect.New(pgelect.Config{
    DSN:        "postgres://user:pass@localhost:5432/mydb?sslmode=disable",
    AppName:    "payments-worker",
    InstanceID: os.Getenv("POD_NAME"),
    Logger:     pgelect.NewSlogLogger(slog.Default()),

    OnElected: func(ctx context.Context) {
        // ctx is cancelled when leadership is lost.
        // Return promptly after ctx.Done() fires.
        runLeaderWork(ctx)
    },
    OnRevoked: func() {
        log.Println("no longer leader")
    },
})
if err != nil {
    log.Fatal(err)
}

ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
defer stop()

// Idempotent — safe to call every startup.
// Alternative: add schema.sql to your migration tool.
if err := el.CreateSchema(ctx); err != nil {
    log.Fatal(err)
}

// Blocks until ctx is cancelled or Stop() is called.
if err := el.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
    log.Fatal(err)
}
```

### With explicit connection fields

```go
el, err := pgelect.New(pgelect.Config{
    DBHost:     "db.internal",
    DBPort:     5432,
    DBName:     "mydb",
    DBUser:     "svc_account",
    DBPassword: secrets.Get("db-password"),
    DBSSLMode:  "verify-full",

    AppName:    "payments-worker",
    InstanceID: os.Getenv("POD_NAME"),
})
```

### With your existing `*sql.DB`

```go
el, err := pgelect.New(pgelect.Config{
    DB:         existingPool,   // caller manages lifecycle
    AppName:    "payments-worker",
    InstanceID: os.Getenv("POD_NAME"),
})
```

---

## DB connection modes

Provide exactly **one** of these. Providing more than one is an error caught at `New()` time.

| Mode | Field(s) | Who closes the pool |
|---|---|---|
| Full DSN | `DSN` | pgelect (on `Start()` return) |
| Explicit fields | `DBHost` + `DBPort` + `DBName` + `DBUser` + `DBPassword` + `DBSSLMode` | pgelect (on `Start()` return) |
| Existing pool | `DB` | Caller |

When pgelect opens its own pool, it applies conservative sizing (`MaxOpenConns=5`, `MaxIdleConns=2`, `ConnMaxIdleTime=5m`). Override via `DBPool`:

```go
pgelect.Config{
    DSN: "...",
    DBPool: pgelect.DBPoolConfig{
        MaxOpenConns:    10,
        MaxIdleConns:    3,
        ConnMaxIdleTime: 2 * time.Minute,
    },
}
```

---

## Configuration reference

| Field | Env var | Default | Description |
|---|---|---|---|
| `DSN` | `PGELECT_DSN` | — | Full PostgreSQL connection string |
| `DBHost` | `PGELECT_DB_HOST` | — | Host (activates explicit-field path) |
| `DBPort` | `PGELECT_DB_PORT` | `5432` | Port |
| `DBName` | `PGELECT_DB_NAME` | — | Database name |
| `DBUser` | `PGELECT_DB_USER` | — | Username |
| `DBPassword` | `PGELECT_DB_PASSWORD` | — | Password |
| `DBSSLMode` | `PGELECT_DB_SSLMODE` | `require` | SSL mode |
| `DBDriver` | `PGELECT_DB_DRIVER` | `postgres` | driver/sql driver name |
| `AppName` | `PGELECT_APP_NAME` | **required** | Election group identifier |
| `InstanceID` | `PGELECT_INSTANCE_ID` | `os.Hostname()` | Unique per-process ID |
| `TableName` | `PGELECT_TABLE_NAME` | `leader_leases` | Lease table name |
| `LockKey` | `PGELECT_LOCK_KEY` | `FNV32a(AppName)` | Advisory lock integer ID |
| `LeaseDuration` | `PGELECT_LEASE_DURATION` | `15s` | Max stale heartbeat before takeover |
| `RenewInterval` | `PGELECT_RENEW_INTERVAL` | `LeaseDuration/3` | Leader heartbeat frequency |
| `RetryInterval` | `PGELECT_RETRY_INTERVAL` | `5s` | Passive retry frequency |
| `ReconnectBackoff` | `PGELECT_RECONNECT_BACKOFF` | `3s` | Wait after DB errors |
| `ShutdownTimeout` | `PGELECT_SHUTDOWN_TIMEOUT` | `10s` | Cleanup deadline after step-down |
| `OnElectedDrainTimeout` | `PGELECT_DRAIN_TIMEOUT` | `=ShutdownTimeout` | Max wait for `OnElected` to return |
| `AutoCreateSchema` | `PGELECT_AUTO_CREATE_SCHEMA` | `false` | Create table on start |

### Timing invariant

```
RenewInterval < LeaseDuration / 2
```

Enforced at `New()` time. This ensures the leader sends ≥2 heartbeats before passive nodes declare it dead, preventing split-brain during transient DB slowness.

---

## Logging

```go
type Logger interface {
    Debug(msg string, keysAndValues ...any)
    Info(msg string, keysAndValues ...any)
    Warn(msg string, keysAndValues ...any)
    Error(msg string, keysAndValues ...any)
}
```

### Built-in adapters

```go
pgelect.NewSlogLogger(slog.Default())   // log/slog — recommended
pgelect.NewStdLogger(log.Default())     // log.Logger
pgelect.NewWriterLogger(os.Stderr)      // any io.Writer
pgelect.NoopLogger()                    // discard (default)
```

### Custom adapters

```go
// zap
type zapLogger struct{ s *zap.SugaredLogger }
func (z zapLogger) Debug(msg string, kv ...any) { z.s.Debugw(msg, kv...) }
func (z zapLogger) Info(msg string, kv ...any)  { z.s.Infow(msg, kv...)  }
func (z zapLogger) Warn(msg string, kv ...any)  { z.s.Warnw(msg, kv...)  }
func (z zapLogger) Error(msg string, kv ...any) { z.s.Errorw(msg, kv...) }

cfg.Logger = zapLogger{s: zapLogger.Sugar()}

// logrus
type logrusLogger struct{ l *logrus.Logger }
func (r logrusLogger) Debug(msg string, kv ...any) { r.l.WithFields(toFields(kv)).Debug(msg) }
func (r logrusLogger) Info(msg string, kv ...any)  { r.l.WithFields(toFields(kv)).Info(msg)  }
func (r logrusLogger) Warn(msg string, kv ...any)  { r.l.WithFields(toFields(kv)).Warn(msg)  }
func (r logrusLogger) Error(msg string, kv ...any) { r.l.WithFields(toFields(kv)).Error(msg) }
```

---

## uber/fx

```go
import "github.com/pgelect/pgelect/fxpgelect"

fx.New(
    fxpgelect.Module,   // provides Elector, registers OnStart/OnStop

    // Option A: full config in code
    fx.Provide(func(db *sql.DB, log *slog.Logger) pgelect.Config {
        return pgelect.Config{
            DB:         db,
            AppName:    "my-worker",
            InstanceID: os.Getenv("POD_NAME"),
            Logger:     pgelect.NewSlogLogger(log),
            OnElected:  func(ctx context.Context) { /* leader work */ },
        }
    }),

    // Option B: env vars + decorator
    // fx.Provide(fxpgelect.ConfigFromEnv),
    // fx.Decorate(func(cfg pgelect.Config, db *sql.DB) pgelect.Config {
    //     cfg.DB = db
    //     cfg.OnElected = func(ctx context.Context) { /* ... */ }
    //     return cfg
    // }),
).Run()
```

**Lifecycle:**
- `OnStart` → schema (if `AutoCreateSchema=true`), then `el.Start(context.Background())` in a goroutine
- `OnStop` → `el.Stop()`, waits for goroutine to exit (lock released, `OnRevoked` fired) within fx's `StopTimeout`

---

## pgx / GORM

```go
// pgx
import "github.com/pgelect/pgelect/pgxadapter"

pool, _ := pgxpool.New(ctx, connString)
el, err := pgxadapter.New(pool, pgelect.Config{
    AppName:   "my-worker",
    OnElected: func(ctx context.Context) { /* ... */ },
})

// GORM
import "github.com/pgelect/pgelect/gormadapter"

gormDB, _ := gorm.Open(postgres.Open(dsn), &gorm.Config{})
el, err := gormadapter.New(gormDB, pgelect.Config{
    AppName:   "my-worker",
    OnElected: func(ctx context.Context) { /* ... */ },
})
```

---

## PgBouncer

The advisory lock lives on a **pinned `*sql.Conn`** checked out via `db.Conn(ctx)` and held for the full leadership lifetime. PgBouncer cannot recycle it regardless of pooling mode.

| PgBouncer mode | Normal queries | pgelect lock connection |
|---|---|---|
| Session pooling | ✅ | ✅ |
| Transaction pooling | ✅ | ✅ — pinned conn bypasses recycling |
| Statement pooling | ✅ | ✅ — pinned conn bypasses recycling |

For maximum safety in production, point pgelect's DSN directly at Postgres (bypassing PgBouncer), or use a PgBouncer listener with `pool_mode=session` dedicated to advisory locks.

---

## How it works

```
App start
  └─► New(cfg)
        ├─ applyDefaults, validate
        └─ resolveDB → open pool (if DSN/DBHost) or borrow (if DB)

el.Start(ctx)
  └─► loop:
        ├─ db.Conn(ctx)                    ← pin one connection from pool
        ├─ pg_try_advisory_lock(key)       ← atomic, non-blocking
        │
        ├─ ACQUIRED → UpsertLease("active")
        │             OnElected(leaderCtx) [goroutine]
        │             heartbeat loop:
        │               every RenewInterval:
        │                 PingContext(pinnedConn)   ← detect silent TCP drops
        │                 UPDATE last_seen          ← on pinned conn
        │               on ctx/Stop/ping-fail:
        │                 cancelLeader()            ← OnElected ctx.Done() fires
        │             drain OnElected (OnElectedDrainTimeout)
        │             pg_advisory_unlock(key)       ← explicit release
        │             UpsertLease("passive")
        │             OnRevoked()
        │
        └─ NOT ACQUIRED → UpsertLease("passive")
                          sleep(RetryInterval)
                          try again

el.Stop() / ctx cancel
  └─► stopCh closed → heartbeat exits → revocation sequence → Start() returns
      (if pgelect owns the pool: db.Close())
```

---

## Failure scenarios

| Scenario | Behaviour |
|---|---|
| **Leader process crashes** | OS closes TCP → Postgres auto-releases advisory lock → passives detect stale `last_seen` within `LeaseDuration` and race for the lock |
| **Silent TCP drop** (firewall, idle timeout) | `PingContext` on pinned conn fails → immediate step-down → `leaderCtx` cancelled → `OnElected` stops → lock released |
| **DB restart** | All advisory locks dropped → all nodes reconnect; first to win `pg_try_advisory_lock` becomes leader |
| **Two nodes race simultaneously** | `pg_try_advisory_lock` is atomic in Postgres; exactly one wins |
| **`OnElected` hangs** | `OnElectedDrainTimeout` fires → lock released anyway; hung goroutine is leaked but cluster is safe |
| **PgBouncer recycles connections** | Not applicable to pinned conn — held by Go |

---

## Repository layout

```
github.com/pgelect/pgelect/
├── elector.go            Elector interface (public API)
├── elector_impl.go       State machine implementation
├── config.go             Config struct, validation, defaults, ConfigFromEnv
├── state.go              State, Status, LeaseInfo types
├── logger.go             Logger interface + built-in adapters
├── errors.go             ErrStopped, ErrInvalidConfig
├── elector_test.go       Unit tests (35, no DB needed)
├── logger_test.go        Logger adapter tests
├── schema.sql            Standalone migration file
├── go.mod                Single module — all deps
├── Makefile              Developer tasks
├── .env.example          Local dev config template
├── .github/workflows/    CI (unit + integration + lint)
│
├── internal/
│   └── connector/        All raw SQL on the pinned connection
│
├── fxpgelect/            uber/fx Module + lifecycle hooks
├── pgxadapter/           pgx/v5 pool adapter
├── gormadapter/          GORM adapter
│
└── examples/
    ├── rawsql/           Plain database/sql (all 3 DB input modes)
    ├── withfx/           uber/fx application
    ├── withpgx/          pgx/v5
    └── withgorm/         GORM
```

---

## Development

```bash
# Start a local Postgres
make docker-pg

# Run unit tests (no DB needed)
make test

# Run with race detector
make test-race

# Run integration tests
DATABASE_URL="postgres://postgres:postgres@localhost:5432/mydb?sslmode=disable" \
make test-integration

# Run an example
DATABASE_URL="postgres://..." make example-rawsql
```

---

## License

MIT
