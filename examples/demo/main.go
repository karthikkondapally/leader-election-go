// Command demo shows pgelect leader election running with different DB drivers.
//
// Usage:
//
//	go run ./examples/demo --driver=stdlib   # default: plain database/sql + DSN
//	go run ./examples/demo --driver=pgx      # pgx/v5 pool
//	go run ./examples/demo --driver=gorm     # GORM
//
// Environment variables:
//
//	DATABASE_URL   postgres connection string (required)
//	POD_NAME       instance identifier (default: hostname)
//	APP_NAME       election group name (default: "demo-worker")
//
// Run two terminals pointing at the same DATABASE_URL to watch the election:
//
//	POD_NAME=pod-a go run ./examples/demo
//	POD_NAME=pod-b go run ./examples/demo
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pgelect/pgelect"
)

func main() {
	driver := flag.String("driver", "stdlib", "db driver to use: stdlib | pgx | gorm")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://postgres:postgres@localhost:5432/mydb?sslmode=disable"
	}
	appName    := envOr("APP_NAME", "demo-worker")
	instanceID := envOr("POD_NAME", mustHostname())

	// Base config — same for all drivers.
	// Only the DB source differs between modes.
	base := pgelect.Config{
		AppName:          appName,
		InstanceID:       instanceID,
		LeaseDuration:    15 * time.Second,
		RenewInterval:    5 * time.Second,
		RetryInterval:    5 * time.Second,
		AutoCreateSchema: true,
		Logger:           pgelect.NewSlogLogger(log),

		OnElected: func(ctx context.Context) {
			log.Info("⚡ became leader — starting work",
				"instance", instanceID, "driver", *driver)

			ticker := time.NewTicker(3 * time.Second)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					log.Info("leader context done — stopping work")
					return
				case <-ticker.C:
					log.Info("doing leader-only work...")
				}
			}
		},

		OnRevoked: func() {
			log.Info("revoked — now passive")
		},
	}

	// Build the Elector using whichever driver was requested.
	el, err := buildElector(*driver, dsn, base, log)
	if err != nil {
		log.Error("failed to create elector", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Print cluster state every 10 s so you can observe the election.
	go watchLeases(ctx, el, log)

	log.Info("starting election loop",
		"app", appName, "instance", instanceID, "driver", *driver)

	if err := el.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("elector exited with error", "err", err)
		os.Exit(1)
	}

	log.Info("stopped cleanly")
}

// buildElector constructs an Elector for the requested driver.
// Each branch demonstrates one of the three supported DB input modes.
func buildElector(driver, dsn string, cfg pgelect.Config, log *slog.Logger) (pgelect.Elector, error) {
	switch driver {

	// ── stdlib: DSN string — pgelect opens and owns the pool ─────────────────
	// Simplest setup. No extra imports required beyond lib/pq or pgx stdlib.
	case "stdlib":
		cfg.DSN = dsn
		return pgelect.New(cfg)

	// ── pgx: pgxpool.Pool — community-preferred driver ────────────────────────
	// Uses github.com/pgelect/pgelect/pgxadapter.
	// The adapter wraps the pool as *sql.DB via pgx/stdlib.OpenDBFromPool.
	case "pgx":
		return buildPGX(dsn, cfg)

	// ── gorm: *gorm.DB — ORM integration ─────────────────────────────────────
	// Uses github.com/pgelect/pgelect/gormadapter.
	// The adapter calls gormDB.DB() to extract the underlying *sql.DB.
	// Your existing GORM code is completely unaffected.
	case "gorm":
		return buildGORM(dsn, cfg)

	default:
		return nil, fmt.Errorf("unknown driver %q — use: stdlib, pgx, gorm", driver)
	}
}

func watchLeases(ctx context.Context, el pgelect.Elector, log *slog.Logger) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			leases, err := el.Leases(ctx)
			if err != nil {
				log.Warn("leases query failed", "err", err)
				continue
			}
			for _, l := range leases {
				log.Info("cluster member",
					"instance", l.InstanceID,
					"status", l.Status,
					"lastSeen", time.Since(l.LastSeen).Round(time.Second),
				)
			}
		}
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func mustHostname() string {
	h, _ := os.Hostname()
	if h == "" {
		return "unknown"
	}
	return h
}
