package pgelect

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config is the complete configuration for an Elector.
//
// # Providing a database connection
//
// Provide exactly ONE of the following:
//
//  1. DB — bring your own *sql.DB pool (shared with the rest of your app):
//
//     Config{DB: existingPool, AppName: "...", InstanceID: "..."}
//
//  2. DSN — a PostgreSQL connection string; pgelect opens and owns the pool:
//
//     Config{DSN: "postgres://user:pass@host:5432/db?sslmode=disable", ...}
//
//  3. Explicit fields — individual connection parameters; pgelect builds the DSN:
//
//     Config{DBHost: "localhost", DBPort: 5432, DBName: "mydb",
//            DBUser: "postgres", DBPassword: "secret", DBSSLMode: "disable", ...}
//
// When pgelect opens the pool (DSN or explicit fields), it closes it when
// Start() returns. When you pass DB, you own the lifecycle.
//
// Setting more than one source is an error caught at New() time.
//
// # Timing invariant (enforced at New time)
//
//	RenewInterval < LeaseDuration / 2
//
// This guarantees the leader sends ≥2 heartbeats before passives declare it
// dead, preventing split-brain during transient DB slowness.
type Config struct {

	// ── Database connection — provide exactly ONE ─────────────────────────────

	// DB is a caller-managed *sql.DB pool.
	// pgelect borrows one pinned connection for the advisory lock and uses the
	// pool for passive queries. Caller is responsible for closing after Stop().
	DB *sql.DB

	// DSN is a PostgreSQL connection string (URL or key=value format).
	// pgelect opens a pool internally and closes it when Start() returns.
	//
	// Examples:
	//   "postgres://user:pass@localhost:5432/mydb?sslmode=disable"
	//   "host=localhost port=5432 dbname=mydb user=postgres password=s sslmode=disable"
	DSN string

	// Explicit connection fields. DBHost being non-empty activates this path.
	// pgelect assembles these into a DSN, opens a pool, and closes it on exit.
	//
	// DBPort    defaults to 5432 when zero.
	// DBSSLMode defaults to "require" when empty (secure by default).
	// DBDriver  defaults to "postgres" (lib/pq or pgx stdlib driver name).
	DBHost     string
	DBPort     int
	DBName     string
	DBUser     string
	DBPassword string
	DBSSLMode  string
	DBDriver   string

	// DBPool controls pool sizing when pgelect opens its own pool.
	// Ignored when DB is provided by the caller.
	// Sensible defaults are applied automatically (see DBPoolConfig).
	DBPool DBPoolConfig

	// ── Identity ──────────────────────────────────────────────────────────────

	// AppName identifies the election group (e.g. "payments-worker").
	// Multiple apps share one table without interfering.
	// Required.
	AppName string

	// InstanceID uniquely identifies this process within the group.
	// Recommended values: Kubernetes pod name, hostname+pid, UUID at startup.
	// Must be stable for the process lifetime.
	// Required.
	InstanceID string

	// ── Timing ────────────────────────────────────────────────────────────────

	// LeaseDuration is how stale a leader heartbeat can be before passive nodes
	// attempt takeover. Default: 15s
	LeaseDuration time.Duration

	// RenewInterval is how often the leader updates last_seen.
	// Must be < LeaseDuration/2 (enforced by New). Default: LeaseDuration/3
	RenewInterval time.Duration

	// RetryInterval is how often passive nodes attempt to acquire the lock.
	// Default: 5s
	RetryInterval time.Duration

	// ReconnectBackoff is the wait between reconnection attempts after DB errors.
	// Default: 3s
	ReconnectBackoff time.Duration

	// ShutdownTimeout is the deadline for the post-leadership cleanup phase
	// (pg_advisory_unlock + lease row update). Uses context.Background() so it
	// works even after the run context is cancelled. Default: 10s
	ShutdownTimeout time.Duration

	// OnElectedDrainTimeout is how long pgelect waits for OnElected to return
	// after cancelling leaderCtx before releasing the lock anyway. Prevents a
	// hung OnElected from blocking the advisory lock release indefinitely.
	// Default: same as ShutdownTimeout
	OnElectedDrainTimeout time.Duration

	// ── Storage ───────────────────────────────────────────────────────────────

	// TableName is the lease table name. Default: "leader_leases"
	TableName string

	// LockKey is the PostgreSQL advisory lock ID.
	// Default: FNV-32a hash of AppName. Override for guaranteed uniqueness
	// when you have many apps sharing the same PostgreSQL cluster.
	LockKey int64

	// AutoCreateSchema calls CreateSchema() on Start. Safe (IF NOT EXISTS).
	// Default: false — manage schema via your migration tool.
	AutoCreateSchema bool

	// ── Callbacks ─────────────────────────────────────────────────────────────

	// OnElected is called in a dedicated goroutine when this instance becomes
	// leader. leaderCtx is cancelled when leadership is lost for any reason.
	//
	// Contract:
	//   - Do all leader-only work here.
	//   - Watch leaderCtx.Done() to know when to stop.
	//   - Return promptly after leaderCtx is cancelled (within OnElectedDrainTimeout).
	//   - Do NOT call elector.Stop() from inside OnElected — use leaderCtx.Done().
	OnElected func(leaderCtx interface{ Done() <-chan struct{} })

	// OnRevoked is called synchronously after OnElected returns and the advisory
	// lock is fully released. Use for post-leadership cleanup that must happen
	// exactly once.
	OnRevoked func()

	// ── Observability ─────────────────────────────────────────────────────────

	// Logger receives structured diagnostic messages from pgelect.
	//
	// Built-in adapters (zero extra deps):
	//   pgelect.NewSlogLogger(slog.Default())  — log/slog (recommended)
	//   pgelect.NewStdLogger(log.Default())    — log.Logger
	//   pgelect.NewWriterLogger(os.Stderr)     — any io.Writer
	//   pgelect.NoopLogger()                   — discard (default)
	//
	// Custom: implement the four-method Logger interface (see logger.go).
	Logger Logger
}

