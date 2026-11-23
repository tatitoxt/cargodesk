package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationLockID is an arbitrary constant for pg_advisory_xact_lock so two
// processes never apply migrations concurrently.
const migrationLockID = 7_420_250_301

type Migration struct {
	Version string
	SQL     string
}

func Migrations() ([]Migration, error) {
	entries, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(entries)
	out := make([]Migration, 0, len(entries))
	for _, e := range entries {
		b, err := migrationFS.ReadFile(e)
		if err != nil {
			return nil, err
		}
		v := strings.TrimSuffix(strings.TrimPrefix(e, "migrations/"), ".sql")
		out = append(out, Migration{Version: v, SQL: string(b)})
	}
	return out, nil
}

// Migrate applies pending migrations, each in its own transaction, and
// returns the versions it applied. Running it again is a no-op.
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	ms, err := Migrations()
	if err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}
	var applied []string
	for _, m := range ms {
		ok, err := applyOne(ctx, pool, m)
		if err != nil {
			return applied, fmt.Errorf("migration %s: %w", m.Version, err)
		}
		if ok {
			applied = append(applied, m.Version)
		}
	}
	return applied, nil
}

func applyOne(ctx context.Context, pool *pgxpool.Pool, m Migration) (bool, error) {
	applied := false
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLockID); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, m.Version).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		if _, err := tx.Exec(ctx, m.SQL); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, m.Version); err != nil {
			return err
		}
		applied = true
		return nil
	})
	return applied, err
}

// ResetSchema drops every CargoDesk table. Used only by `seed --reset` and
// integration tests.
func ResetSchema(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`)
	return err
}
