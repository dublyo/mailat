// Package testutil provides isolated integration-test infrastructure.
package testutil

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/database"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

// Database creates a disposable schema, never a table in the configured default
// schema. It requires explicit opt-in so ordinary go test cannot write remotely.
func EmptyDatabase(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("MAILAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("MAILAT_TEST_DATABASE_URL is not set; hosted integration test skipped")
	}
	base, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal("open test database:", err)
	}
	schema := "mailat_test_" + uuid.New().String()[:8]
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err = base.ExecContext(ctx, `CREATE SCHEMA `+pq.QuoteIdentifier(schema)); err != nil {
		base.Close()
		t.Fatal("create isolated schema:", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := base.ExecContext(ctx, `DROP SCHEMA `+pq.QuoteIdentifier(schema)+` CASCADE`); err != nil {
			t.Error("remove test schema:", err)
		}
		base.Close()
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("parse test database URL")
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := sql.Open("postgres", u.String())
	if err != nil {
		t.Fatal("open isolated connection:", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func Database(t *testing.T) *sql.DB {
	t.Helper()
	db := EmptyDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := database.Migrate(ctx, db); err != nil {
		t.Fatal("migrate isolated schema:", err)
	}
	return db
}