// DBPoolConfig controls *sql.DB pool sizing when pgelect opens its own pool.
type DBPoolConfig struct {
	// MaxOpenConns — default 5. pgelect is light: 1 pinned + a few for queries.
	MaxOpenConns int
	// MaxIdleConns — default 2.
	MaxIdleConns int
	// ConnMaxLifetime — default 0 (no limit).
	ConnMaxLifetime time.Duration
	// ConnMaxIdleTime — default 5m.
	ConnMaxIdleTime time.Duration
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// resolveDB validates the DB source, opens a pool if needed, and returns
// ownDB=true when pgelect is responsible for closing it.
func (c *Config) resolveDB() (db *sql.DB, ownDB bool, err error) {
	sources := 0
	if c.DB != nil {
		sources++
	}
	if c.DSN != "" {
		sources++
	}
	if c.DBHost != "" {
		sources++
	}

	switch {
	case sources == 0:
		return nil, false, fmt.Errorf(
			"%w: a database connection is required — set DB, DSN, or DBHost",
			ErrInvalidConfig,
		)
	case sources > 1:
		return nil, false, fmt.Errorf(
			"%w: provide exactly one of DB, DSN, or DBHost — got %d",
			ErrInvalidConfig, sources,
		)
	case c.DB != nil:
		return c.DB, false, nil
	}

	dsn := c.DSN
	if dsn == "" {
		dsn = c.buildDSN()
	}

	driver := c.DBDriver
	if driver == "" {
		driver = "postgres"
	}

	pool, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, false, fmt.Errorf("%w: open DB pool: %v", ErrInvalidConfig, err)
	}

	p := c.DBPool
	if p.MaxOpenConns == 0 {
		p.MaxOpenConns = 5
	}
	if p.MaxIdleConns == 0 {
		p.MaxIdleConns = 2
	}
	if p.ConnMaxIdleTime == 0 {
		p.ConnMaxIdleTime = 5 * time.Minute
	}
	pool.SetMaxOpenConns(p.MaxOpenConns)
	pool.SetMaxIdleConns(p.MaxIdleConns)
	pool.SetConnMaxLifetime(p.ConnMaxLifetime)
	pool.SetConnMaxIdleTime(p.ConnMaxIdleTime)

	return pool, true, nil
}

// buildDSN constructs a postgres:// URL from explicit connection fields.
func (c *Config) buildDSN() string {
	port := c.DBPort
	if port == 0 {
		port = 5432
	}
	ssl := c.DBSSLMode
	if ssl == "" {
		ssl = "require"
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		c.DBUser, c.DBPassword, c.DBHost, port, c.DBName, ssl,
	)
}

// applyDefaults fills zero-value fields with safe production defaults.
func (c *Config) applyDefaults() {
	if c.LeaseDuration == 0 {
		c.LeaseDuration = 15 * time.Second
	}
	if c.RenewInterval == 0 {
		c.RenewInterval = c.LeaseDuration / 3
	}
	if c.RetryInterval == 0 {
		c.RetryInterval = 5 * time.Second
	}
	if c.ReconnectBackoff == 0 {
		c.ReconnectBackoff = 3 * time.Second
	}
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = 10 * time.Second
	}
	if c.OnElectedDrainTimeout == 0 {
		c.OnElectedDrainTimeout = c.ShutdownTimeout
	}
	if c.TableName == "" {
		c.TableName = "leader_leases"
	}
	if c.LockKey == 0 {
		c.LockKey = appNameToLockKey(c.AppName)
	}
	if c.Logger == nil {
		c.Logger = NoopLogger()
	}
}

// validate checks required fields and timing invariant.
// Must be called after applyDefaults.
func (c *Config) validate() error {
	if c.AppName == "" {
		return fmt.Errorf("%w: AppName must not be empty", ErrInvalidConfig)
	}
	if c.InstanceID == "" {
		return fmt.Errorf("%w: InstanceID must not be empty", ErrInvalidConfig)
	}
	if c.RenewInterval >= c.LeaseDuration/2 {
		return fmt.Errorf(
			"%w: RenewInterval (%v) must be < LeaseDuration/2 (%v) — "+
				"violating this risks split-brain",
			ErrInvalidConfig, c.RenewInterval, c.LeaseDuration/2,
		)
	}
	return nil
}

