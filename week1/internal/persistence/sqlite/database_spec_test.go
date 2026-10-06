package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	gatewaysqlite "github.com/homework-G20200607010067/week1/internal/persistence/sqlite"
)

func TestOpenCreatesAndMigratesAnEmptyDatabase_BitsSpecUT(t *testing.T) {
	database, err := gatewaysqlite.Open(context.Background(), filepath.Join(t.TempDir(), "nested", "gateway.db"), time.Second)
	if err != nil {
		t.Fatalf("open empty database: %v", err)
	}
	defer database.Close()
	for _, table := range []string{"prompt_versions", "usage_events"} {
		var name string
		if err := database.QueryRowContext(context.Background(),
			"SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", table,
		).Scan(&name); err != nil {
			t.Fatalf("expected migrated table %q: %v", table, err)
		}
	}
}
