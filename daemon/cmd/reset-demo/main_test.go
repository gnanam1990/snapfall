package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// projectRoot builds a directory that looks like a Snapfall checkout.
func projectRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	// t.TempDir can hand back a symlinked path (/var -> /private/var on macOS); resolve it so
	// comparisons in the tests match what resolveRoot returns.
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	for _, d := range []string{"daemon", "sidecar"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// The finding this test exists for: reset-demo used to hand any supplied path to RemoveAll
// behind a blocklist that only excluded ".", "..", the filesystem root and $HOME. A typo or a
// stale SIDECAR_STORE_PATH could therefore recursively delete an unrelated directory.
func TestConfineRefusesAnythingOutsideTheProject(t *testing.T) {
	root := projectRoot(t)
	outside := t.TempDir()

	for _, path := range []string{
		outside,
		filepath.Join(outside, "important"),
		filepath.Join(root, "..", "sibling"),
		filepath.Join(root, "daemon", "..", "..", "escape"),
		"",
		"   ",
	} {
		if got, err := confine(root, path); err == nil {
			t.Fatalf("confine(%q) allowed %q; it is outside %s", path, got, root)
		}
	}
}

// The tool removes state UNDER the project, never the project itself.
func TestConfineRefusesTheRootItself(t *testing.T) {
	root := projectRoot(t)
	for _, path := range []string{root, root + string(filepath.Separator), filepath.Join(root, "daemon", "..")} {
		if _, err := confine(root, path); err == nil {
			t.Fatalf("confine allowed the project root itself via %q", path)
		} else if !strings.Contains(err.Error(), "project root itself") {
			t.Fatalf("unexpected error for %q: %v", path, err)
		}
	}
}

func TestConfineAllowsRealTargets(t *testing.T) {
	root := projectRoot(t)
	for _, rel := range []string{
		"daemon/snapfall.db",
		"daemon/snapfall.db-wal",
		"daemon/memory",
		"sidecar/.data",
		".demo",
	} {
		abs, err := confine(root, filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("confine rejected the legitimate target %s: %v", rel, err)
		}
		if !strings.HasPrefix(abs, root+string(filepath.Separator)) {
			t.Fatalf("confine(%s) returned %s, which is not under %s", rel, abs, root)
		}
	}
}

// A parent directory that is a symlink out of the tree defeats a purely lexical containment
// check, so containment is verified against resolved paths.
func TestConfineResolvesSymlinkedParents(t *testing.T) {
	root := projectRoot(t)
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable in this environment (%v)", err)
	}
	if got, err := confine(root, filepath.Join(link, "victim")); err == nil {
		t.Fatalf("confine followed a symlink out of the project and allowed %q", got)
	}
}

func TestResolveRootRejectsBroadRoots(t *testing.T) {
	// A directory with no daemon/ or sidecar/ inside is not a Snapfall checkout, so it cannot
	// be used to widen what confine() permits.
	if _, err := resolveRoot(t.TempDir()); err == nil {
		t.Fatal("a directory that is not a Snapfall checkout must be refused as --root")
	}
	if _, err := resolveRoot(""); err == nil {
		t.Fatal("an empty --root must be refused")
	}
	if home, err := os.UserHomeDir(); err == nil {
		if _, err := resolveRoot(home); err == nil {
			t.Fatal("the home directory must be refused as --root")
		}
	}
	root := projectRoot(t)
	got, err := resolveRoot(root)
	if err != nil {
		t.Fatalf("a real checkout must be accepted: %v", err)
	}
	if got != root {
		t.Fatalf("resolveRoot(%s) = %s", root, got)
	}
	// A file is not a root.
	f := filepath.Join(root, "daemon", "snapfall.db")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveRoot(f); err == nil {
		t.Fatal("a file must be refused as --root")
	}
}

// Files must be removed with os.Remove and directories with os.RemoveAll, and a path whose
// real kind contradicts its declared kind must abort rather than be deleted on faith.
func TestRunRemovesTheRightKinds(t *testing.T) {
	root := projectRoot(t)
	db := filepath.Join(root, "daemon", "snapfall.db")
	memory := filepath.Join(root, "daemon", "memory")
	if err := os.WriteFile(db, []byte("sqlite"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(memory, "job_104"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memory, "job_104", "notes.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	targets := []target{{db, "daemon event store", false}, {memory, "Brain per-job memory", true}}

	// A dry run must delete nothing at all.
	if err := run(root, targets, true); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if _, err := os.Stat(db); err != nil {
		t.Fatal("dry run removed the database")
	}
	if _, err := os.Stat(memory); err != nil {
		t.Fatal("dry run removed the memory directory")
	}

	if err := run(root, targets, false); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Fatal("the database was not removed")
	}
	if _, err := os.Stat(memory); !os.IsNotExist(err) {
		t.Fatal("the memory directory was not removed")
	}
	// The project itself survives.
	if _, err := os.Stat(filepath.Join(root, "sidecar")); err != nil {
		t.Fatal("the reset removed more than its targets")
	}

	// Absent targets are reported, not an error: reset must be runnable twice.
	if err := run(root, targets, false); err != nil {
		t.Fatalf("a second reset must succeed with everything already absent: %v", err)
	}
}

func TestRunRefusesAKindMismatch(t *testing.T) {
	root := projectRoot(t)
	// Declared a file, actually a directory. RemoveAll on the strength of the name alone is
	// exactly the mistake this guards.
	surprise := filepath.Join(root, "daemon", "snapfall.db")
	if err := os.MkdirAll(filepath.Join(surprise, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := run(root, []target{{surprise, "daemon event store", false}}, false)
	if err == nil {
		t.Fatal("a directory where a file was expected must abort the reset")
	}
	if !strings.Contains(err.Error(), "refusing to remove") {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, statErr := os.Stat(surprise); statErr != nil {
		t.Fatal("the mismatched path was removed anyway")
	}
}

func TestRunRefusesATargetOutsideTheRoot(t *testing.T) {
	root := projectRoot(t)
	outside := t.TempDir()
	victim := filepath.Join(outside, "unrelated-project")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := run(root, []target{{victim, "sidecar payment records", true}}, false); err == nil {
		t.Fatal("a target outside the project root must abort the reset")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatal("an out-of-tree directory was deleted")
	}
}