// ── ConfigFromEnv ─────────────────────────────────────────────────────────────

// ConfigFromEnv builds a Config from PGELECT_* environment variables.
// Callbacks (OnElected, OnRevoked) and Logger cannot come from env vars —
// set them on the returned Config before passing to New().
//
// DB connection (set ONE group):
//
//	PGELECT_DSN                 full connection string
//	  — or —
//	PGELECT_DB_HOST             host          (activates explicit-field path)
//	PGELECT_DB_PORT             port          (default: 5432)
//	PGELECT_DB_NAME             database name
//	PGELECT_DB_USER             username
//	PGELECT_DB_PASSWORD         password
//	PGELECT_DB_SSLMODE          sslmode       (default: require)
//	PGELECT_DB_DRIVER           driver name   (default: postgres)
//
// Election settings:
//
//	PGELECT_APP_NAME            string   required
//	PGELECT_INSTANCE_ID         string   default: os.Hostname()
//	PGELECT_TABLE_NAME          string   default: leader_leases
//	PGELECT_LEASE_DURATION      duration default: 15s
//	PGELECT_RENEW_INTERVAL      duration default: LeaseDuration/3
//	PGELECT_RETRY_INTERVAL      duration default: 5s
//	PGELECT_RECONNECT_BACKOFF   duration default: 3s
//	PGELECT_SHUTDOWN_TIMEOUT    duration default: 10s
//	PGELECT_DRAIN_TIMEOUT       duration default: ShutdownTimeout
//	PGELECT_LOCK_KEY            int64    default: FNV32a(AppName)
//	PGELECT_AUTO_CREATE_SCHEMA  bool     default: false
func ConfigFromEnv() (Config, error) {
	var cfg Config

	cfg.DSN = os.Getenv("PGELECT_DSN")
	cfg.DBHost = os.Getenv("PGELECT_DB_HOST")
	cfg.DBName = os.Getenv("PGELECT_DB_NAME")
	cfg.DBUser = os.Getenv("PGELECT_DB_USER")
	cfg.DBPassword = os.Getenv("PGELECT_DB_PASSWORD")
	cfg.DBSSLMode = os.Getenv("PGELECT_DB_SSLMODE")
	cfg.DBDriver = os.Getenv("PGELECT_DB_DRIVER")

	if v := os.Getenv("PGELECT_DB_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return cfg, fmt.Errorf("%w: PGELECT_DB_PORT must be an integer: %v", ErrInvalidConfig, err)
		}
		cfg.DBPort = p
	}

	cfg.AppName = os.Getenv("PGELECT_APP_NAME")
	if cfg.AppName == "" {
		return cfg, fmt.Errorf("%w: PGELECT_APP_NAME is required", ErrInvalidConfig)
	}

	cfg.InstanceID = os.Getenv("PGELECT_INSTANCE_ID")
	if cfg.InstanceID == "" {
		h, err := os.Hostname()
		if err != nil {
			return cfg, fmt.Errorf(
				"%w: PGELECT_INSTANCE_ID not set and os.Hostname() failed: %v",
				ErrInvalidConfig, err,
			)
		}
		cfg.InstanceID = h
	}

	cfg.TableName = os.Getenv("PGELECT_TABLE_NAME")

	for _, f := range []struct {
		key string
		dst *time.Duration
	}{
		{"PGELECT_LEASE_DURATION", &cfg.LeaseDuration},
		{"PGELECT_RENEW_INTERVAL", &cfg.RenewInterval},
		{"PGELECT_RETRY_INTERVAL", &cfg.RetryInterval},
		{"PGELECT_RECONNECT_BACKOFF", &cfg.ReconnectBackoff},
		{"PGELECT_SHUTDOWN_TIMEOUT", &cfg.ShutdownTimeout},
		{"PGELECT_DRAIN_TIMEOUT", &cfg.OnElectedDrainTimeout},
	} {
		if err := parseDuration(f.key, f.dst); err != nil {
			return cfg, err
		}
	}

	if v := os.Getenv("PGELECT_LOCK_KEY"); v != "" {
		k, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return cfg, fmt.Errorf("%w: PGELECT_LOCK_KEY must be a valid int64: %v", ErrInvalidConfig, err)
		}
		cfg.LockKey = k
	}

	if v := os.Getenv("PGELECT_AUTO_CREATE_SCHEMA"); v == "true" || v == "1" {
		cfg.AutoCreateSchema = true
	}

	return cfg, nil
}

func parseDuration(key string, dst *time.Duration) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fmt.Errorf(`%w: %s must be a valid Go duration (e.g. "15s", "1m"): %v`,
			ErrInvalidConfig, key, err)
	}
	*dst = d
	return nil
}
