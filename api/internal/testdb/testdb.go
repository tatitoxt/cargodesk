// Package testdb prepares a real PostgreSQL database for integration tests.
// Set CARGODESK_TEST_DATABASE_URL; its schema is dropped and re-created.
// Packages that use it must run serially (go test -p 1).
package testdb

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tatitoxt/cargodesk/api/internal/seed"
	"github.com/tatitoxt/cargodesk/api/internal/store"
)

const EnvURL = "CARGODESK_TEST_DATABASE_URL"

// Now is the fixed clock used by seeded integration tests.
var Now = time.Date(2026, 3, 10, 15, 0, 0, 0, time.UTC)

// Fresh returns a pool on an empty, migrated and seeded database. Without the
// env var the test is skipped, unless CI is set, in which case it fails.
func Fresh(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(EnvURL)
	if url == "" {
		if os.Getenv("CI") != "" {
			t.Fatalf("%s must be set in CI", EnvURL)
		}
		t.Skipf("%s not set; skipping integration test", EnvURL)
	}
	ctx := context.Background()
	pool, err := store.Open(ctx, url)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := store.ResetSchema(ctx, pool); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := store.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := seed.Run(ctx, pool, Now); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return pool
}
