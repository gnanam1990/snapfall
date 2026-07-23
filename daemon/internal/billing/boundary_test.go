package billing

import (
	"os/exec"
	"strings"
	"testing"
)

// Pin 3, proven the AT-16 way — over the compiler's import graph, not a runtime check.
//
// Billing sends nothing (invoices are durable events surfaces render), so there is no
// egress to gate and no Grant-style credential here — deliberately. The boundary that
// remains is placement, and these tests are its law:
//
//  1. internal/worker has NO import path to internal/billing: a worker cannot even NAME
//     the type that invoices, so "a worker invoices a job" is unexpressible — the
//     invoice-side analogue of the cross-job purchase impossibility.
//  2. internal/billing has NO import path to approval, policy, funding, or worker: it
//     cannot move money, decide an approval, or reach a worker. It is a read-side
//     formatter over the store, and the compiler holds it there.
//
// The remaining caller-side guarantee — only Brain holds the *Agent pointer, from one
// invocation site — is pinned at the wiring step (Step 2), same technique as the
// dispatch chokepoint.

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

func TestBoundary_WorkerCannotReachBilling(t *testing.T) {
	deps := goListDeps(t, modulePrefix+"internal/worker")
	if _, reachable := deps[modulePrefix+"internal/billing"]; reachable {
		t.Fatal("internal/worker can reach internal/billing — a worker could name the invoicing type")
	}
}

func TestBoundary_BillingIsReadSideOnly(t *testing.T) {
	deps := goListDeps(t, modulePrefix+"internal/billing")
	for _, forbidden := range []string{"internal/approval", "internal/policy", "internal/funding", "internal/worker"} {
		if _, reachable := deps[modulePrefix+forbidden]; reachable {
			t.Fatalf("internal/billing can reach %s — Billing must stay a read-side formatter", forbidden)
		}
	}
}

// Checker sanity: billing DOES reach the store (it reads chain rows). If this fails,
// goListDeps is broken and the two tests above prove nothing.
func TestBoundary_CheckerSeesBillingReachingStore(t *testing.T) {
	deps := goListDeps(t, modulePrefix+"internal/billing")
	if _, reachable := deps[modulePrefix+"internal/store"]; !reachable {
		t.Fatal("checker broken: billing must reach store, but go list does not show it")
	}
}
