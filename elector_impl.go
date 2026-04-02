package pgelect

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pgelect/pgelect/internal/connector"
)

// New validates cfg, applies defaults, resolves the DB connection, and returns
// a ready Elector. No network connections are opened at this point.
//
// Returns ErrInvalidConfig (use errors.Is) when:
//   - No DB source is provided (DB, DSN, or DBHost)
//   - More than one DB source is provided
//   - AppName or InstanceID are empty
//   - RenewInterval >= LeaseDuration/2
func New(cfg Config) (Elector, error) {
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	db, ownDB, err := cfg.resolveDB()
	if err != nil {
		return nil, err
	}
	e := &elImpl{
		cfg:    cfg,
		db:     db,
		ownDB:  ownDB,
		stopCh: make(chan struct{}),
	}
	e.state.Store(int32(StatePassive))
	return e, nil
}

// elImpl is the concrete Elector implementation.
type elImpl struct {
	cfg   Config
	db    *sql.DB // resolved pool — caller-owned or pgelect-owned
	ownDB bool    // true → pgelect opened the pool and must close it

	state   atomic.Int32
	stopCh  chan struct{}
	stopped atomic.Bool
	mu      sync.Mutex // guards stopCh close
}

// ── Public interface ──────────────────────────────────────────────────────────

func (e *elImpl) IsLeader() bool { return State(e.state.Load()) == StateLeader }
func (e *elImpl) State() State   { return State(e.state.Load()) }

func (e *elImpl) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped.CompareAndSwap(false, true) {
		close(e.stopCh)
	}
}

