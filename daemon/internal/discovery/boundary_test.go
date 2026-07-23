package discovery

import (
	"os/exec"
	"strings"
	"testing"
)

// FR-DSC-001 made structural — suggest, never authorize — proven the AT-16 way, over
// the compiler's import graph:
//
//  1. internal/discovery has NO import path to policy, approval, funding, purchasing,
//     brain, worker, or ownerapi. A discovery result is a type that cannot even NAME a
//     PaymentIntent, a Grant, or a Lifecycle — selection literally cannot express
//     authorization.
//  2. The worker side holds symmetrically: worker defines its own Finder seam and never
//     imports discovery (AT-16 stays maximal — worker reaches nothing but envelope).
//     The two meet only at the wiring layer, like every spoke in this system.
//  3. A Match becomes money only by being copied into a PurchaseRequest and submitted
//     through the same Purchase seam as every other intent — job-stamped by Brain,
//     gated by the deterministic policy engine — regardless of how it was found.

const modulePrefix = "github.com/gnanam1990/snapfall/daemon/"

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

func TestBoundary_DiscoveryCannotNameMoneyOrWorker(t *testing.T) {
	deps := goListDeps(t, modulePrefix+"internal/discovery")
	for _, forbidden := range []string{
		"internal/policy", "internal/approval", "internal/funding",
		"internal/purchasing", "internal/brain", "internal/worker", "internal/ownerapi",
	} {
		if _, reachable := deps[modulePrefix+forbidden]; reachable {
			t.Fatalf("internal/discovery can reach %s — selection could express authorization", forbidden)
		}
	}
}

// Checker sanity: the dep set must at least contain the package itself; an empty or
// broken listing would make the test above prove nothing.
func TestBoundary_CheckerSeesDiscoveryItself(t *testing.T) {
	deps := goListDeps(t, modulePrefix+"internal/discovery")
	if _, ok := deps[modulePrefix+"internal/discovery"]; !ok {
		t.Fatal("checker broken: go list -deps does not include the package itself")
	}
}
