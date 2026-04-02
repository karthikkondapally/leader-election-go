// Package pgelect implements distributed leader election backed by PostgreSQL
// session-scoped advisory locks and a lease table.
//
// # How it works
//
// One dedicated *sql.Conn is pinned from the pool (never returned while
// leadership is held). pg_try_advisory_lock runs on that connection —
// exactly one process across the cluster wins. The winner fires OnElected
// and starts a heartbeat loop. On shutdown, pg_advisory_unlock is called
// explicitly and the lease row is marked passive.
//
// Because the advisory lock is session-scoped, a process crash automatically
// releases it — no external watchdog or TTL mechanism needed.
//
// # Installation
//
//	go get github.com/pgelect/pgelect
//
// # Quick start — DSN
//
//	el, err := pgelect.New(pgelect.Config{
//	    DSN:        "postgres://user:pass@localhost:5432/mydb?sslmode=disable",
//	    AppName:    "payments-worker",
//	    InstanceID: os.Getenv("POD_NAME"),
//	    Logger:     pgelect.NewSlogLogger(slog.Default()),
//	    OnElected:  func(ctx context.Context) { runLeaderWork(ctx) },
//	})
//
// # Quick start — existing pool
//
//	el, err := pgelect.New(pgelect.Config{
//	    DB:         existingPool,
//	    AppName:    "payments-worker",
//	    InstanceID: os.Getenv("POD_NAME"),
//	})
//
// # With uber/fx
//
//	import "github.com/pgelect/pgelect/fxpgelect"
//	fx.New(fxpgelect.Module, ...)
//
// # With pgx
//
//	import "github.com/pgelect/pgelect/pgxadapter"
//	el, err := pgxadapter.New(pgxPool, cfg)
//
// # With GORM
//
//	import "github.com/pgelect/pgelect/gormadapter"
//	el, err := gormadapter.New(gormDB, cfg)
package pgelect

import "context"

// Elector is the public handle to the leader election loop.
// Obtain one via New(). All methods are safe for concurrent use.
type Elector interface {
	// Start runs the election loop, blocking until ctx is cancelled or Stop
	// is called. Intended to run in a goroutine or as main's blocking call.
	//
	// Returns:
	//   context.Canceled  — clean shutdown (normal)
	//   ErrStopped        — Stop() was called before Start
	Start(ctx context.Context) error

	// Stop signals the loop to shut down gracefully and returns immediately.
	// If leadership is held, the full revocation sequence runs:
	//   leaderCtx cancelled → OnElected drains → pg_advisory_unlock →
	//   lease marked passive → OnRevoked → Start() returns.
	// Idempotent and safe to call from multiple goroutines.
	Stop()

	// IsLeader reports whether this instance currently holds the advisory lock.
	IsLeader() bool

	// State returns the detailed current state of the election loop.
	State() State

	// Leases returns a snapshot of all instances for this AppName.
	// Uses the regular pool (not the pinned connection).
	Leases(ctx context.Context) ([]LeaseInfo, error)

	// CreateSchema creates the lease table and index if they do not exist.
	// Idempotent. Safe to call on every startup.
	// Alternative: add schema.sql to your migration tool.
	CreateSchema(ctx context.Context) error
}
