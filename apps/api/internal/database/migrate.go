package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"fmt"
	"sort"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrate upgrades one configured schema. A connection-scoped advisory lock keeps
// overlapping container restarts from running the same DDL concurrently.
func Migrate(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext(current_schema()), 20261003)`); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtext(current_schema()), 20261003)`)
	if _, err = conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS mailat_schema_migrations (name text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		name := entry.Name()
		body, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(body))
		var previous string
		err = conn.QueryRowContext(ctx, `SELECT checksum FROM mailat_schema_migrations WHERE name=$1`, name).Scan(&previous)
		if err == nil {
			if previous != checksum {
				return fmt.Errorf("migration %s changed after application", name)
			}
			continue
		}
		if err != sql.ErrNoRows {
			return err
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		skipBaseline := false
		if name == "001_initial.sql" {
			// Pre-migration installs already have the base schema. Additive upgrades
			// below reconcile new fields without recreating or dropping user tables.
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='organizations')`).Scan(&skipBaseline)
		}
		if err == nil && !skipBaseline {
			_, err = tx.ExecContext(ctx, string(body))
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO mailat_schema_migrations(name,checksum) VALUES($1,$2)`, name, checksum)
		}
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if err = tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}