func (e *elImpl) CreateSchema(ctx context.Context) error {
	stmts := []string{
		fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				app_name    TEXT        NOT NULL,
				instance_id TEXT        NOT NULL,
				status      TEXT        NOT NULL CHECK (status IN ('active','passive')),
				last_seen   TIMESTAMPTZ NOT NULL DEFAULT now(),
				PRIMARY KEY (app_name, instance_id)
			)`, e.cfg.TableName),
		fmt.Sprintf(
			`CREATE INDEX IF NOT EXISTS idx_%s_app_status ON %s (app_name, status)`,
			e.cfg.TableName, e.cfg.TableName,
		),
	}
	for _, stmt := range stmts {
		if _, err := e.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("pgelect: create schema: %w", err)
		}
	}
	return nil
}

func (e *elImpl) Leases(ctx context.Context) ([]LeaseInfo, error) {
	rows, err := e.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT app_name, instance_id, status, last_seen
		FROM   %s
		WHERE  app_name = $1
		ORDER  BY status, instance_id
	`, e.cfg.TableName), e.cfg.AppName)
	if err != nil {
		return nil, fmt.Errorf("pgelect: query leases: %w", err)
	}
	defer rows.Close()
	var out []LeaseInfo
	for rows.Next() {
		var l LeaseInfo
		if err := rows.Scan(&l.AppName, &l.InstanceID, &l.Status, &l.LastSeen); err != nil {
			return nil, fmt.Errorf("pgelect: scan lease: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ── Election loop ─────────────────────────────────────────────────────────────
//
// State machine:
//
//	┌──────────────┐  lock acquired  ┌──────────────┐
//	│   Passive    │────────────────►│    Leader    │
//	└──────┬───────┘                 └──────┬───────┘
//	       │  ▲  DB err / stop              │  ctx / stop / conn-loss
//	       │  │                             ▼
//	       │  │                    ┌──────────────────┐
//	       └──┴────────────────────│  Reconnecting    │
//	          reconnected          └────────┬─────────┘
//	                                        │  stop
//	                                        ▼
//	                               ┌──────────────────┐
//	                               │    Stopped       │
//	                               └──────────────────┘

// Start is the main blocking election loop.
//
// Shutdown is driven by TWO independent signals (both handled):
//  1. ctx cancellation — signal.NotifyContext, fx lifecycle, test timeouts
//  2. Stop()          — internal stopCh, works even with context.Background()
//
// Both are merged into runCtx so every inner select has one Done channel.
// When Start returns, if pgelect owns the DB pool it is closed automatically.
func (e *elImpl) Start(ctx context.Context) error {
	if e.stopped.Load() {
		return ErrStopped
	}

	if e.ownDB {
		defer func() {
			if err := e.db.Close(); err != nil {
				e.cfg.Logger.Warn("pgelect: error closing owned DB pool", "err", err)
			}
		}()
	}

	e.cfg.Logger.Info("pgelect: starting",
		"app", e.cfg.AppName,
		"instance", e.cfg.InstanceID,
		"lockKey", e.cfg.LockKey,
		"leaseDuration", e.cfg.LeaseDuration,
		"renewInterval", e.cfg.RenewInterval,
		"retryInterval", e.cfg.RetryInterval,
		"shutdownTimeout", e.cfg.ShutdownTimeout,
	)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Bridge Stop() → runCtx cancellation.
	go func() {
		select {
		case <-e.stopCh:
			e.cfg.Logger.Info("pgelect: Stop() called")
			cancel()
		case <-runCtx.Done():
		}
	}()

	for {
		if runCtx.Err() != nil {
			e.state.Store(int32(StateStopped))
			e.cfg.Logger.Info("pgelect: stopped", "reason", runCtx.Err())
			return runCtx.Err()
		}
		e.runOneCycle(runCtx)
	}
}

// runOneCycle is one attempt: pin conn → try lock → lead or sleep.
func (e *elImpl) runOneCycle(ctx context.Context) {
	// Check out a dedicated backend connection from the pool.
	// It will NOT be returned to the pool until pin.Close() — this is what
	// makes pgelect safe with PgBouncer in transaction mode.
	pin, err := connector.New(ctx, e.db, e.cfg.TableName)
	if err != nil {
		e.cfg.Logger.Warn("pgelect: pinned conn failed",
			"err", err, "retryIn", e.cfg.ReconnectBackoff)
		e.state.Store(int32(StateReconnecting))
		e.sleepOrStop(ctx, e.cfg.ReconnectBackoff)
		return
	}
	defer func() {
		if cerr := pin.Close(); cerr != nil {
			e.cfg.Logger.Warn("pgelect: error closing pinned conn", "err", cerr)
		}
	}()

	// Non-blocking. Atomic in Postgres — exactly one session wins.
	acquired, err := pin.TryAcquireAdvisoryLock(ctx, e.cfg.LockKey)
	if err != nil {
		e.cfg.Logger.Warn("pgelect: pg_try_advisory_lock error", "err", err)
		e.sleepOrStop(ctx, e.cfg.RetryInterval)
		return
	}

	if !acquired {
		e.state.Store(int32(StatePassive))
		if uerr := pin.UpsertLease(ctx, e.cfg.AppName, e.cfg.InstanceID, string(StatusPassive)); uerr != nil {
			e.cfg.Logger.Warn("pgelect: passive upsert failed", "err", uerr)
		}
		e.cfg.Logger.Debug("pgelect: passive", "retryIn", e.cfg.RetryInterval)
		e.sleepOrStop(ctx, e.cfg.RetryInterval)
		return
	}

	e.becomeLeader(ctx, pin)
}

// becomeLeader runs the full leader lifetime:
//  1. Upsert active row (visible before callback fires — no observer gap)
//  2. Launch OnElected goroutine with leaderCtx
//  3. Run heartbeat loop (blocks until ctx/stop/conn-loss)
//  4. Drain OnElected with hard deadline
//  5. Release lock + mark passive + fire OnRevoked
func (e *elImpl) becomeLeader(ctx context.Context, pin *connector.PinnedConn) {
	if err := pin.UpsertLease(ctx, e.cfg.AppName, e.cfg.InstanceID, string(StatusActive)); err != nil {
		e.cfg.Logger.Error("pgelect: active upsert failed — releasing lock", "err", err)
		if _, rerr := pin.ReleaseAdvisoryLock(ctx, e.cfg.LockKey); rerr != nil {
			e.cfg.Logger.Error("pgelect: release lock in error path failed", "err", rerr)
		}
		return
	}

	e.state.Store(int32(StateLeader))
	e.cfg.Logger.Info("pgelect: became leader",
		"app", e.cfg.AppName, "instance", e.cfg.InstanceID)

	// leaderCtx is always cancelled before we release the advisory lock.
	leaderCtx, cancelLeader := context.WithCancel(ctx)
	defer cancelLeader() // safety net

	electedDone := make(chan struct{})
	if e.cfg.OnElected != nil {
		go func() {
			defer close(electedDone)
			e.cfg.OnElected(leaderCtx)
		}()
	} else {
		close(electedDone)
	}

	// runHeartbeat blocks and always calls cancelLeader before returning.
	e.runHeartbeat(ctx, pin, cancelLeader)

	// Drain OnElected — give it time to finish cleanup after leaderCtx cancel.
	drain := time.NewTimer(e.cfg.OnElectedDrainTimeout)
	defer drain.Stop()
	select {
	case <-electedDone:
		e.cfg.Logger.Info("pgelect: OnElected returned cleanly")
	case <-drain.C:
		e.cfg.Logger.Warn("pgelect: OnElected drain timeout — proceeding with lock release",
			"timeout", e.cfg.OnElectedDrainTimeout)
	}

	// Release — use a fresh context; run ctx is likely cancelled.
	e.state.Store(int32(StatePassive))
	cleanCtx, cleanCancel := context.WithTimeout(context.Background(), e.cfg.ShutdownTimeout)
	defer cleanCancel()

	if released, rerr := pin.ReleaseAdvisoryLock(cleanCtx, e.cfg.LockKey); rerr != nil {
		e.cfg.Logger.Error("pgelect: pg_advisory_unlock error", "err", rerr)
	} else if !released {
		e.cfg.Logger.Warn("pgelect: pg_advisory_unlock false — lock not held by this session")
	}

	if uerr := pin.UpsertLease(cleanCtx, e.cfg.AppName, e.cfg.InstanceID, string(StatusPassive)); uerr != nil {
		e.cfg.Logger.Warn("pgelect: passive upsert after revocation failed", "err", uerr)
	}

	e.cfg.Logger.Info("pgelect: leadership released",
		"app", e.cfg.AppName, "instance", e.cfg.InstanceID)

	if e.cfg.OnRevoked != nil {
		e.cfg.OnRevoked()
	}
}

// runHeartbeat ticks every RenewInterval.
// Watches ctx.Done AND stopCh — Stop() always fires even with context.Background().
// cancelLeader is called exactly once (sync.Once) before this returns.
func (e *elImpl) runHeartbeat(ctx context.Context, pin *connector.PinnedConn, cancelLeader context.CancelFunc) {
	var once sync.Once
	revoke := func(reason string, args ...any) {
		once.Do(func() {
			e.cfg.Logger.Info("pgelect: revoking leadership — "+reason, args...)
			cancelLeader()
		})
	}

	ticker := time.NewTicker(e.cfg.RenewInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			revoke("context cancelled", "reason", ctx.Err())
			return
		case <-e.stopCh:
			revoke("Stop() called")
			return
		case <-ticker.C:
			// Ping first. A silent TCP drop means the advisory lock is already
			// gone. Detect it here before we write a heartbeat that looks healthy
			// to passive nodes but corresponds to a lock we no longer hold.
			if !pin.IsAlive(ctx) {
				e.cfg.Logger.Error("pgelect: pinned connection lost — stepping down")
				e.state.Store(int32(StateReconnecting))
				revoke("connection lost")
				return
			}
			if err := pin.HeartbeatLease(ctx, e.cfg.AppName, e.cfg.InstanceID); err != nil {
				e.cfg.Logger.Error("pgelect: heartbeat failed — stepping down", "err", err)
				e.state.Store(int32(StateReconnecting))
				revoke("heartbeat failed", "err", err)
				return
			}
			e.cfg.Logger.Debug("pgelect: heartbeat sent")
		}
	}
}

// sleepOrStop waits for d, but wakes immediately on ctx cancel or Stop().
func (e *elImpl) sleepOrStop(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-e.stopCh:
	case <-time.After(d):
	}
}

// appNameToLockKey hashes appName → int64 via FNV-32a.
func appNameToLockKey(appName string) int64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(appName))
	return int64(h.Sum32())
}
