package purchasing

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/brain"
	"github.com/gnanam1990/snapfall/daemon/internal/freeze"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// fakeClock drives BOTH the lifecycle (via Now) and the Purchaser's Deadline waits from one
// time source, so expiry cannot disagree between them. Advance releases deadline waiters.
type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []deadlineWaiter
}
type deadlineWaiter struct {
	at time.Time
	ch chan struct{}
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)} }
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *fakeClock) Deadline(t time.Time) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan struct{})
	if !c.now.Before(t) {
		close(ch)
		return ch
	}
	c.waiters = append(c.waiters, deadlineWaiter{t, ch})
	return ch
}
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if !c.now.Before(w.at) {
			close(w.ch)
		} else {
			kept = append(kept, w)
		}
	}
	c.waiters = kept
}

func newPurchaser(t *testing.T) (*Purchaser, *approval.Lifecycle, *fakeClock, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	fc := newFakeClock()
	l := approval.New(st, fc.Now)
	l.Policy = func() (policy.PolicyConfig, string) { return policy.DemoPolicy(), "pol_7" }
	l.Spend = func(string) policy.SpendState { return policy.SpendState{} }
	p := New(l, st, fc, "org_demo", 5*time.Minute)
	return p, l, fc, st
}

func intent(merchant string, amountMicros int64) brain.PurchaseIntent {
	return brain.PurchaseIntent{
		JobID: "job_104", AgentKind: "due-diligence",
		Merchant: merchant, Resource: "GET /v1/x",
		AmountMicros: amountMicros, MaxAmountMicros: amountMicros, Purpose: "source",
	}
}

// DENY: an unallowlisted merchant is denied by policy, and the STRUCTURED reason (code +
// message) flows back — the worker can distinguish this from "needs approval"/"expired".
func TestPurchaser_DenyCarriesStructuredReason(t *testing.T) {
	p, _, _, _ := newPurchaser(t)
	out, err := p.Decide(context.Background(), intent("api.not-allowlisted.example", 40_000))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Decision != "DENY" || out.Status != "denied" || out.Code == "" || out.Reason == "" {
		t.Fatalf("deny outcome not structured: %+v", out)
	}
}

