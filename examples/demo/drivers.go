package main

// This file contains the driver-specific Elector constructors.
// They live in a separate file so main.go stays readable even though all
// three drivers are compiled into the same binary.
//
// In a real application you would import only one adapter — not all three.
// The demo binary is the exception: it imports all of them so you can
// switch at runtime with --driver=.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"

	"github.com/pgelect/pgelect"
	"github.com/pgelect/pgelect/gormadapter"
	"github.com/pgelect/pgelect/pgxadapter"
)

// buildPGX creates an Elector backed by a pgx/v5 connection pool.
func buildPGX(dsn string, cfg pgelect.Config) (pgelect.Elector, error) {
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}
	// pool.Close() will be called when the process exits.
	// In production, defer pool.Close() in main after el.Start returns.
	return pgxadapter.New(pool, cfg)
}

// buildGORM creates an Elector backed by a GORM *gorm.DB.
// The gormadapter extracts the underlying *sql.DB — no GORM APIs are used
// by pgelect internally; it goes straight to database/sql.
func buildGORM(dsn string, cfg pgelect.Config) (pgelect.Elector, error) {
	gormDB, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		// Silence GORM's own query logger in the demo — pgelect has its own.
		Logger: gormLogger.Default.LogMode(gormLogger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("gorm.Open: %w", err)
	}
	return gormadapter.New(gormDB, cfg)
}
