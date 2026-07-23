package main

import (
	"os"
	"strings"
	"testing"
)

// The boot-sequence pins — closing a CLASS of defect, not an instance.
//
// Three times now, a capability existed in a package, passed its package tests, and
// never ran in the actual binary: Brain.Recover constructed-and-discarded (caught in
// review), the owner token gating only startup while requests went unauthenticated
// (caught in security review), and Lifecycle.Recover never called at boot (caught
// while wiring the advance path). Green tests, absent property.
//
// These pins apply the single-wiring-site technique in the other direction: every
// capability that MUST run in the serve path is asserted PRESENT in this package's
// non-test sources, and the recovery block is asserted to come BEFORE the supervisor
// starts. A refactor that drops one goes red here instead of the property going
// quietly missing.
//
// HONEST LIMITS, named:
//   - A source pin proves the call is wired, not that a future refactor won't guard it
//     behind a dead branch. The behavioral halves live in their own packages
//     (advancing's restart tests, brain's escalation tests, freeze's replay tests);
//     this test pins the wiring, which is exactly where all three instances failed.
//   - "Auth middleware on every route" is pinned structurally in ownerapi (the root
//     mux admits exactly the two credential-wrapped customer routes plus the
//     withAuth-wrapped owner mux) and behaviorally by the per-route 401 tests there —
//     a full automatic route enumeration is not cheap in net/http and is not attempted.
//   - Key-format validation is NOT in this list because the capability does not exist
//     yet — the daemon holds no chain key until the write path lands. When the signer
//     arrives, its startup validation joins these pins; this comment is the reminder.
func TestBoot_StartupCapabilitiesAreWired(t *testing.T) {
	src := serveSource(t)

	required := []struct{ token, why string }{
		{"br.Recover()", "Brain.Recover — job memory replays at boot (the #4 finding)"},
		{".EscalateInterruptedTasks(", "crash-mid-task policy — interrupted jobs escalate, never resume"},
		{"life.Recover(", "Lifecycle.Recover — the approval ledger replays at boot (the third-instance finding)"},
		{".EscalateInterrupted(", "advancing — approved-but-unexecuted advances surface, never auto-execute"},
		{"freeze.NewRegistry(", "the kill switch replays its engagements from the event log"},
		{"br.SetRootContext(", "SIGTERM semantics — task lifetimes bound to the daemon root"},
		{"br.WaitTasks()", "shutdown drain — task goroutines complete before the store closes"},
		{"br.SetPurchaser(", "worker spends route through the real policy+approval pipeline"},
	}
	for _, req := range required {
		if !strings.Contains(src, req.token) {
			t.Errorf("boot capability missing from the serve path: %s (%s)", req.token, req.why)
		}
	}

	// Order: every recovery call above lives inside wireBrain, and wireBrain's
	// INVOCATION must precede sup.Start in run(). Per-token source position is
	// meaningless across function definitions (wireBrain's body sits below run() in
	// the file), so the pin is on the call chain: the wireBrain call comes first, and
	// the recovery tokens are confirmed present (above) inside this package.
	start := strings.Index(src, "sup.Start(")
	if start < 0 {
		t.Fatal("sup.Start not found — the serve path changed shape; re-pin deliberately")
	}
	wire := strings.Index(src, "wireBrain(")
	if wire < 0 || wire > start {
		t.Fatalf("wireBrain must be invoked before sup.Start (wire=%d start=%d) — recovery completes before serving", wire, start)
	}
}

func serveSource(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(raw)
	}
	return b.String()
}
