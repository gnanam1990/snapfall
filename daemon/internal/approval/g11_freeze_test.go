package approval

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/freeze"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// AT-09 (docs/SRS-v4-annex.md:787): "Freeze stops new claims, signatures, advances;
// dashboard remains readable." The advance clause is STRUCTURALLY UNPROVEN — no
// advance-request path exists in the daemon yet (see the freeze package doc).

func newFrozenFixture(t *testing.T) (*fixture, *freeze.Registry, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "g11.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	f := newFixtureOn(t, st)
	reg, err := freeze.NewRegistry(context.Background(), st, f.clock.Now)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	f.l.Freeze = reg
	return f, reg, st
}

// Test 6 — a frozen scope cannot Submit, the gate fires BEFORE the nonce claim, and
// the SAME nonce submits cleanly after the lift ("stops new claims").
func TestAT09_FrozenScopeCannotSubmit(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind freeze.Kind
		id   string
	}{
		{"org scope", freeze.KindOrg, "org_demo"},
		{"job scope", freeze.KindJob, "job_104"},
		{"agent scope", freeze.KindAgent, "due-diligence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, reg, _ := newFrozenFixture(t)
			ctx := context.Background()

			reg.Engage(ctx, tc.kind, tc.id, "gnanam", "kill switch")

			in := f.demoIntent(nil)
			_, err := f.l.Submit(ctx, in)
			if err == nil || !strings.Contains(err.Error(), "frozen") {
				t.Fatalf("frozen submit: %v, want a frozen refusal", err)
			}

			// The nonce was NOT burned: lift, resubmit the same intent, it works.
			reg.Lift(ctx, tc.kind, tc.id, "gnanam", "resolved")
			if _, err := f.l.Submit(ctx, in); err != nil {
				t.Fatalf("submit after lift with the SAME nonce: %v — the frozen gate burned the nonce", err)
			}
		})
	}
}

// Test 7 — the key one: approved BEFORE the freeze, executed AFTER it engages.
// No Grant is minted; the executor never runs ("stops new signatures").
func TestAT09_FrozenScopeCannotExecute(t *testing.T) {
	f, reg, _ := newFrozenFixture(t)
	ctx := context.Background()

	res, _ := f.l.Submit(ctx, f.demoIntent(nil))
	f.l.Decide(ctx, res.Request.ID, DecideApprove, "gnanam", "approved pre-freeze")

	reg.Engage(ctx, freeze.KindJob, "job_104", "gnanam", "incident mid-job")

	err := f.l.Execute(ctx, res.Request.Intent, res.Request.ID, f.exec.fn())
	if err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("frozen execute: %v, want a frozen refusal naming the freeze", err)
	}
	for _, want := range []string{"job", "gnanam", "incident mid-job"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q: %v", want, err)
		}
	}
	if f.exec.n.Load() != 0 {
		t.Fatal("executor ran in a frozen scope — a signature was produced")
	}

	// The request is NOT consumed: it executes fine once lifted (test 10 folded in).
	reg.Lift(ctx, freeze.KindJob, "job_104", "gnanam", "resolved")
	if err := f.l.Execute(ctx, res.Request.Intent, res.Request.ID, f.exec.fn()); err != nil {
		t.Fatalf("execute after lift: %v", err)
	}
	if f.exec.n.Load() != 1 {
		t.Fatal("post-lift execution did not run exactly once")
	}
}

// Test 8 — the structural pin: exactly ONE Grant construction site exists in the
// package, so the freeze check in front of it gates ALL grant minting. Mirrors the
// StageDeliveryReady scan. (A zero-value `Grant{}` carries nothing and matches
// nothing here; the scan targets composite literals with fields.)
func TestAT09_GrantSiteSingleAndGated(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`Grant\{[a-z]`) // a Grant literal with (unexported) fields
	sites := 0
	var siteFile string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(".", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if n := len(re.FindAll(raw, -1)); n > 0 {
			sites += n
			siteFile = e.Name()
		}
	}
	if sites != 1 {
		t.Fatalf("found %d Grant construction sites, want exactly 1 — a second site can mint money credentials past the freeze gate", sites)
	}
	// And the single site lives in lifecycle.go, inside Execute, after the freeze check.
	if siteFile != "lifecycle.go" {
		t.Fatalf("the Grant site moved to %s — re-verify it sits behind the freeze gate", siteFile)
	}
}

