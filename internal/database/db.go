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

	// Neon instances can take up to 45s when waking up from scale-to-zero (cold start)
	pingCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		return nil, fmt.Errorf("database unreachable: %w", err)
	}

	slog.Info("connected to postgres successfully")

	// Automatically run migrations to adjust schema constraints
	if err := AutoMigrate(ctx, pool); err != nil {
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return pool, nil
}

// AutoMigrate fixes column constraints on orders and order_items.
func AutoMigrate(ctx context.Context, pool *pgxpool.Pool) error {
	schema := `
	CREATE TABLE IF NOT EXISTS orders (
		id SERIAL PRIMARY KEY,
		total_cents INT NOT NULL,
		status VARCHAR(50) NOT NULL DEFAULT 'completed',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	DO $$
	BEGIN
		-- Relax customer_email if it exists in orders
		IF EXISTS (
			SELECT 1 FROM information_schema.columns 
			WHERE table_name = 'orders' AND column_name = 'customer_email'
		) THEN
			ALTER TABLE orders ALTER COLUMN customer_email DROP NOT NULL;
			ALTER TABLE orders ALTER COLUMN customer_email SET DEFAULT '';
		END IF;

		-- Ensure price_cents column exists on order_items
		IF NOT EXISTS (
			SELECT 1 FROM information_schema.columns 
			WHERE table_name = 'order_items' AND column_name = 'price_cents'
		) THEN
			ALTER TABLE order_items ADD COLUMN price_cents INT NOT NULL DEFAULT 0;
		END IF;

		-- Relax unit_price_cents if it exists on order_items
		IF EXISTS (
			SELECT 1 FROM information_schema.columns 
			WHERE table_name = 'order_items' AND column_name = 'unit_price_cents'
		) THEN
			ALTER TABLE order_items ALTER COLUMN unit_price_cents DROP NOT NULL;
			ALTER TABLE order_items ALTER COLUMN unit_price_cents SET DEFAULT 0;
		END IF;
	END $$;

	CREATE TABLE IF NOT EXISTS order_items (
		id SERIAL PRIMARY KEY,
		order_id INT NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
		product_id INT NOT NULL REFERENCES products(id),
		quantity INT NOT NULL,
		price_cents INT NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);
	`

	if _, err := pool.Exec(ctx, schema); err != nil {
		return fmt.Errorf("migration exec failed: %w", err)
	}

	slog.Info("database migrations verified successfully")
	return nil
}