// AUTO_APPROVE: a small allowlisted buy runs the execution gates and returns
// approved-pending-integration — money movement pending F2, with an honest ledger marker.
func TestPurchaser_AutoApproveIsPendingIntegrationNotFabricated(t *testing.T) {
	p, _, _, st := newPurchaser(t)
	out, err := p.Decide(context.Background(), intent(policy.DemoMerchantProfile, 40_000))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Decision != "AUTO_APPROVE" || out.Status != "approved-pending-integration" {
		t.Fatalf("auto-approve outcome wrong: %+v", out)
	}
	if out.Data != nil || out.Receipt != nil {
		t.Fatal("no data or receipt may be fabricated for a pending purchase")
	}
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE kind='purchase.pending_settlement'`).Scan(&n)
	if n != 1 {
		t.Fatalf("pending-settlement not recorded honestly: %d events", n)
	}
}

// HUMAN_APPROVAL_REQUIRED -> owner APPROVES: the $4.00 escalation is real, and once the
// owner approves, the purchase proceeds to pending-integration.
func TestPurchaser_HumanApproval_OwnerApproves(t *testing.T) {
	p, l, _, _ := newPurchaser(t)
	done := make(chan struct{})
	p.afterSubmit = func(reqID string) {
		if _, err := l.Decide(context.Background(), reqID, approval.DecideApprove, "gnanam", "looks good"); err != nil {
			t.Errorf("Decide(approve): %v", err)
		}
		close(done)
	}
	out, err := p.Decide(context.Background(), intent(policy.DemoMerchantProfile, 4_000_000))
	<-done
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Status != "approved-pending-integration" {
		t.Fatalf("after owner approval: %+v", out)
	}
}

// HUMAN_APPROVAL_REQUIRED -> owner REJECTS: this is AT-04. The rejection is a real approval
// decision and its structured reason reaches the worker intact.
func TestPurchaser_HumanApproval_OwnerRejectsWithReason(t *testing.T) {
	p, l, _, _ := newPurchaser(t)
	p.afterSubmit = func(reqID string) {
		if _, err := l.Decide(context.Background(), reqID, approval.DecideReject, "gnanam", "too expensive; find a cheaper source"); err != nil {
			t.Errorf("Decide(reject): %v", err)
		}
	}
	out, err := p.Decide(context.Background(), intent(policy.DemoMerchantProfile, 4_000_000))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Decision != "DENY" || out.Status != "denied" {
		t.Fatalf("rejection outcome wrong: %+v", out)
	}
	if out.Reason != "too expensive; find a cheaper source" {
		t.Fatalf("owner's structured reason did not reach the worker: %q", out.Reason)
	}
}

// EXPIRY: the owner never decides. The blocked Purchase wakes from the SAME approval
// ExpiresAt via the injected clock — not a second timeout — returns "expired", and the
// goroutine TERMINATES (no unkillable hang despite context.WithoutCancel upstream).
func TestPurchaser_HumanApproval_ExpiresWhenOwnerNeverDecides(t *testing.T) {
	p, _, fc, _ := newPurchaser(t)

	resCh := make(chan struct{})
	var out struct {
		decision, status, code string
	}
	// afterSubmit fires once the request is pending and the Purchase is about to block —
	// then we advance the clock PAST the 5-minute window to trigger the expiry wake.
	p.afterSubmit = func(_ string) { fc.Advance(6 * time.Minute) }
	go func() {
		o, err := p.Decide(context.Background(), intent(policy.DemoMerchantProfile, 4_000_000))
		if err != nil {
			t.Errorf("Decide: %v", err)
		}
		out.decision, out.status, out.code = o.Decision, o.Status, o.Code
		close(resCh)
	}()

	select {
	case <-resCh:
	case <-time.After(3 * time.Second):
		t.Fatal("DEADLOCK: the blocked Purchase never woke on expiry — the goroutine hung")
	}
	if out.status != "expired" || out.code != "approval-expired" {
		t.Fatalf("expiry outcome wrong: decision=%s status=%s code=%s", out.decision, out.status, out.code)
	}
}

// FREEZE one layer out: a freeze engaging between the policy decision and Execute is
// refused by the existing gate inside approval.Execute — verified, not assumed.
func TestPurchaser_FreezeBetweenDecisionAndExecuteIsRefused(t *testing.T) {
	p, l, _, st := newPurchaser(t)
	reg, err := freeze.NewRegistry(context.Background(), st, time.Now)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	l.Freeze = reg

	// Freeze the org the instant the request is submitted — before Execute runs.
	p.afterSubmit = func(reqID string) {
		reg.Engage(context.Background(), freeze.KindOrg, "org_demo", "gnanam", "mid-purchase freeze")
		// Then approve, so the ONLY thing that can stop execution is the freeze gate.
		l.Decide(context.Background(), reqID, approval.DecideApprove, "gnanam", "ok")
	}
	out, err := p.Decide(context.Background(), intent(policy.DemoMerchantProfile, 4_000_000))
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if out.Status != "denied" || out.Code != "execution-refused" {
		t.Fatalf("freeze between decision and execute was not refused: %+v", out)
	}
}

// Serve pin 2: shutdown landing BEFORE the write-ahead claim is a safe refusal — the
// purchase is interrupted, NOTHING is claimed, and restart has nothing to double-pay.
// (Whichever select branch wins the decided-vs-cancelled race, the outcome is the same
// wrapped context error and zero claims — deterministic assertions.) Cancellation PAST the
// claim is impossible by construction: execute() checks ctx once before the claim and then
// hands approval.Execute a context.WithoutCancel — no cancellable context exists in scope
// beyond that line.
func TestPurchaser_ShutdownBeforeClaimRefusesSafely(t *testing.T) {
	p, l, _, st := newPurchaser(t)
	ctx, sigterm := context.WithCancel(context.Background())
	p.afterSubmit = func(reqID string) {
		// The owner approves — and shutdown lands in the same instant.
		l.Decide(context.Background(), reqID, approval.DecideApprove, "gnanam", "ok")
		sigterm()
	}
	_, err := p.Decide(ctx, intent(policy.DemoMerchantProfile, 4_000_000))
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("want an interruption wrapping context.Canceled, got %v", err)
	}
	var n int
	st.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE kind='payment.executing'`).Scan(&n)
	if n != 0 {
		t.Fatalf("a write-ahead claim was written for an interrupted purchase (%d) — double-pay hazard on restart", n)
	}
}
