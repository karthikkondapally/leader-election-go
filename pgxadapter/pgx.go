// Package pgxadapter bridges pgx/v5's pgxpool.Pool to pgelect.
//
//	import "github.com/pgelect/pgelect/pgxadapter"
//
//	pool, _ := pgxpool.New(ctx, connString)
//	el, err := pgxadapter.New(pool, pgelect.Config{
//	    AppName:    "my-worker",
//	    InstanceID: hostname,
//	    OnElected:  func(ctx context.Context) { /* ... */ },
//	})
//
// Internally uses pgx/stdlib.OpenDBFromPool to wrap the pgxpool as a *sql.DB.
// db.Conn(ctx) on that wrapper calls pool.Acquire() — giving a true pinned
// pgx connection for the advisory lock.
package pgxadapter

import (
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/pgelect/pgelect"
)

// New creates a pgelect.Elector from a *pgxpool.Pool.
// Do not set cfg.DB — it is populated by this function.
func New(pool *pgxpool.Pool, cfg pgelect.Config) (pgelect.Elector, error) {
	if pool == nil {
		return nil, fmt.Errorf("pgxadapter: pool must not be nil")
	}
	cfg.DB = stdlib.OpenDBFromPool(pool)
	return pgelect.New(cfg)
}
