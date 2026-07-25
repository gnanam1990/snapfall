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
// Every path is confined to the project tree. This is a recursive-delete tool that gets pointed
// at things at 2am before a recording, and a blocklist of "obviously bad" paths is the wrong
// shape of defence: anything outside the project is refused, so a typo or a stale environment
// override fails loudly instead of eating an unrelated directory.
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

// target is one piece of local demo state. kind is asserted against the filesystem before
// anything is removed, so a path that is unexpectedly a directory is never handed to
// RemoveAll on the strength of its name alone.
type target struct {
	path string
	what string
	dir  bool
}

// Paths default to running from the daemon module directory, like every other cmd here;
// ./scripts/reset_demo cds there for you.
func main() {
	root := flag.String("root", "..", "project root; every target must resolve inside it")
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

	if err := run(*root, targets, *dryRun); err != nil {
		fmt.Fprintln(os.Stderr, "reset-demo:", err)
		os.Exit(1)
	}
}

// resolved is a target that has passed every check and is safe to remove.
type resolved struct {
	target
	abs    string
	link   bool // a symlink, removed as a link and never traversed
	absent bool
}

func run(rootFlag string, targets []target, dryRun bool) error {
	root, err := resolveRoot(rootFlag)
	if err != nil {
		return err
	}
	fmt.Println("Snapfall demo reset · local state only (Arc state is immutable by design)")
	fmt.Printf("  project root  %s\n\n", root)

	// PASS 1: resolve and validate everything, removing nothing.
	//
	// Validating and deleting in one loop means a target that fails halfway through leaves the
	// earlier ones already destroyed and the reset half-applied, which for a tool whose whole
	// job is getting to a known-clean state is the worst outcome: not clean, not the previous
	// state either. Nothing is touched until every target is known-good (review: PR #41).
	plan := make([]resolved, 0, len(targets))
	for _, t := range targets {
		abs, err := confine(root, t.path)
		if err != nil {
			return err
		}
		// Lstat, not Stat: a symlink is inspected as a link so it is never traversed.
		info, err := os.Lstat(abs)
		switch {
		case errors.Is(err, os.ErrNotExist):
			plan = append(plan, resolved{target: t, abs: abs, absent: true})
			continue
		case err != nil:
			return fmt.Errorf("stat %s: %w", t.path, err)
		}

		link := info.Mode()&os.ModeSymlink != 0
		if !link && info.IsDir() != t.dir {
			kind, want := "a file", "a directory"
			if info.IsDir() {
				kind, want = "a directory", "a file"
			}
			return fmt.Errorf("refusing to remove %s: %s expects %s but found %s", abs, t.what, want, kind)
		}
		plan = append(plan, resolved{target: t, abs: abs, link: link})
	}

	// PASS 2: act. Every path here is confined, exists, and matches its declared kind.
	var removed, absent int
	for _, r := range plan {
		if r.absent {
			fmt.Printf("  %-26s absent   %s\n", r.what, r.path)
			absent++
			continue
		}
		if dryRun {
			fmt.Printf("  %-26s WOULD REMOVE %s\n", r.what, r.abs)
			removed++
			continue
		}
		// Only a genuine directory gets the recursive form.
		remove := os.Remove
		if r.dir && !r.link {
			remove = os.RemoveAll
		}
		if err := remove(r.abs); err != nil {
			return fmt.Errorf("remove %s: %w", r.abs, err)
		}
		fmt.Printf("  %-26s removed  %s\n", r.what, r.abs)
		removed++
	}

	verb := "removed"
	if dryRun {
		verb = "would remove"
	}
	fmt.Printf("\n%s %d item(s); %d already absent.\n", verb, removed, absent)
	if !dryRun {
		fmt.Println("Next: ./scripts/seed_demo, which mints a FRESH job id, so this take cannot collide with the last one.")
	}
	return nil
}

// resolveRoot turns the --root flag into a real absolute directory and refuses roots broad
// enough to make a mistake catastrophic. It must look like the Snapfall tree, so an
// accidentally inherited root cannot widen what confine() will allow.
func resolveRoot(rootFlag string) (string, error) {
	if strings.TrimSpace(rootFlag) == "" {
		return "", errors.New("--root must be set")
	}
	abs, err := filepath.Abs(rootFlag)
	if err != nil {
		return "", fmt.Errorf("resolve --root %q: %w", rootFlag, err)
	}
	// Resolve symlinks so containment is checked against real paths on both sides.
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("--root %s: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("--root %s is not a directory", abs)
	}
	if vol := filepath.VolumeName(abs); abs == vol+string(filepath.Separator) || abs == string(filepath.Separator) {
		return "", fmt.Errorf("refusing to use a filesystem root as --root (%s)", abs)
	}
	if home, err := os.UserHomeDir(); err == nil {
		if h, err := filepath.EvalSymlinks(home); err == nil {
			home = h
		}
		if abs == filepath.Clean(home) {
			return "", fmt.Errorf("refusing to use the home directory as --root (%s)", abs)
		}
	}
	// A Snapfall checkout has these. Requiring them means a root pointed at a general-purpose
	// directory (a projects folder, /tmp, C:\) is rejected before any target is considered.
	// Both markers must be DIRECTORIES. os.Stat succeeds for a regular file named "daemon", so
	// a presence-only check let an unrelated directory pass the checkout guard and thereby
	// authorize deletion of whatever its flags pointed at (review: PR #41).
	for _, marker := range []string{"daemon", "sidecar"} {
		info, err := os.Stat(filepath.Join(abs, marker))
		if err != nil || !info.IsDir() {
			return "", fmt.Errorf("--root %s does not look like a Snapfall checkout (no %s/ directory inside); "+
				"pass --root pointing at the repository", abs, marker)
		}
	}
	return abs, nil
}

// confine resolves a target and returns its absolute path only if it sits strictly inside root.
// The root itself is refused: this tool removes state under the project, never the project.
func confine(root, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("refusing to act on an empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", path, err)
	}
	// The target may not exist yet, so resolve symlinks on the deepest existing ancestor and
	// re-attach the remainder. This catches a parent directory that is a link out of the tree,
	// which a purely lexical check would miss.
	dir, base := filepath.Split(abs)
	if real, err := filepath.EvalSymlinks(filepath.Clean(dir)); err == nil {
		abs = filepath.Join(real, base)
	}

	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", fmt.Errorf("refusing to remove %s: cannot relate it to %s", abs, root)
	}
	if rel == "." {
		return "", fmt.Errorf("refusing to remove the project root itself (%s)", abs)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("refusing to remove %s: it is outside the project root %s", abs, root)
	}
	return abs, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
