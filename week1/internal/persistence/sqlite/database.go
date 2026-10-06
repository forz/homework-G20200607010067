package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Open creates the database directory, opens SQLite with bounded concurrency,
// applies idempotent schema migrations, and returns only after a successful ping.
func Open(ctx context.Context, path string, busyTimeout time.Duration) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	milliseconds := max(busyTimeout.Milliseconds(), 1)
	dsn := fmt.Sprintf(
		"file:%s?_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)",
		strings.ReplaceAll(filepath.ToSlash(path), "?", "%3F"), milliseconds,
	)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS prompt_versions (
            prompt_id TEXT NOT NULL,
            version INTEGER NOT NULL CHECK(version >= 1),
            name TEXT NOT NULL,
            description TEXT NOT NULL DEFAULT '',
            role TEXT NOT NULL CHECK(role IN ('system', 'developer')),
            content TEXT NOT NULL,
            is_active INTEGER NOT NULL DEFAULT 0 CHECK(is_active IN (0, 1)),
            created_at TEXT NOT NULL,
            PRIMARY KEY (prompt_id, version)
        )`,
		`CREATE INDEX IF NOT EXISTS idx_prompt_versions_created
            ON prompt_versions(prompt_id, created_at DESC)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_prompt_versions_one_active
            ON prompt_versions(prompt_id) WHERE is_active = 1`,
		`CREATE TABLE IF NOT EXISTS usage_events (
            request_id TEXT PRIMARY KEY,
            created_at TEXT NOT NULL,
            model_alias TEXT NOT NULL,
            protocol TEXT NOT NULL,
            upstream_model TEXT NOT NULL DEFAULT '',
            stream INTEGER NOT NULL CHECK(stream IN (0, 1)),
            status TEXT NOT NULL CHECK(status IN ('success', 'error', 'cancelled')),
            http_status INTEGER NOT NULL,
            input_tokens INTEGER NOT NULL DEFAULT 0,
            output_tokens INTEGER NOT NULL DEFAULT 0,
            total_tokens INTEGER NOT NULL DEFAULT 0,
            cached_tokens INTEGER NOT NULL DEFAULT 0,
            cache_write_tokens INTEGER NOT NULL DEFAULT 0,
            reasoning_tokens INTEGER NOT NULL DEFAULT 0,
            usage_incomplete INTEGER NOT NULL DEFAULT 0 CHECK(usage_incomplete IN (0, 1)),
            latency_ms REAL NOT NULL DEFAULT 0,
            first_token_ms REAL,
            transport_retries INTEGER NOT NULL DEFAULT 0,
            structured_corrections INTEGER NOT NULL DEFAULT 0,
            prompt_id TEXT NOT NULL DEFAULT '',
            prompt_version INTEGER,
            error_type TEXT NOT NULL DEFAULT '',
            error_code TEXT NOT NULL DEFAULT '',
            provider_metadata_json TEXT NOT NULL DEFAULT ''
        )`,
		`CREATE INDEX IF NOT EXISTS idx_usage_created
            ON usage_events(created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_usage_model_created
            ON usage_events(model_alias, created_at DESC)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate sqlite schema: %w", err)
		}
	}
	return nil
}
