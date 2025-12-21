package internal

import (
	"context"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// advisoryLockID is a fixed constant used for postgres advisory locks
// to prevent concurrent migrations from running.
const advisoryLockID = 7294815603

// acquireAdvisoryLock attempts to acquire a postgres advisory lock.
func acquireAdvisoryLock(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockID)
	if err != nil {
		return fmt.Errorf("failed to acquire advisory lock: %w", err)
	}
	return nil
}

// releaseAdvisoryLock releases the postgres advisory lock.
func releaseAdvisoryLock(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, "SELECT pg_advisory_unlock($1)", advisoryLockID)
	if err != nil {
		return fmt.Errorf("failed to release advisory lock: %w", err)
	}
	return nil
}

// createMigrator creates a new golang-migrate instance using the embedded SQL files.
func createMigrator(pool *pgxpool.Pool) (*migrate.Migrate, error) {
	sourceDriver, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("failed to create migration source driver: %w", err)
	}

	dbURL := pool.Config().ConnString()
	m, err := migrate.NewWithSourceInstance("iofs", sourceDriver, "pgx5://"+dbURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create migrator: %w", err)
	}

	return m, nil
}

// MigrateUp runs all pending migrations (both River and application migrations).
// It acquires a postgres advisory lock to prevent concurrent migrations.
func MigrateUp(ctx context.Context, pool *pgxpool.Pool) error {
	if err := acquireAdvisoryLock(ctx, pool); err != nil {
		return err
	}
	defer releaseAdvisoryLock(ctx, pool)

	// Run River migrations first
	riverMigrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("failed to create river migrator: %w", err)
	}

	_, err = riverMigrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return fmt.Errorf("failed to run river migrations up: %w", err)
	}

	// Run application migrations
	m, err := createMigrator(pool)
	if err != nil {
		return err
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("failed to run application migrations up: %w", err)
	}

	return nil
}

// MigrateDown rolls back all migrations (both application and River migrations).
// It acquires a postgres advisory lock to prevent concurrent migrations.
func MigrateDown(ctx context.Context, pool *pgxpool.Pool) error {
	if err := acquireAdvisoryLock(ctx, pool); err != nil {
		return err
	}
	defer releaseAdvisoryLock(ctx, pool)

	// Run application migrations down first
	m, err := createMigrator(pool)
	if err != nil {
		return err
	}
	defer m.Close()

	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("failed to run application migrations down: %w", err)
	}

	// Run River migrations down
	riverMigrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("failed to create river migrator: %w", err)
	}

	_, err = riverMigrator.Migrate(ctx, rivermigrate.DirectionDown, nil)
	if err != nil {
		return fmt.Errorf("failed to run river migrations down: %w", err)
	}

	return nil
}
