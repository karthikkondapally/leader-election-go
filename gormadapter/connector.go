// Package connector owns every SQL statement executed on the pinned connection.
// All raw SQL is here — audit this file to see exactly what pgelect sends to Postgres.
package connector

import (
	"context"
	"database/sql"
	"fmt"
)

// PinnedConn wraps a *sql.Conn checked out of the pool exclusively.
// It is never returned to the pool while the advisory lock is held.
// Callers MUST call Close() when done.
type PinnedConn struct {
	conn      *sql.Conn
	tableName string
}

// New checks out one connection from db and verifies it is alive.
func New(ctx context.Context, db *sql.DB, tableName string) (*PinnedConn, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("connector: acquire conn: %w", err)
	}
	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("connector: ping failed: %w", err)
	}
	return &PinnedConn{conn: conn, tableName: tableName}, nil
}

// TryAcquireAdvisoryLock calls pg_try_advisory_lock(key) on the pinned conn.
// Returns (true, nil) if acquired, (false, nil) if another session holds it.
// The lock is SESSION-SCOPED: released when this connection closes.
func (p *PinnedConn) TryAcquireAdvisoryLock(ctx context.Context, key int64) (bool, error) {
	var acquired bool
	if err := p.conn.QueryRowContext(ctx,
		`SELECT pg_try_advisory_lock($1)`, key,
	).Scan(&acquired); err != nil {
		return false, fmt.Errorf("connector: pg_try_advisory_lock: %w", err)
	}
	return acquired, nil
}

// ReleaseAdvisoryLock calls pg_advisory_unlock(key) on the pinned conn.
// Returns (false, nil) if this session did not hold the lock — log, not fatal.
func (p *PinnedConn) ReleaseAdvisoryLock(ctx context.Context, key int64) (bool, error) {
	var released bool
	if err := p.conn.QueryRowContext(ctx,
		`SELECT pg_advisory_unlock($1)`, key,
	).Scan(&released); err != nil {
		return false, fmt.Errorf("connector: pg_advisory_unlock: %w", err)
	}
	return released, nil
}

// UpsertLease writes or updates the lease row for this instance.
// status must be "active" or "passive".
func (p *PinnedConn) UpsertLease(ctx context.Context, appName, instanceID, status string) error {
	_, err := p.conn.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (app_name, instance_id, status, last_seen)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (app_name, instance_id)
		DO UPDATE SET status    = EXCLUDED.status,
		              last_seen = now()
	`, p.tableName), appName, instanceID, status)
	if err != nil {
		return fmt.Errorf("connector: upsert lease: %w", err)
	}
	return nil
}

// HeartbeatLease updates last_seen on the pinned connection.
// If the row is missing (table truncated), falls back to UpsertLease.
func (p *PinnedConn) HeartbeatLease(ctx context.Context, appName, instanceID string) error {
	res, err := p.conn.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s SET last_seen = now()
		WHERE  app_name = $1 AND instance_id = $2
	`, p.tableName), appName, instanceID)
	if err != nil {
		return fmt.Errorf("connector: heartbeat: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return p.UpsertLease(ctx, appName, instanceID, "active")
	}
	return nil
}

// IsAlive pings the pinned connection. A false return means the advisory
// lock is already gone — step down immediately.
func (p *PinnedConn) IsAlive(ctx context.Context) bool {
	return p.conn.PingContext(ctx) == nil
}

// Close returns the connection to the pool and releases any held advisory lock.
func (p *PinnedConn) Close() error {
	if p.conn == nil {
		return nil
	}
	err := p.conn.Close()
	p.conn = nil
	return err
}
