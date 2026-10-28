package database

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ConnectPool creates a resilient connection pool to PostgreSQL configured for PgBouncer/Neon.
func ConnectPool(ctx context.Context, connStr string) (*pgxpool.Pool, error) {
	config, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return nil, fmt.Errorf("invalid db config: %w", err)
	}

	// Disable client-side prepared statements for compatibility with Neon / PgBouncer pooler
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	config.MaxConns = 10
	config.MinConns = 1
	config.MaxConnLifetime = 1 * time.Hour
	config.MaxConnIdleTime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("failed to create pool: %w", err)
	}

	// Neon instances need up to 15s when waking up from scale-to-zero (cold start)
	pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		return nil, fmt.Errorf("database unreachable: %w", err)
	}

	slog.Info("connected to postgres successfully")
	return pool, nil
}
