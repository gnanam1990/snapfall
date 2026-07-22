package integration

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/freeze"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// Test 16 — pin 3's full intersection, one scenario: freeze × exactly-once × restart.
//
//	payment A: executed before the kill
//	payment B: approved, NOT executed
//	KILL (store closed) with the freeze engaged
//	RESTART: freeze replays engaged; lifecycle replays both requests
//	while frozen: B refused (frozen), A refused (already executed)
//	UNFREEZE (audited)
//	B executes exactly once; A still refused
//	the event log holds exactly ONE payment.executed per payment
func TestAT10Extended_KillFreezeRestartUnfreeze(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "intersection.db")
	clockAt := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)

	mkLifecycle := func(st *store.Store) (*approval.Lifecycle, *freeze.Registry, *funding.Agent) {
		clock := newFakeClock(clockAt)
		l := approval.New(st, clock.Now)
		l.Policy = func() (policy.PolicyConfig, string) { return policy.DemoPolicy(), "pol_7" }
		l.Spend = func(string) policy.SpendState { return policy.SpendState{} }
		reg, err := freeze.NewRegistry(ctx, st, clock.Now)
		if err != nil {
			t.Fatalf("NewRegistry: %v", err)
		}
		reg.InFlightProbe = l.InFlight
		l.Freeze = reg
		return l, reg, funding.New()
	}

	mkIntent := func(id, nonceByte string) approval.Intent {
		return approval.Intent{
			IntentID: id, OrgID: "org_demo", JobID: "job_x",
			TaskID: "t1", AgentID: "due-diligence",
			Merchant: policy.DemoMerchantPremium, Resource: "GET /v1/premium-dataset",
			AmountMicros: 4_000_000, Purpose: "dataset " + id,
			Nonce:     "0x" + strings.Repeat(nonceByte, 32),
			ExpiresAt: clockAt.Add(time.Hour),
		}
	}

	// ── Life 1: A executes; B approves; freeze engages; KILL. ──
	st1, _ := store.Open(ctx, dbPath)
	l1, reg1, fund1 := mkLifecycle(st1)

	resA, err := l1.Submit(ctx, mkIntent("pi_A", "aa"))
	if err != nil {
		t.Fatalf("submit A: %v", err)
	}
	l1.Decide(ctx, resA.Request.ID, approval.DecideApprove, "gnanam", "")
	if err := l1.Execute(ctx, resA.Request.Intent, resA.Request.ID, fund1.Execute); err != nil {
		t.Fatalf("execute A: %v", err)
	}

	resB, err := l1.Submit(ctx, mkIntent("pi_B", "bb"))
	if err != nil {
		t.Fatalf("submit B: %v", err)
	}
	l1.Decide(ctx, resB.Request.ID, approval.DecideApprove, "gnanam", "")
	// B is approved but NEVER executed before the kill.

	reg1.Engage(ctx, freeze.KindOrg, "org_demo", "gnanam", "incident, killing daemon")
	idA, intentA := resA.Request.ID, resA.Request.Intent
	idB, intentB := resB.Request.ID, resB.Request.Intent
	st1.Close() // ── THE KILL ──

	// ── Life 2: restart. Freeze and requests replay from the log. ──
	st2, _ := store.Open(ctx, dbPath)
	defer st2.Close()
	l2, reg2, fund2 := mkLifecycle(st2)
	if err := l2.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	// The freeze survived the restart.
	if reg2.Check("org_demo", "", "") == nil {
		t.Fatal("freeze lost across restart")
	}

	// While frozen: B cannot execute (frozen), A cannot re-execute (already executed
	// — checked BEFORE the freeze would even matter; assert the executed claim held).
	if err := l2.Execute(ctx, intentB, idB, fund2.Execute); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("B under freeze: %v, want frozen refusal", err)
	}
	reqA, ok := l2.Request(idA)
	if !ok || !reqA.Executed {
		t.Fatalf("A's executed claim lost in replay: %+v", reqA)
	}

	// ── UNFREEZE (audited), then: B exactly once, A never again. ──
	if err := reg2.Lift(ctx, freeze.KindOrg, "org_demo", "gnanam", "incident resolved"); err != nil {
		t.Fatalf("Lift: %v", err)
	}

	if err := l2.Execute(ctx, intentB, idB, fund2.Execute); err != nil {
		t.Fatalf("B after unfreeze must execute (it was never lost): %v", err)
	}
	if err := l2.Execute(ctx, intentB, idB, fund2.Execute); !errors.Is(err, approval.ErrAlreadyExecuted) {
		t.Fatalf("B replay: %v, want ErrAlreadyExecuted", err)
	}
	if err := l2.Execute(ctx, intentA, idA, fund2.Execute); !errors.Is(err, approval.ErrAlreadyExecuted) {
		t.Fatalf("A replay after unfreeze: %v, want ErrAlreadyExecuted", err)
	}

	// Funding in life 2 acted exactly once (B). A's execution lives in life 1's record.
	if got := len(fund2.Executed()); got != 1 {
		t.Fatalf("funding in life 2 executed %d instruction(s), want 1 (B only)", got)
	}

	// ── The ledger of record: exactly ONE payment.executed per payment, ever. ──
	var n int
	st2.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE kind='payment.executed'`).Scan(&n)
	if n != 2 {
		t.Fatalf("payment.executed events = %d, want exactly 2 (one per payment, across both lives)", n)
	}
	// And the freeze audit trail is complete: engaged + lifted.
	st2.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE kind IN ('freeze.engaged','freeze.lifted')`).Scan(&n)
	if n != 2 {
		t.Fatalf("freeze audit events = %d, want 2 (engage + lift)", n)
	}
}
