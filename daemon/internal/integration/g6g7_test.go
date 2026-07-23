// Package integration proves G6 + G7 work as ONE path over the real daemon substrate:
// one WAL SQLite store shared by Brain (Phase 1) and the payment lifecycle (Phase 2),
// with the funding boundary at the end and the event log recording every hop.
package integration

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/brain"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

type fakeClock struct{ now atomic.Int64 }

func newFakeClock(t time.Time) *fakeClock {
	c := &fakeClock{}
	c.now.Store(t.UnixNano())
	return c
}
func (c *fakeClock) Now() time.Time          { return time.Unix(0, c.now.Load()).UTC() }
func (c *fakeClock) Advance(d time.Duration) { c.now.Add(int64(d)) }

// eventKinds reads the kinds column in sequence order.
func eventKinds(t *testing.T, st *store.Store) []string {
	t.Helper()
	rows, err := st.DB().Query(`SELECT kind FROM events ORDER BY seq`)
	if err != nil {
		t.Fatalf("querying events: %v", err)
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("scan: %v", err)
		}
		kinds = append(kinds, k)
	}
	return kinds
}

// assertSubsequence checks that want appears in kinds, in order (gaps allowed).
func assertSubsequence(t *testing.T, kinds, want []string) {
	t.Helper()
	i := 0
	for _, k := range kinds {
		if i < len(want) && k == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("event log missing %q (matched %d/%d) in:\n%s",
			want[i], i, len(want), strings.Join(kinds, "\n"))
	}
}

// The Step-5 integration: Brain runs the Phase-1 stub DD job, then the SAME store
// carries the $4.00 intent through policy → approval → execution → funding, and the
// event log tells the whole story in order.
func TestIntegration_PolicyApprovalFundingOnePath(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "integration.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mem, err := brain.NewMemoryStore(filepath.Join(t.TempDir(), "jobs"))
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	fund := funding.New()

	// ── Phase 1 alive: Brain routes the stub DD job end-to-end on this store. ──
	b := brain.New(log, st, mem, fund)
	b.SetScoper(brain.StubScoper{})
	if err := b.RegisterWorker(worker.StubDD{}); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if _, err := b.HandleOwnerRequest(ctx, "job_dd_1", "Acme Corp"); err != nil {
		t.Fatalf("HandleOwnerRequest: %v", err)
	}
	if err := b.Confirm(ctx, "job_dd_1", "gnanam"); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	jm, err := mem.Get("job_dd_1")
	if err != nil || jm.CompletionPct != 100 {
		t.Fatalf("Phase 1 regressed: DD job did not complete (%+v, %v)", jm, err)
	}

	// ── Phase 2: the payment path on the SAME store. ──
	clock := newFakeClock(time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC))
	l := approval.New(st, clock.Now)
	l.Policy = func() (policy.PolicyConfig, string) { return policy.DemoPolicy(), "pol_7" }
	l.Spend = func(string) policy.SpendState { return policy.SpendState{} }

	intent := approval.Intent{
		IntentID: "pi_int_1", OrgID: "org_demo", JobID: "job_dd_1",
		TaskID: "task_research_01", AgentID: "due-diligence",
		Merchant: policy.DemoMerchantPremium, Resource: "GET /v1/premium-dataset",
		AmountMicros: 4_000_000, Purpose: "premium market dataset",
		Nonce:     "0x" + strings.Repeat("aa", 32),
		ExpiresAt: clock.Now().Add(5 * time.Minute),
	}

	res, err := l.Submit(ctx, intent)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.Decision.Outcome != policy.HumanApprovalRequired {
		t.Fatalf("outcome %s, want HUMAN_APPROVAL_REQUIRED", res.Decision.Outcome)
	}

	if _, err := l.Decide(ctx, res.Request.ID, approval.DecideApprove, "gnanam", "needed"); err != nil {
		t.Fatalf("Decide: %v", err)
	}

	// The executor is the funding boundary: it consumes an approval-minted Grant and
	// relays an owner-approved instruction. This is the ONLY shape in which funding
	// ever acts — never from a bare Decision.
	// The executor hands the grant STRAIGHT to funding — wiring cannot restate or
	// embellish the values; funding derives everything from the grant itself.
	execErr := l.Execute(ctx, res.Request.Intent, res.Request.ID, fund.Execute)
	if execErr != nil {
		t.Fatalf("Execute: %v", execErr)
	}

	done := fund.Executed()
	if len(done) != 1 || done[0].AmountMicros != 4_000_000 || done[0].RequestID != res.Request.ID {
		t.Fatalf("funding boundary saw the wrong instruction: %+v", done)
	}

	// ── The event log tells the WHOLE story, in order, in one place. ──
	kinds := eventKinds(t, st)
	assertSubsequence(t, kinds, []string{
		"brain.msg.owner.request",
		"brain.msg.brain.scope_proposal",
		"brain.msg.owner.confirm",
		"brain.msg.brain.assignment",
		"brain.msg.worker.report",
		"policy.evaluated",
		"approval.requested",
		"approval.approve",
		"payment.executing",
		"payment.executed",
	})
}

// The architect's scenario, end to end on the real path: Evaluate is clock-free, so an
// expired approval is invisible to policy — and must still never reach funding, because
// execution goes through the G7 gate.
func TestIntegration_ExpiredApprovalNeverReachesFunding(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "expiry.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	fund := funding.New()
	clock := newFakeClock(time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC))
	l := approval.New(st, clock.Now)
	l.Policy = func() (policy.PolicyConfig, string) { return policy.DemoPolicy(), "pol_7" }
	l.Spend = func(string) policy.SpendState { return policy.SpendState{} }

	intent := approval.Intent{
		IntentID: "pi_exp_1", OrgID: "org_demo", JobID: "job_dd_1",
		TaskID: "t1", AgentID: "due-diligence",
		Merchant: policy.DemoMerchantPremium, Resource: "GET /v1/premium-dataset",
		AmountMicros: 4_000_000, Purpose: "premium dataset",
		Nonce:     "0x" + strings.Repeat("bb", 32),
		ExpiresAt: clock.Now().Add(5 * time.Minute),
	}

	res, _ := l.Submit(ctx, intent)
	l.Decide(ctx, res.Request.ID, approval.DecideApprove, "gnanam", "")

	// Policy would still say yes — it cannot see time. The gate must not.
	clock.Advance(6 * time.Minute)

	err = l.Execute(ctx, res.Request.Intent, res.Request.ID, fund.Execute)
	if err == nil {
		t.Fatal("an expired approval executed")
	}
	if got := len(fund.Executed()); got != 0 {
		t.Fatalf("funding acted on an expired approval %d time(s) — the G7 gate is the ONLY thing enforcing expiry, and it failed", got)
	}
}
