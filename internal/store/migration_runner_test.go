
package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/asdfgh129/gobitmule/internal/core"
)

// ADR-0027: the v2 migration runner records applied migrations in
// schema_migrations so non-idempotent ALTER TABLE statements run exactly
// once; re-opening the same database must not re-run them (the duplicate
// column failure was caught by the rollback drill).
func TestMigrationRunnerRecordsAppliedMigrations(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	for i := 1; i <= 3; i++ {
		st, err := Open(dbPath, core.NewEventBus(8))
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		v, err := st.SchemaVersion(context.Background())
		if err != nil || v != CurrentSchemaVersion() {
			t.Fatalf("open #%d: schema_version=%d err=%v, want %d", i, v, err, CurrentSchemaVersion())
		}
		rows, err := st.DB().Query(`SELECT name FROM schema_migrations ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				t.Fatal(err)
			}
			names = append(names, n)
		}
		rows.Close()
		if len(names) != CurrentSchemaVersion() {
			t.Fatalf("open #%d: recorded %d migrations, want %d", i, len(names), CurrentSchemaVersion())
		}
		st.Close()
	}
}
