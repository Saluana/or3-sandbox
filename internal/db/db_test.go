package db

import (
	"context"
	"testing"
)

func TestOpenCreatesMillicoreColumns(t *testing.T) {
	database, err := Open(context.Background(), t.TempDir()+"/sandbox.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer database.Close()

	for _, tc := range []struct {
		table  string
		column string
	}{
		{table: "sandboxes", column: "cpu_limit_millis"},
		{table: "quotas", column: "max_cpu_millis"},
	} {
		rows, err := database.QueryContext(context.Background(), "PRAGMA table_info("+tc.table+")")
		if err != nil {
			t.Fatalf("pragma %s: %v", tc.table, err)
		}
		found := false
		for rows.Next() {
			var cid int
			var name, dataType string
			var notNull int
			var defaultValue any
			var pk int
			if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
				rows.Close()
				t.Fatalf("scan pragma row: %v", err)
			}
			if name == tc.column {
				found = true
				break
			}
		}
		rows.Close()
		if !found {
			t.Fatalf("missing %s column on %s", tc.column, tc.table)
		}
	}
}
