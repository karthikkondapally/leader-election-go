// Package fxpgelect integrates pgelect with uber/fx.
//
//	go get github.com/pgelect/pgelect
//
//	import "github.com/pgelect/pgelect/fxpgelect"
//
// # What it does
//
//   - Provides pgelect.Elector to the fx container.
//   - Registers OnStart/OnStop lifecycle hooks.
//   - OnStart: creates schema (if AutoCreateSchema=true), launches Start in a goroutine.
//   - OnStop:  calls Stop(), waits for the goroutine to exit cleanly.
//
// # Usage
//
//	fx.New(
//	    fxpgelect.Module,
//	    fx.Provide(func(db *sql.DB) pgelect.Config {
//	        return pgelect.Config{
//	            DB:         db,
//	            AppName:    "my-worker",
//	            InstanceID: os.Getenv("POD_NAME"),
//	            OnElected:  func(ctx context.Context) { /* leader work */ },
//	        }
//	    }),
//	).Run()
//
// # Config from environment variables
//
//	fx.New(
//	    fxpgelect.Module,
//	    fx.Provide(fxpgelect.ConfigFromEnv),
//	    fx.Decorate(func(cfg pgelect.Config, db *sql.DB, log *slog.Logger) pgelect.Config {
//	        cfg.DB     = db
//	        cfg.Logger = pgelect.NewSlogLogger(log)
//	        cfg.OnElected = func(ctx context.Context) { /* ... */ }
//	        return cfg
//	    }),
//	).Run()
package fxpgelect

import (
	"context"
	"fmt"

	"go.uber.org/fx"

	"github.com/pgelect/pgelect"
)

// Module is the fx.Option to include in fx.New().
// It provides pgelect.Elector and registers OnStart/OnStop hooks.
// Prerequisite: pgelect.Config must be provided to the fx container.
var Module = fx.Module("pgelect",
	fx.Provide(newElector),
	fx.Invoke(registerLifecycle),
)

// ── Provider ──────────────────────────────────────────────────────────────────

type providerParams struct {
	fx.In
	Config pgelect.Config
}

type providerResult struct {
	fx.Out
	Elector pgelect.Elector
}

func newElector(p providerParams) (providerResult, error) {
	el, err := pgelect.New(p.Config)
	if err != nil {
		return providerResult{}, fmt.Errorf("fxpgelect: %w", err)
	}
	return providerResult{Elector: el}, nil
}

// ── Lifecycle ─────────────────────────────────────────────────────────────────

type lifecycleParams struct {
	fx.In
	LC         fx.Lifecycle
	Elector    pgelect.Elector
	Config     pgelect.Config
	Shutdowner fx.Shutdowner `optional:"true"`
}

func registerLifecycle(p lifecycleParams) {
	// loopDone is closed when Start() exits — OnStop waits on it.
	loopDone := make(chan struct{})

	p.LC.Append(fx.Hook{
		// OnStart must return quickly — fx cancels after StartTimeout (15s).
		// We launch Start in a goroutine with context.Background() because
		// the OnStart ctx is cancelled as soon as OnStart returns.
		OnStart: func(ctx context.Context) error {
			if p.Config.AutoCreateSchema {
				if err := p.Elector.CreateSchema(ctx); err != nil {
					return fmt.Errorf("fxpgelect: auto create schema: %w", err)
				}
			}
			go func() {
				defer close(loopDone)
				err := p.Elector.Start(context.Background())
				if err != nil && err != context.Canceled {
					p.Config.Logger.Error("fxpgelect: election loop exited unexpectedly", "err", err)
					if p.Shutdowner != nil {
						_ = p.Shutdowner.Shutdown(fx.ExitCode(1))
					}
				}
			}()
			return nil
		},

		// OnStop is given fx's StopTimeout (default 15s).
		// Shutdown sequence:
		//   Stop() → heartbeat exits → leaderCtx cancelled → OnElected drains
		//   → pg_advisory_unlock → passive row → OnRevoked → loopDone closed
		OnStop: func(ctx context.Context) error {
			p.Elector.Stop()
			select {
			case <-loopDone:
				return nil
			case <-ctx.Done():
				return fmt.Errorf("fxpgelect: timed out waiting for election loop to stop: %w", ctx.Err())
			}
		},
	})
}

// ConfigFromEnv reads pgelect.Config from PGELECT_* environment variables.
// Use with fx.Provide, then fx.Decorate to add DB, Logger, and callbacks.
// See pgelect.ConfigFromEnv for the full list of supported variables.
func ConfigFromEnv() (pgelect.Config, error) {
	return pgelect.ConfigFromEnv()
}
