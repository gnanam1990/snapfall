package freeze

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// AT-09 (docs/SRS-v4-annex.md:787, verbatim):
//   "Freeze stops new claims, signatures, advances; dashboard remains readable."
// SEC-009: "Global freeze stops new payments and advance requests within 1 s of
// local command."

func newRegistry(t *testing.T) (*Registry, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "freeze.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	r, err := NewRegistry(context.Background(), st, time.Now)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return r, st
}

func eventCount(t *testing.T, st *store.Store, kind string) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE kind = ?`, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Test 1 — the ordering guarantee that IS the ≤1s bound (and better): the moment
// Engage returns, Check refuses. Plus the non-flaky sanity bound on Engage itself.
func TestFreeze_EngageIsImmediate(t *testing.T) {
	r, _ := newRegistry(t)
	ctx := context.Background()

	start := time.Now()
	if _, err := r.Engage(ctx, KindOrg, "org_demo", "gnanam", "incident"); err != nil {
		t.Fatalf("Engage: %v", err)
	}
	elapsed := time.Since(start)

	// Ordering: the very next check sees it. No sleep, no poll, no race.
	if e := r.Check("org_demo", "job_x", "agent_x"); e == nil {
		t.Fatal("Check passed immediately after Engage returned — the ordering guarantee is broken")
	}
	// SEC-009 linkage: one transactional append. The 1s bound has ~3 orders of
	// magnitude of margin; this cannot flake on a loaded box.
	if elapsed > time.Second {
		t.Fatalf("Engage took %v — SEC-009's 1s bound violated by the command itself", elapsed)
	}
}

// Test 2 — scopes are exact.
func TestFreeze_ScopesAreExact(t *testing.T) {
	r, _ := newRegistry(t)
	ctx := context.Background()

	r.Engage(ctx, KindJob, "job_a", "gnanam", "job scope")
	r.Engage(ctx, KindAgent, "due-diligence", "gnanam", "agent scope")

	if r.Check("org_demo", "job_a", "") == nil {
		t.Error("frozen job not caught")
	}
	if r.Check("org_demo", "job_b", "") != nil {
		t.Error("unrelated job caught by a job-scope freeze")
	}
	if r.Check("org_demo", "job_b", "due-diligence") == nil {
		t.Error("frozen agent not caught on another job")
	}
	if r.Check("org_demo", "job_b", "qa-reviewer") != nil {
		t.Error("unrelated agent caught")
	}

	// Org freeze covers everything in the org.
	r.Engage(ctx, KindOrg, "org_demo", "gnanam", "org scope")
	if r.Check("org_demo", "job_b", "qa-reviewer") == nil {
		t.Error("org freeze must cover every job and agent in the org")
	}
	if r.Check("org_other", "job_z", "") != nil {
		t.Error("another org caught by this org's freeze")
	}
}

// Test 2b — the architect's addition: lifting a NARROWER scope never unfreezes work
// a BROADER still-active scope covers.
func TestFreeze_LiftNarrowerKeepsBroaderFrozen(t *testing.T) {
	r, _ := newRegistry(t)
	ctx := context.Background()

	r.Engage(ctx, KindJob, "job_a", "gnanam", "job first")
	r.Engage(ctx, KindOrg, "org_demo", "gnanam", "then the org")

	// Lift the job. The org is still frozen — the job must remain covered.
	if err := r.Lift(ctx, KindJob, "job_a", "gnanam", "job issue resolved"); err != nil {
		t.Fatalf("Lift: %v", err)
	}
	e := r.Check("org_demo", "job_a", "")
	if e == nil {
		t.Fatal("lifting the job freeze unfroze work the org freeze still covers")
	}
	if e.Kind != KindOrg {
		t.Fatalf("covering scope is %s, want org", e.Kind)
	}

	// Only lifting the org too clears it.
	r.Lift(ctx, KindOrg, "org_demo", "gnanam", "all clear")
	if r.Check("org_demo", "job_a", "") != nil {
		t.Fatal("scope still frozen after both lifts")
	}
}

// Test 3 — lift restores, and both directions are audited with actor + reason.
func TestFreeze_LiftRestoresAndIsAudited(t *testing.T) {
	r, st := newRegistry(t)
	ctx := context.Background()

	r.Engage(ctx, KindJob, "job_a", "gnanam", "suspicious spend")
	r.Lift(ctx, KindJob, "job_a", "gnanam", "false alarm")

	if r.Check("org_demo", "job_a", "") != nil {
		t.Fatal("still frozen after lift")
	}
	if n := eventCount(t, st, "freeze.engaged"); n != 1 {
		t.Errorf("freeze.engaged events = %d, want 1", n)
	}
	if n := eventCount(t, st, "freeze.lifted"); n != 1 {
		t.Errorf("freeze.lifted events = %d, want 1", n)
	}

	var actor, payload string
	st.DB().QueryRow(`SELECT actor, payload_json FROM events WHERE kind='freeze.lifted'`).Scan(&actor, &payload)
	if actor != "gnanam" {
		t.Errorf("lift actor = %q", actor)
	}
	for _, want := range []string{"false alarm", "gnanam"} {
		if !contains(payload, want) {
			t.Errorf("lift audit payload missing %q: %s", want, payload)
		}
	}
}

// Test 4 — a kill switch that lifts itself on restart is broken.
func TestFreeze_SurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "restart.db")

	st1, _ := store.Open(ctx, dbPath)
	r1, err := NewRegistry(ctx, st1, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	r1.Engage(ctx, KindOrg, "org_demo", "gnanam", "engaged before crash")
	st1.Close()

	st2, _ := store.Open(ctx, dbPath)
	defer st2.Close()
	r2, err := NewRegistry(ctx, st2, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Check("org_demo", "", "") == nil {
		t.Fatal("freeze silently lifted across restart")
	}

	// And a lift BEFORE the crash also replays: engage+lift another scope, restart, clear.
	r2.Engage(ctx, KindJob, "job_b", "gnanam", "x")
	r2.Lift(ctx, KindJob, "job_b", "gnanam", "y")
	st2.Close()
	st3, _ := store.Open(ctx, dbPath)
	defer st3.Close()
	r3, _ := NewRegistry(ctx, st3, time.Now)
	if r3.Check("", "job_b", "") != nil {
		t.Fatal("lifted freeze re-engaged across restart")
	}
	if r3.Check("org_demo", "", "") == nil {
		t.Fatal("org freeze lost across second restart")
	}
}

// Test 5 — idempotency, both directions, recorded.
func TestFreeze_EngageAndLiftIdempotent(t *testing.T) {
	r, st := newRegistry(t)
	ctx := context.Background()

	e1, _ := r.Engage(ctx, KindJob, "job_a", "gnanam", "first")
	e2, err := r.Engage(ctx, KindJob, "job_a", "anandan", "second command")
	if err != nil {
		t.Fatalf("duplicate engage must not error: %v", err)
	}
	if !e2.At.Equal(e1.At) || e2.By != e1.By {
		t.Error("duplicate engage mutated the original entry")
	}
	if n := eventCount(t, st, "freeze.engaged.duplicate"); n != 1 {
		t.Errorf("duplicate engage not recorded: %d", n)
	}

	if err := r.Lift(ctx, KindJob, "job_never_frozen", "gnanam", "oops"); err != nil {
		t.Fatalf("lift of non-frozen scope must not error: %v", err)
	}
	if n := eventCount(t, st, "freeze.lifted.duplicate"); n != 1 {
		t.Errorf("no-op lift not recorded: %d", n)
	}
}

// Test 13 (freeze side) — the owner report works while frozen and carries the
// in-flight note when the switch landed mid-execution (pin 2's visibility).
func TestFreeze_StatusReportCarriesInFlightNote(t *testing.T) {
	r, _ := newRegistry(t)
	ctx := context.Background()

	r.InFlightProbe = func() int { return 1 } // one execution mid-flight
	r.Engage(ctx, KindOrg, "org_demo", "gnanam", "kill switch during payment")

	rep := r.StatusReport()
	if len(rep.Active) != 1 {
		t.Fatalf("report shows %d active freezes, want 1", len(rep.Active))
	}
	if rep.Active[0].InFlightAtEngage != 1 {
		t.Error("in-flight count not recorded on the entry")
	}
	for _, want := range []string{"1 execution(s) were in flight", "COMPLETED rather than aborting", "double-pay"} {
		if !contains(rep.InFlightNote, want) {
			t.Errorf("owner note missing %q:\n%s", want, rep.InFlightNote)
		}
	}

	// With nothing in flight, no note — the owner is not warned about nothing.
	r2, _ := newRegistry(t)
	r2.Engage(ctx, KindJob, "job_a", "gnanam", "quiet freeze")
	if note := r2.StatusReport().InFlightNote; note != "" {
		t.Errorf("unexpected in-flight note: %q", note)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
