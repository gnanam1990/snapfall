package main

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenWALStoreCreatesDatabaseParent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state", "chain", "snapfall.db")
	st, mode, err := openWALStore(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if mode != "wal" {
		t.Fatalf("journal mode = %q, want wal", mode)
	}
}
