// Command reset-demo returns the machine to a clean pre-demo state between recording takes
// (V12). The release gate asks for reset -> spine -> reset -> spine with no manual fixes, so
// this has to be exhaustive about local state and honest about chain state.
//
// What it clears: the daemon's SQLite store, Brain's per-job memory files, the sidecar's
// durable payment records, and the recorded run id.
//
// What it CANNOT clear: anything on Arc. The chain has no delete, and JobVault.createJob
// reverts with JobExists on a reused id — which is exactly why seed-demo mints a fresh job id
// per run instead of trying to reuse job_104. Pool liquidity also persists, and that is
// wanted: seeding is idempotent and a repeat take needs no new deposit.
//
//	reset-demo --dry-run     list what would be removed
//	reset-demo               remove it
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// target is one piece of local demo state.
type target struct {
	path string
	what string
	dir  bool
}

// Paths default to running from the daemon module directory, like every other cmd here;
// ./scripts/reset_demo cds there for you.
func main() {
	dbPath := flag.String("db", envOr("SNAPFALL_DB_PATH", "snapfall.db"), "daemon SQLite store")
	memoryDir := flag.String("memory", envOr("SNAPFALL_MEMORY_DIR", "memory"), "Brain per-job memory directory")
	sidecarStore := flag.String("sidecar-store", envOr("SIDECAR_STORE_PATH", "../sidecar/.data"), "sidecar durable payment records")
	statePath := flag.String("state", "../.demo", "recorded run state directory")
	dryRun := flag.Bool("dry-run", false, "list what would be removed and exit")
	flag.Parse()

	targets := []target{
		{*dbPath, "daemon event store", false},
		{*dbPath + "-wal", "SQLite write-ahead log", false},
		{*dbPath + "-shm", "SQLite shared memory", false},
		{*memoryDir, "Brain per-job memory", true},
		{*sidecarStore, "sidecar payment records", true},
		{*statePath, "recorded run state", true},
	}

	if err := run(targets, *dryRun); err != nil {
		fmt.Fprintln(os.Stderr, "reset-demo:", err)
		os.Exit(1)
	}
}

func run(targets []target, dryRun bool) error {
	fmt.Println("Snapfall demo reset · local state only (Arc state is immutable by design)")

	var removed, absent int
	for _, t := range targets {
		if err := guard(t.path); err != nil {
			return err
		}
		_, err := os.Stat(t.path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			fmt.Printf("  %-26s absent   %s\n", t.what, t.path)
			absent++
			continue
		case err != nil:
			return fmt.Errorf("stat %s: %w", t.path, err)
		}

		if dryRun {
			fmt.Printf("  %-26s WOULD REMOVE %s\n", t.what, t.path)
			removed++
			continue
		}
		if err := os.RemoveAll(t.path); err != nil {
			return fmt.Errorf("remove %s: %w", t.path, err)
		}
		fmt.Printf("  %-26s removed  %s\n", t.what, t.path)
		removed++
	}

	verb := "removed"
	if dryRun {
		verb = "would remove"
	}
	fmt.Printf("\n%s %d item(s); %d already absent.\n", verb, removed, absent)
	if !dryRun {
		fmt.Println("Next: ./scripts/seed_demo — it mints a FRESH job id, so this take cannot collide with the last one.")
	}
	return nil
}

// guard refuses paths that would make a mistake catastrophic. A reset script is exactly the
// kind of tool that gets pointed at the wrong directory at 2am before a recording.
func guard(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("refusing to act on an empty path")
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || clean == string(filepath.Separator) {
		return fmt.Errorf("refusing to remove %q", path)
	}
	if abs, err := filepath.Abs(clean); err == nil {
		if vol := filepath.VolumeName(abs); abs == vol+string(filepath.Separator) || abs == string(filepath.Separator) {
			return fmt.Errorf("refusing to remove a filesystem root (%s)", abs)
		}
		if home, err := os.UserHomeDir(); err == nil && abs == filepath.Clean(home) {
			return fmt.Errorf("refusing to remove the home directory (%s)", abs)
		}
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