// Test 9 — pin 2's definition, tested: a freeze engaged FROM INSIDE the executor
// (mid-execution) lets the current execution complete and record; the next intent is
// refused; the freeze event carries in_flight=1 and the owner report says so.
func TestFreeze_MidExecutionCompletes(t *testing.T) {
	f, reg, st := newFrozenFixture(t)
	ctx := context.Background()

	res, _ := f.l.Submit(ctx, f.demoIntent(nil))
	f.l.Decide(ctx, res.Request.ID, DecideApprove, "gnanam", "")

	ran := false
	err := f.l.Execute(ctx, res.Request.Intent, res.Request.ID, func(ctx context.Context, g Grant) error {
		// The owner slams the kill switch while the payment is mid-flight.
		if _, err := reg.Engage(ctx, freeze.KindOrg, "org_demo", "gnanam", "mid-payment freeze"); err != nil {
			return err
		}
		ran = true
		return nil
	})
	if err != nil {
		t.Fatalf("in-flight execution must COMPLETE, got: %v", err)
	}
	if !ran {
		t.Fatal("executor did not run")
	}

	// The completion is durably recorded.
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE kind='payment.executed'`).Scan(&n)
	if n != 1 {
		t.Fatalf("payment.executed events = %d, want 1", n)
	}

	// The freeze event and owner report carry the in-flight visibility (addition 3).
	rep := reg.StatusReport()
	if len(rep.Active) != 1 || rep.Active[0].InFlightAtEngage != 1 {
		t.Fatalf("freeze entry in_flight = %+v, want 1", rep.Active)
	}
	if !strings.Contains(rep.InFlightNote, "COMPLETED rather than aborting") {
		t.Fatalf("owner report missing the in-flight note: %q", rep.InFlightNote)
	}

	// And the NEXT intent is refused.
	next := f.demoIntent(func(in *Intent) {
		in.IntentID = "pi_next"
		in.Nonce = "0x" + strings.Repeat("ee", 32)
	})
	if _, err := f.l.Submit(ctx, next); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("next intent after mid-flight freeze: %v, want frozen refusal", err)
	}
}

// Test 14 — Recover rebuilds requests from the event log: an approved-but-unexecuted
// request survives a restart and executes exactly once (the gap pin 3 found, closed).
func TestRecover_LifecycleReplaysFromEvents(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "recover.db")

	st1, _ := store.Open(ctx, dbPath)
	f1 := newFixtureOn(t, st1)
	res, err := f1.l.Submit(ctx, f1.demoIntent(nil))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	f1.l.Decide(ctx, res.Request.ID, DecideApprove, "gnanam", "approved then killed")
	reqID := res.Request.ID
	intent := res.Request.Intent
	st1.Close() // the kill: approved, never executed

	// Restart.
	st2, _ := store.Open(ctx, dbPath)
	defer st2.Close()
	f2 := newFixtureOn(t, st2)
	if err := f2.l.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	req, ok := f2.l.Request(reqID)
	if !ok {
		t.Fatal("approved request LOST across restart — the pin-3 gap is back")
	}
	if req.State != StateApproved || req.DecidedBy != "gnanam" || req.Executed {
		t.Fatalf("recovered request wrong: %+v", req)
	}
	if req.IntentHash != InternalHash(intent) {
		t.Fatal("recovered intent hash does not verify")
	}

	// It executes exactly once post-restart.
	if err := f2.l.Execute(ctx, intent, reqID, f2.exec.fn()); err != nil {
		t.Fatalf("post-recovery execute: %v", err)
	}
	if err := f2.l.Execute(ctx, intent, reqID, f2.exec.fn()); !errors.Is(err, ErrAlreadyExecuted) {
		t.Fatalf("second execute: %v, want ErrAlreadyExecuted", err)
	}
	if f2.exec.n.Load() != 1 {
		t.Fatalf("executor ran %d times, want 1", f2.exec.n.Load())
	}
}

// Test 15 — extended AT-10: an EXECUTED payment never repeats across restart, even
// though the executor ran before the kill.
func TestAT10Extended_ExecutedPaymentNeverRepeats(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "at10.db")

	st1, _ := store.Open(ctx, dbPath)
	f1 := newFixtureOn(t, st1)
	res, _ := f1.l.Submit(ctx, f1.demoIntent(nil))
	f1.l.Decide(ctx, res.Request.ID, DecideApprove, "gnanam", "")
	if err := f1.l.Execute(ctx, res.Request.Intent, res.Request.ID, f1.exec.fn()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	reqID, intent := res.Request.ID, res.Request.Intent
	st1.Close() // killed after execution

	st2, _ := store.Open(ctx, dbPath)
	defer st2.Close()
	f2 := newFixtureOn(t, st2)
	if err := f2.l.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	req, ok := f2.l.Request(reqID)
	if !ok || !req.Executed {
		t.Fatalf("executed claim lost across restart: %+v", req)
	}
	if err := f2.l.Execute(ctx, intent, reqID, f2.exec.fn()); !errors.Is(err, ErrAlreadyExecuted) {
		t.Fatalf("replayed execute: %v, want ErrAlreadyExecuted", err)
	}
	if f2.exec.n.Load() != 0 {
		t.Fatal("a completed payment REPEATED after restart — extended AT-10 violated")
	}

	// The same nonce cannot re-enter either (durable intake dedup).
	if _, err := f2.l.Submit(ctx, intent); !errors.Is(err, ErrNonceReplayed) {
		t.Fatalf("resubmit of executed intent: %v, want ErrNonceReplayed", err)
	}
}

// policy sanity for the fixture: the demo policy must accept the fixture's agent
// (guards against fixture drift making the freeze tests vacuous).
func TestG11_FixtureIntentActuallyEvaluates(t *testing.T) {
	f, _, _ := newFrozenFixture(t)
	d := policy.Evaluate(policy.DemoPolicy(), policy.SpendState{}, policy.PaymentIntent{
		JobID: "job_104", Merchant: f.demoIntent(nil).Merchant, AmountMicros: 4_000_000,
	})
	if d.Outcome != policy.HumanApprovalRequired {
		t.Fatalf("fixture intent no longer escalates: %s", d.Outcome)
	}
}
