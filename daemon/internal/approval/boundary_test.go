package approval

import (
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// Step-5 structural check: Funding cannot act on a raw policy Decision.
//
// Proven the AT-16 way — over the compiler's import graph and the type system — not by
// a runtime check:
//
//  1. internal/funding has NO import path to internal/policy: a policy.Decision is not
//     a type Funding can even NAME, so no code path can hand one to it.
//  2. internal/funding has NO import path to internal/approval either: it cannot invoke
//     the lifecycle; only the wiring layer (brain, and later the Funding executor) can.
//  3. The Executor bridge takes an approval.Grant whose fields are ALL unexported: a
//     Grant forged outside this package is empty — it names no amount, no merchant, no
//     job. The data needed to move money enters a Grant only inside Execute, after
//     every gate (hash, state, expiry, policy version, exactly-once) has passed.
//
// Why this matters (the architect's scenario): Evaluate() is pure and clock-free, so an
// EXPIRED approval is invisible to it. If a bare Decision could reach execution, expiry
// would be unenforced. These three properties make that path unexpressible.

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

// Funding cannot name a policy.Decision: no import path exists.
func TestBoundary_FundingCannotNameAPolicyDecision(t *testing.T) {
	deps := goListDeps(t, modulePrefix+"internal/funding")
	if _, reachable := deps[modulePrefix+"internal/policy"]; reachable {
		t.Fatal("internal/funding can reach internal/policy — a bare Decision becomes expressible in Funding's vocabulary, and an expired approval could sail through (Evaluate is clock-free; expiry lives in G7 alone)")
	}
}

// Funding cannot invoke the lifecycle either — the direction of dependency is
// wiring→funding, never funding→approval.
func TestBoundary_FundingCannotReachTheLifecycle(t *testing.T) {
	deps := goListDeps(t, modulePrefix+"internal/funding")
	if _, reachable := deps[modulePrefix+"internal/approval"]; reachable {
		t.Fatal("internal/funding can reach internal/approval — funding must be a passive boundary the wiring hands grants to")
	}
}

// Checker sanity: approval DOES reach policy (it evaluates intents). If this fails,
// goListDeps is broken and the two tests above prove nothing.
func TestBoundary_CheckerSeesApprovalReachingPolicy(t *testing.T) {
	deps := goListDeps(t, modulePrefix+"internal/approval")
	if _, reachable := deps[modulePrefix+"internal/policy"]; !reachable {
		t.Fatal("checker broken: approval must reach policy, but go list does not show it")
	}
}

// Every Grant field is unexported — the type-system half of the proof. If someone adds
// an exported field, a forged Grant could carry attacker-chosen data into an executor,
// and this test names the field.
func TestBoundary_GrantFieldsAllUnexported(t *testing.T) {
	typ := reflect.TypeOf(Grant{})
	if typ.NumField() == 0 {
		t.Fatal("Grant has no fields — the capability carries nothing")
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.IsExported() {
			t.Errorf("Grant.%s is exported — a forged Grant could smuggle data into an executor", f.Name)
		}
	}
}

// A zero-value (forged) Grant is empty and useless: no amount, no merchant, no job.
func TestBoundary_ForgedGrantIsEmpty(t *testing.T) {
	var forged Grant
	if !forged.Empty() {
		t.Fatal("zero-value Grant must report Empty")
	}
	in := forged.Intent()
	if in.AmountMicros != 0 || in.Merchant != "" || in.JobID != "" {
		t.Fatalf("forged Grant carries data: %+v", in)
	}
}
