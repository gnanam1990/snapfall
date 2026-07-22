package brain

import (
	"os/exec"
	"strings"
	"testing"
)

// AT-16 — THE LAW is structural: Worker→Funding has no code path AT ALL.
//
// This is not a runtime check that a call was denied. It interrogates the compiler's
// own import graph: if any file anywhere under internal/worker imports internal/funding
// (directly or transitively), `go list -deps` will report it and this test fails.
// The channel isn't blocked. It does not exist.
func TestAT16_WorkerHasNoPathToFunding(t *testing.T) {
	deps := goListDeps(t, "github.com/gnanam1990/snapfall/daemon/internal/worker")

	const funding = "github.com/gnanam1990/snapfall/daemon/internal/funding"
	if _, reachable := deps[funding]; reachable {
		t.Fatal("AT-16 VIOLATED: internal/worker can reach internal/funding — " +
			"a Worker with a channel to money is one prompt-injection from treasury loss (PRD §3)")
	}
}

// The same law, wider: a Worker may know the envelope vocabulary and the standard
// library — nothing else in this codebase. Not the store, not the brain, not the
// owner surface. A worker cannot call what it cannot name.
func TestAT16_WorkerImportsOnlyEnvelope(t *testing.T) {
	deps := goListDeps(t, "github.com/gnanam1990/snapfall/daemon/internal/worker")

	const modulePrefix = "github.com/gnanam1990/snapfall/daemon/"
	allowed := map[string]bool{
		modulePrefix + "internal/worker":   true, // itself
		modulePrefix + "internal/envelope": true, // the shared vocabulary
	}

	for dep := range deps {
		if strings.HasPrefix(dep, modulePrefix) && !allowed[dep] {
			t.Errorf("internal/worker reaches %s — workers may depend on the envelope package only", dep)
		}
	}
}

// Sanity check on the checker: brain DOES reach funding (it is the hub and holds the
// only pointer). If this fails, goListDeps is broken and the two tests above prove nothing.
func TestAT16_CheckerSeesBrainReachingFunding(t *testing.T) {
	deps := goListDeps(t, "github.com/gnanam1990/snapfall/daemon/internal/brain")

	const funding = "github.com/gnanam1990/snapfall/daemon/internal/funding"
	if _, reachable := deps[funding]; !reachable {
		t.Fatal("checker broken: brain must reach funding (it is the hub), but go list does not show it")
	}
}

// goListDeps returns the full transitive dependency set of pkg as a set of import paths.
func goListDeps(t *testing.T, pkg string) map[string]struct{} {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", pkg).Output()
	if err != nil {
		t.Fatalf("go list -deps %s: %v", pkg, err)
	}
	deps := make(map[string]struct{})
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		deps[strings.TrimSpace(line)] = struct{}{}
	}
	return deps
}
