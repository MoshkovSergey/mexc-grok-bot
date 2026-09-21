// Package db provides PostgreSQL connection and embedded migration bootstrap.
package db

import (
	"context"
	"database/sql"
	_ "embed"
	"time"

	// pgx stdlib driver registers database/sql driver name "pgx".
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/001_init.sql
var migrationSQL string

// Open creates a PostgreSQL connection pool.
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	database, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}

	if err := database.PingContext(ctx); err != nil {
		return nil, err
	}

	// Conservative pool settings for a single-node bot.
	database.SetMaxOpenConns(10)
	database.SetMaxIdleConns(5)
	database.SetConnMaxLifetime(30 * time.Minute)

	return database, nil
}

// Migrate applies the embedded initialization SQL.
// For production, replace with a proper migration tool and versioned files.
func Migrate(ctx context.Context, database *sql.DB) error {
	_, err := database.ExecContext(ctx, migrationSQL)
	return err
}