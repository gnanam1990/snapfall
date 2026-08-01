package budget

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// ── harness ────────────────────────────────────────────────────────────────────────────
//
// Every test runs against a REAL store.Store on a temp database, the way the store's own
// tests do. The ledger's whole claim is that it is a fold of the durable log, so a fake
// store would test the opposite of the property that matters.

func openTemp(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// testClock is injected time (mutex-guarded because the JobGate test reads it from a
// second goroutine under -race).
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// base is a fixed mid-day instant, deliberately NOT near a UTC midnight so an
// off-by-a-window bug cannot pass by luck.
var base = time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)

func newLedger(t *testing.T) (*Ledger, *store.Store, *testClock) {
	t.Helper()
	st := openTemp(t)
	clk := &testClock{now: base}
	return New(st, clk.Now), st, clk
}

// hold builds a well-formed hold; tests override only what they are about.
func hold(requestID, jobID string, micros int64, expiresAt time.Time) Hold {
	return Hold{
		RequestID:     requestID,
		IntentID:      "int_" + requestID,
		JobID:         jobID,
		OrgID:         "org_1",
		ReserveMicros: micros,
		PaymentID:     "pay_" + requestID,
		Nonce:         "nonce_" + requestID,
		Merchant:      "api.research-data.example",
		PayTo:         "0x000000000000000000000000000000000000dEaD",
		ExpiresAt:     expiresAt,
	}
}

// claim appends the approval lifecycle's write-ahead execution claim by hand, with the
// exact shape lifecycle.go writes: EntityID = jobID, payload keyed by request_id. The
// ledger must never write this kind itself, so tests cannot go through the ledger.
func claim(t *testing.T, st *store.Store, requestID, jobID string) {
	t.Helper()
	_, err := st.Append(context.Background(), store.Event{
		Kind: kindPaymentExecuting, EntityID: jobID, Actor: "approval",
		Payload: map[string]any{"request_id": requestID, "intent_hash": "0xdeadbeef"},
	})
	if err != nil {
		t.Fatalf("appending %s: %v", kindPaymentExecuting, err)
	}
}

func countEvents(t *testing.T, st *store.Store, kind string) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM events WHERE kind = ?`, kind).Scan(&n); err != nil {
		t.Fatalf("counting %s events: %v", kind, err)
	}
	return n
}

// payloadView is every payload key the assertions read.
type payloadView struct {
	RequestID       string `json:"request_id"`
	JobID           string `json:"job_id"`
	OrgID           string `json:"org_id"`
	ReservedMicros  int64  `json:"reserved_micros"`
	CommittedMicros int64  `json:"committed_micros"`
	ReleasedMicros  int64  `json:"released_micros"`
	PaymentID       string `json:"payment_id"`
	ReceiptHash     string `json:"receipt_hash"`
	State           string `json:"state"`
	Code            string `json:"code"`
	PreSign         bool   `json:"pre_sign"`
	ExpiresAt       int64  `json:"expires_at"`
	HeldSince       int64  `json:"held_since"`
	HeldMicros      int64  `json:"held_micros"`
}

func lastPayload(t *testing.T, st *store.Store, kind string) payloadView {
	t.Helper()
	var raw string
	if err := st.DB().QueryRowContext(context.Background(),
		`SELECT payload_json FROM events WHERE kind = ? ORDER BY seq DESC LIMIT 1`, kind).Scan(&raw); err != nil {
		t.Fatalf("reading last %s payload: %v", kind, err)
	}
	var p payloadView
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("unmarshalling %s payload: %v", kind, err)
	}
	return p
}

// foldState is the ledger's complete internal state, flattened for comparison. The
// idempotence test compares this, not just the derived sums: two different folds can
// agree on a total and disagree about which holds are open.
type foldState struct {
	Entries map[string]entrySnapshot
	JobOrg  map[string]string
}

type entrySnapshot struct {
	Hold      Hold
	Reserved  bool
	Claimed   bool
	ClosedBy  string
	Committed int64
}

func snapshot(l *Ledger) foldState {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := foldState{Entries: make(map[string]entrySnapshot, len(l.entries)), JobOrg: make(map[string]string, len(l.jobOrg))}
	for id, e := range l.entries {
		out.Entries[id] = entrySnapshot{
			Hold: e.hold, Reserved: e.reserved, Claimed: e.claimed,
			ClosedBy: e.closedBy, Committed: e.committedMicros,
		}
	}
	for j, o := range l.jobOrg {
		out.JobOrg[j] = o
	}
	return out
}

// withoutClaims copies a snapshot with every claim flag cleared, so a comparison can
// ignore the one fact only a fold can know.
func withoutClaims(fs foldState) foldState {
	out := foldState{Entries: make(map[string]entrySnapshot, len(fs.Entries)), JobOrg: fs.JobOrg}
	for id, e := range fs.Entries {
		e.Claimed = false
		out.Entries[id] = e
	}
	return out
}

// ── reserve ────────────────────────────────────────────────────────────────────────────

// The hold must be visible to the policy engine the instant Reserve returns: that is the
// whole point of holding at intake rather than at owner-approval. Two concurrent intents
// on one job must not both evaluate against zero.
func TestReserve_HoldIsImmediatelyVisibleToTheEngine(t *testing.T) {
	ctx := context.Background()
	led, st, _ := newLedger(t)

	if err := led.Reserve(ctx, hold("apr_a", "job_104", 40_000, base.Add(5*time.Minute))); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	got := led.Spend("job_104")
	if got.JobCommittedMicros != 40_000 {
		t.Errorf("JobCommittedMicros = %d, want 40000", got.JobCommittedMicros)
	}
	if got.DailySpentMicros != 40_000 {
		t.Errorf("DailySpentMicros = %d, want 40000", got.DailySpentMicros)
	}
	if n := countEvents(t, st, KindReserved); n != 1 {
		t.Errorf("%s events = %d, want 1", KindReserved, n)
	}

	// The durable record carries everything the reclaim paths need, at second precision.
	p := lastPayload(t, st, KindReserved)
	if p.RequestID != "apr_a" || p.JobID != "job_104" || p.OrgID != "org_1" {
		t.Errorf("reserve payload lost identity: %+v", p)
	}
	if p.ReservedMicros != 40_000 {
		t.Errorf("reserved_micros = %d, want 40000", p.ReservedMicros)
	}
	if p.PaymentID != "pay_apr_a" {
		t.Errorf("payment_id = %q, want the PRECOMPUTED id (the boot reconciler cannot reach status without it)", p.PaymentID)
	}
	if p.ExpiresAt != base.Add(5*time.Minute).Unix() {
		t.Errorf("expires_at = %d, want %d (unix SECONDS)", p.ExpiresAt, base.Add(5*time.Minute).Unix())
	}

	// A job with no holds sees zero, not another job's spend.
	if other := led.Spend("job_999"); other.JobCommittedMicros != 0 {
		t.Errorf("unrelated job JobCommittedMicros = %d, want 0", other.JobCommittedMicros)
	}
}

// Idempotent by request id: a retried intake must not double-hold, and must not double-log.
func TestReserve_IsIdempotentByRequestID(t *testing.T) {
	ctx := context.Background()
	led, st, _ := newLedger(t)
	h := hold("apr_a", "job_104", 4_000_000, base.Add(5*time.Minute))

	if err := led.Reserve(ctx, h); err != nil {
		t.Fatalf("first Reserve: %v", err)
	}
	if err := led.Reserve(ctx, h); err != nil {
		t.Fatalf("repeat Reserve must be a recorded no-op, got: %v", err)
	}
	if n := countEvents(t, st, KindReserved); n != 1 {
		t.Errorf("%s events = %d, want 1 (a retry must not append a second reservation)", KindReserved, n)
	}
	if got := led.Spend("job_104").JobCommittedMicros; got != 4_000_000 {
		t.Errorf("JobCommittedMicros = %d, want 4000000 (the hold was counted twice)", got)
	}
}

// A hold with no expiry could never be proven dead, so Sweep could never reclaim it: it
// would permanently shrink the budget. Refuse it at the door instead.
func TestReserve_RefusesAHoldItCouldNeverReclaim(t *testing.T) {
	ctx := context.Background()
	led, st, _ := newLedger(t)

	cases := map[string]Hold{
		"no request id": hold("", "job_104", 40_000, base.Add(time.Minute)),
		"no job id":     hold("apr_a", "", 40_000, base.Add(time.Minute)),
		"zero micros":   hold("apr_a", "job_104", 0, base.Add(time.Minute)),
		"negative":      hold("apr_a", "job_104", -1, base.Add(time.Minute)),
		"no expiry":     hold("apr_a", "job_104", 40_000, time.Time{}),
	}
	for name, h := range cases {
		if err := led.Reserve(ctx, h); !errors.Is(err, ErrInvalidHold) {
			t.Errorf("%s: err = %v, want ErrInvalidHold", name, err)
		}
	}
	if n := countEvents(t, st, KindReserved); n != 0 {
		t.Errorf("%s events = %d, want 0 (a refused hold must leave no record)", KindReserved, n)
	}
}

// A closed hold cannot be reopened: the fold is set-based and first close wins, so
// returning nil here would report a hold that does not exist.
func TestReserve_RefusesToReopenAClosedHold(t *testing.T) {
	ctx := context.Background()
	led, _, _ := newLedger(t)
	h := hold("apr_a", "job_104", 40_000, base.Add(5*time.Minute))

	if err := led.Reserve(ctx, h); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := led.Release(ctx, "apr_a", "pre-sign refusal", "MERCHANT_CHANGED", true); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := led.Reserve(ctx, h); !errors.Is(err, ErrHoldClosed) {
		t.Errorf("re-Reserve after release: err = %v, want ErrHoldClosed", err)
	}
}

// ── commit ─────────────────────────────────────────────────────────────────────────────

func TestCommit_ConvertsTheHoldAndReleasesTheRemainder(t *testing.T) {
	ctx := context.Background()
	led, st, _ := newLedger(t)

	if err := led.Reserve(ctx, hold("apr_a", "job_104", 4_000_000, base.Add(5*time.Minute))); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := led.Commit(ctx, "apr_a", 3_000_000, "pay_apr_a", "0xauthnonce", "DELIVERED"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// The hold is gone; what actually moved is what counts, against the job and the day.
	got := led.Spend("job_104")
	if got.JobCommittedMicros != 3_000_000 {
		t.Errorf("JobCommittedMicros = %d, want 3000000 (paid, not reserved)", got.JobCommittedMicros)
	}
	if got.DailySpentMicros != 3_000_000 {
		t.Errorf("DailySpentMicros = %d, want 3000000", got.DailySpentMicros)
	}

	p := lastPayload(t, st, KindCommitted)
	if p.CommittedMicros != 3_000_000 || p.ReleasedMicros != 1_000_000 {
		t.Errorf("committed/released = %d/%d, want 3000000/1000000", p.CommittedMicros, p.ReleasedMicros)
	}
	if p.ReceiptHash != "0xauthnonce" || p.State != "DELIVERED" || p.PaymentID != "pay_apr_a" {
		t.Errorf("commit payload lost the receipt join key or the state: %+v", p)
	}
	if n := countEvents(t, st, KindReleased); n != 0 {
		t.Errorf("%s events = %d, want 0 (the remainder is released INSIDE the commit)", KindReleased, n)
	}
}

// W4: the sidecar reported DELIVERED but budget.committed had not been written when the
// daemon died. The boot reconciler re-commits from the sidecar's durable record, so a
// duplicate commit must be a no-op rather than double-counting the spend.
func TestCommit_DuplicateIsANoOp(t *testing.T) {
	ctx := context.Background()
	led, st, _ := newLedger(t)

	if err := led.Reserve(ctx, hold("apr_a", "job_104", 4_000_000, base.Add(5*time.Minute))); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := led.Commit(ctx, "apr_a", 3_000_000, "pay_apr_a", "0xauthnonce", "DELIVERED"); err != nil {
			t.Fatalf("Commit %d: %v", i, err)
		}
	}
	if n := countEvents(t, st, KindCommitted); n != 1 {
		t.Errorf("%s events = %d, want 1", KindCommitted, n)
	}
	if got := led.Spend("job_104").JobCommittedMicros; got != 3_000_000 {
		t.Errorf("JobCommittedMicros = %d, want 3000000 (the spend was counted more than once)", got)
	}
}

// Money moving after the ledger declared the hold dead is the one case that must be loud:
// no fabricated release, no silently dropped commit.
func TestCommit_AfterReleaseIsLoud(t *testing.T) {
	ctx := context.Background()
	led, st, _ := newLedger(t)

	if err := led.Reserve(ctx, hold("apr_a", "job_104", 40_000, base.Add(5*time.Minute))); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := led.Release(ctx, "apr_a", "swept", CodeExpiredUnattempted, true); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := led.Commit(ctx, "apr_a", 40_000, "pay_apr_a", "0xauthnonce", "DELIVERED"); !errors.Is(err, ErrCommitAfterRelease) {
		t.Errorf("Commit after Release: err = %v, want ErrCommitAfterRelease", err)
	}
	if n := countEvents(t, st, KindCommitted); n != 0 {
		t.Errorf("%s events = %d, want 0 (the divergence is escalated, not recorded as truth)", KindCommitted, n)
	}
}

func TestCommit_UnknownRequestIsRefused(t *testing.T) {
	led, _, _ := newLedger(t)
	if err := led.Commit(context.Background(), "apr_missing", 1, "p", "r", "DELIVERED"); !errors.Is(err, ErrNoSuchHold) {
		t.Errorf("err = %v, want ErrNoSuchHold", err)
	}
}

// ── release ────────────────────────────────────────────────────────────────────────────

// §2.3: the fold is SET-based, not counter-based, precisely so a duplicate release cannot
// refund the budget twice.
func TestRelease_DuplicateIsAHarmlessNoOp(t *testing.T) {
	ctx := context.Background()
	led, st, _ := newLedger(t)

	if err := led.Reserve(ctx, hold("apr_a", "job_104", 4_000_000, base.Add(5*time.Minute))); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := led.Reserve(ctx, hold("apr_b", "job_104", 1_000_000, base.Add(5*time.Minute))); err != nil {
		t.Fatalf("Reserve b: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := led.Release(ctx, "apr_a", "owner rejected", "REJECTED", true); err != nil {
			t.Fatalf("Release %d: %v", i, err)
		}
	}
	if n := countEvents(t, st, KindReleased); n != 1 {
		t.Errorf("%s events = %d, want 1", KindReleased, n)
	}
	// Only apr_a came back. A counter-based fold would have driven this negative.
	if got := led.Spend("job_104").JobCommittedMicros; got != 1_000_000 {
		t.Errorf("JobCommittedMicros = %d, want 1000000", got)
	}

	p := lastPayload(t, st, KindReleased)
	if p.ReleasedMicros != 4_000_000 || !p.PreSign || p.Code != "REJECTED" {
		t.Errorf("release payload = %+v, want the full 4000000 released, pre_sign true, code REJECTED", p)
	}
}

func TestRelease_UnknownRequestIsRefused(t *testing.T) {
	led, _, _ := newLedger(t)
	if err := led.Release(context.Background(), "apr_missing", "r", "C", true); !errors.Is(err, ErrNoSuchHold) {
		t.Errorf("err = %v, want ErrNoSuchHold", err)
	}
}

// ── the fold ───────────────────────────────────────────────────────────────────────────

// The hold is a folded FACT, not memory. Construct, act, throw the ledger away, rebuild
// from the same log, and assert the state is identical down to which holds are open and
// which are claimed. Then fold the same log a second time and assert nothing shifted:
// Recover must be idempotent, or a double boot would change the budget.
func TestFold_IsIdempotentOverTheSameLog(t *testing.T) {
	ctx := context.Background()
	led, st, clk := newLedger(t)

	if err := led.Reserve(ctx, hold("apr_a", "job_104", 4_000_000, base.Add(5*time.Minute))); err != nil {
		t.Fatalf("Reserve a: %v", err)
	}
	clk.Set(base.Add(time.Second)) // distinct ReservedAt values, so ordering is testable
	if err := led.Reserve(ctx, hold("apr_b", "job_104", 60_000, base.Add(5*time.Minute))); err != nil {
		t.Fatalf("Reserve b: %v", err)
	}
	clk.Set(base.Add(2 * time.Second))
	if err := led.Reserve(ctx, hold("apr_c", "job_205", 100_000, base.Add(5*time.Minute))); err != nil {
		t.Fatalf("Reserve c: %v", err)
	}
	if err := led.Commit(ctx, "apr_b", 60_000, "pay_apr_b", "0xauth_b", "DELIVERED"); err != nil {
		t.Fatalf("Commit b: %v", err)
	}
	if err := led.Release(ctx, "apr_c", "pre-sign refusal", "MERCHANT_CHANGED", true); err != nil {
		t.Fatalf("Release c: %v", err)
	}
	claim(t, st, "apr_a", "job_104") // apr_a may be signed: it is unresolved, never swept

	// A fresh ledger over the same store must reach the same place. This is the restart.
	rebuilt := New(st, clk.Now)
	if err := rebuilt.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	// Compare the state the live ledger BUILT against the state the fold RECONSTRUCTED,
	// setting the claim flag aside: the ledger never writes payment.executing, so a live
	// ledger cannot know about a claim until it folds or sweeps. Everything else must be
	// identical, down to the millisecond on ReservedAt.
	live, folded := snapshot(led), snapshot(rebuilt)
	if !reflect.DeepEqual(withoutClaims(live), withoutClaims(folded)) {
		t.Errorf("a rebuilt ledger disagrees with the live one:\n live   = %+v\n folded = %+v", live, folded)
	}
	if live.Entries["apr_a"].Claimed {
		t.Error("the live ledger claims to know about a payment.executing it never wrote")
	}
	if !folded.Entries["apr_a"].Claimed {
		t.Error("the fold did not pick up the lifecycle's payment.executing claim: apr_a would be swept")
	}

	// Idempotence proper: fold the same log again, twice.
	for i := 0; i < 2; i++ {
		if err := rebuilt.Recover(ctx); err != nil {
			t.Fatalf("Recover replay %d: %v", i, err)
		}
		if again := snapshot(rebuilt); !reflect.DeepEqual(folded, again) {
			t.Fatalf("Recover is not idempotent at replay %d:\n first = %+v\n again = %+v", i, folded, again)
		}
	}

	// And the derived numbers: apr_a still held (4.00), apr_b settled (0.06),
	// apr_c released (nothing).
	if got := rebuilt.Spend("job_104"); got.JobCommittedMicros != 4_060_000 {
		t.Errorf("job_104 JobCommittedMicros = %d, want 4060000 (open hold + settled spend)", got.JobCommittedMicros)
	}
	if got := rebuilt.Spend("job_205"); got.JobCommittedMicros != 0 {
		t.Errorf("job_205 JobCommittedMicros = %d, want 0 (its only hold was released)", got.JobCommittedMicros)
	}
	// The daily figure is org-wide, so it sums across both jobs.
	if got := rebuilt.Spend("job_205").DailySpentMicros; got != 4_060_000 {
		t.Errorf("DailySpentMicros = %d, want 4060000 (org-wide, both jobs)", got)
	}

	unresolved := rebuilt.Unresolved()
	if len(unresolved) != 1 || unresolved[0].RequestID != "apr_a" {
		t.Errorf("Unresolved() = %+v, want exactly the claimed open hold apr_a", unresolved)
	}
	// Recovery must reproduce the persisted identity, not just the amount.
	if unresolved[0].PaymentID != "pay_apr_a" || unresolved[0].Nonce != "nonce_apr_a" {
		t.Errorf("recovered hold lost the payment id or nonce: %+v", unresolved[0])
	}
	if !unresolved[0].ExpiresAt.Equal(base.Add(5 * time.Minute)) {
		t.Errorf("recovered ExpiresAt = %v, want %v", unresolved[0].ExpiresAt, base.Add(5*time.Minute))
	}
}

// A committed event whose reserve is missing (a truncated or hand-edited log) must not
// invent a hold, and must not crash the fold.
func TestFold_ToleratesACloseWithoutItsReserve(t *testing.T) {
	ctx := context.Background()
	led, st, _ := newLedger(t)

	if _, err := st.Append(ctx, store.Event{Kind: KindCommitted, EntityID: "job_104", Actor: actor,
		Payload: map[string]any{"request_id": "apr_ghost", "job_id": "job_104", "committed_micros": 1_000_000}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := led.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got := led.Spend("job_104"); got.JobCommittedMicros != 0 || got.DailySpentMicros != 0 {
		t.Errorf("spend = %+v, want zero: a close with no reserve names no hold", got)
	}
	if err := led.Commit(ctx, "apr_ghost", 1, "p", "r", "DELIVERED"); !errors.Is(err, ErrNoSuchHold) {
		t.Errorf("Commit on a reserve-less request: err = %v, want ErrNoSuchHold", err)
	}
}

// ── the daily window ───────────────────────────────────────────────────────────────────

// The window is the UTC calendar day, keyed on the RESERVE event's timestamp (the
// SpendState caller contract). Yesterday's hold still counts against the job budget
// forever, but must drop out of today's cap.
func TestSpend_DailyWindowIsTheUTCDayOfTheReserve(t *testing.T) {
	ctx := context.Background()
	led, _, clk := newLedger(t)

	yesterday := time.Date(2026, 7, 24, 23, 30, 0, 0, time.UTC)
	clk.Set(yesterday)
	if err := led.Reserve(ctx, hold("apr_old", "job_104", 1_000_000, yesterday.Add(24*time.Hour))); err != nil {
		t.Fatalf("Reserve old: %v", err)
	}

	clk.Set(base) // 2026-07-25 12:00 UTC: a new window
	if err := led.Reserve(ctx, hold("apr_new", "job_104", 40_000, base.Add(5*time.Minute))); err != nil {
		t.Fatalf("Reserve new: %v", err)
	}

	got := led.Spend("job_104")
	if got.JobCommittedMicros != 1_040_000 {
		t.Errorf("JobCommittedMicros = %d, want 1040000 (the job budget is not windowed)", got.JobCommittedMicros)
	}
	if got.DailySpentMicros != 40_000 {
		t.Errorf("DailySpentMicros = %d, want 40000 (yesterday's hold is outside today's window)", got.DailySpentMicros)
	}
}

// The daily cap is org-wide; the job budget is not. A second org's spend must not eat
// this org's cap.
func TestSpend_DailyIsOrgScopedAndJobIsJobScoped(t *testing.T) {
	ctx := context.Background()
	led, _, _ := newLedger(t)

	mine := hold("apr_a", "job_104", 40_000, base.Add(5*time.Minute))
	sibling := hold("apr_b", "job_205", 60_000, base.Add(5*time.Minute)) // same org, other job
	stranger := hold("apr_c", "job_777", 5_000_000, base.Add(5*time.Minute))
	stranger.OrgID = "org_other"

	for _, h := range []Hold{mine, sibling, stranger} {
		if err := led.Reserve(ctx, h); err != nil {
			t.Fatalf("Reserve %s: %v", h.RequestID, err)
		}
	}

	got := led.Spend("job_104")
	if got.JobCommittedMicros != 40_000 {
		t.Errorf("JobCommittedMicros = %d, want 40000 (this job only)", got.JobCommittedMicros)
	}
	if got.DailySpentMicros != 100_000 {
		t.Errorf("DailySpentMicros = %d, want 100000 (org_1's two jobs, not org_other's)", got.DailySpentMicros)
	}
}

// ── sweep: FR-PAY-007's missing half ───────────────────────────────────────────────────

// A hold is swept only when it is PROVABLY dead. An expired hold with a payment.executing
// claim may already be SIGNED, and a signed authorization is a bearer instrument: it must
// stay held and be handed to the reconciler, never released by machinery.
func TestSweep_ReleasesExpiredUnattempted_ButNeverAClaimedHold(t *testing.T) {
	ctx := context.Background()
	led, st, _ := newLedger(t)

	expiry := base.Add(time.Minute)
	if err := led.Reserve(ctx, hold("apr_unattempted", "job_104", 4_000_000, expiry)); err != nil {
		t.Fatalf("Reserve unattempted: %v", err)
	}
	if err := led.Reserve(ctx, hold("apr_claimed", "job_104", 60_000, expiry)); err != nil {
		t.Fatalf("Reserve claimed: %v", err)
	}
	// The lifecycle claimed execution for one of them AFTER the ledger's last fold, which
	// is the case a memory-only claim view would get wrong.
	claim(t, st, "apr_claimed", "job_104")

	now := expiry.Add(time.Second) // both expiries have passed
	n, err := led.Sweep(ctx, now)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("Sweep released %d holds, want exactly 1 (the unattempted one)", n)
	}

	p := lastPayload(t, st, KindReleased)
	if p.RequestID != "apr_unattempted" {
		t.Fatalf("Sweep released %q; the CLAIMED hold must never be released", p.RequestID)
	}
	if p.ReleasedMicros != 4_000_000 || !p.PreSign || p.Code != CodeExpiredUnattempted {
		t.Errorf("sweep release payload = %+v, want 4000000 released, pre_sign true, code %s", p, CodeExpiredUnattempted)
	}

	// The claimed hold still pins its budget, and is exactly what the reconciler polls.
	if got := led.Spend("job_104").JobCommittedMicros; got != 60_000 {
		t.Errorf("JobCommittedMicros = %d, want 60000 (only the claimed hold survives)", got)
	}
	unresolved := led.Unresolved()
	if len(unresolved) != 1 || unresolved[0].RequestID != "apr_claimed" {
		t.Fatalf("Unresolved() = %+v, want the claimed hold apr_claimed", unresolved)
	}
	if unresolved[0].PaymentID != "pay_apr_claimed" {
		t.Errorf("unresolved hold payment_id = %q, want the precomputed id the reconciler polls", unresolved[0].PaymentID)
	}

	// Sweeping again finds nothing and appends nothing.
	n2, err := led.Sweep(ctx, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("second Sweep: %v", err)
	}
	if n2 != 0 {
		t.Errorf("second Sweep released %d, want 0", n2)
	}
	if got := countEvents(t, st, KindReleased); got != 1 {
		t.Errorf("%s events = %d, want 1", KindReleased, got)
	}
}

// Before expiry the hold correctly STANDS: the owner may still approve it. Sweeping early
// would be exactly the double-spend window holding at intake exists to close.
func TestSweep_LeavesAnUnexpiredHoldAlone(t *testing.T) {
	ctx := context.Background()
	led, st, _ := newLedger(t)

	expiry := base.Add(5 * time.Minute)
	if err := led.Reserve(ctx, hold("apr_a", "job_104", 4_000_000, expiry)); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	n, err := led.Sweep(ctx, expiry.Add(-time.Second))
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 0 {
		t.Errorf("Sweep released %d before expiry, want 0", n)
	}
	if got := countEvents(t, st, KindReleased); got != 0 {
		t.Errorf("%s events = %d, want 0", KindReleased, got)
	}

	// At the expiry instant it IS dead: expired means NOT before ExpiresAt, matching the
	// lifecycle's own test, so there is no one-second window where a hold is neither
	// executable nor reclaimable.
	n, err = led.Sweep(ctx, expiry)
	if err != nil {
		t.Fatalf("Sweep at expiry: %v", err)
	}
	if n != 1 {
		t.Errorf("Sweep at the expiry instant released %d, want 1", n)
	}
}

// A closed hold is never re-swept, whatever the clock says.
func TestSweep_IgnoresClosedHolds(t *testing.T) {
	ctx := context.Background()
	led, st, _ := newLedger(t)

	expiry := base.Add(time.Minute)
	if err := led.Reserve(ctx, hold("apr_a", "job_104", 40_000, expiry)); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if err := led.Commit(ctx, "apr_a", 40_000, "pay_apr_a", "0xauth", "DELIVERED"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	n, err := led.Sweep(ctx, expiry.Add(time.Hour))
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 0 {
		t.Errorf("Sweep released %d, want 0 (settled spend is not a hold)", n)
	}
	if got := countEvents(t, st, KindReleased); got != 0 {
		t.Errorf("%s events = %d, want 0", KindReleased, got)
	}
}

// ── escalation ─────────────────────────────────────────────────────────────────────────

// Nothing in the sidecar ever leaves RECONCILING, so a post-sign hold can sit forever.
// StalledSince surfaces it for the owner; it is still not released.
func TestStalledSince_YieldsOnlyClaimedHoldsPastTheDeadline(t *testing.T) {
	ctx := context.Background()
	led, st, _ := newLedger(t)

	if err := led.Reserve(ctx, hold("apr_stalled", "job_104", 4_000_000, base.Add(5*time.Minute))); err != nil {
		t.Fatalf("Reserve stalled: %v", err)
	}
	if err := led.Reserve(ctx, hold("apr_unclaimed", "job_104", 40_000, base.Add(5*time.Minute))); err != nil {
		t.Fatalf("Reserve unclaimed: %v", err)
	}
	claim(t, st, "apr_stalled", "job_104")
	if err := led.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	if got := led.StalledSince(24*time.Hour, base.Add(23*time.Hour)); len(got) != 0 {
		t.Errorf("StalledSince before the deadline = %+v, want none", got)
	}
	got := led.StalledSince(24*time.Hour, base.Add(25*time.Hour))
	if len(got) != 1 || got[0].RequestID != "apr_stalled" {
		t.Fatalf("StalledSince = %+v, want exactly the claimed hold apr_stalled", got)
	}

	if err := led.NoteReconcileStalled(ctx, got[0], "RECONCILING"); err != nil {
		t.Fatalf("NoteReconcileStalled: %v", err)
	}
	p := lastPayload(t, st, KindReconcileStalled)
	if p.RequestID != "apr_stalled" || p.HeldMicros != 4_000_000 || p.State != "RECONCILING" {
		t.Errorf("stall record = %+v, want apr_stalled / 4000000 / RECONCILING", p)
	}
	if p.HeldSince != base.Unix() {
		t.Errorf("held_since = %d, want %d", p.HeldSince, base.Unix())
	}

	// A record, not a transition: the hold is STILL held.
	if got := led.Spend("job_104").JobCommittedMicros; got != 4_040_000 {
		t.Errorf("JobCommittedMicros = %d, want 4040000 (a stall record releases nothing)", got)
	}
	if n := countEvents(t, st, KindReleased); n != 0 {
		t.Errorf("%s events = %d, want 0 (a post-sign hold is never auto-released)", KindReleased, n)
	}
}

func TestNoteChainDivergence_RecordsBothFigures(t *testing.T) {
	ctx := context.Background()
	led, st, _ := newLedger(t)

	if err := led.NoteChainDivergence(ctx, "job_104", "1", 6_000_000, 5_000_000); err != nil {
		t.Fatalf("NoteChainDivergence: %v", err)
	}
	var raw, entity string
	if err := st.DB().QueryRowContext(ctx,
		`SELECT payload_json, entity_id FROM events WHERE kind = ?`, KindChainDivergence).Scan(&raw, &entity); err != nil {
		t.Fatalf("reading divergence event: %v", err)
	}
	if entity != "job_104" {
		t.Errorf("entity_id = %q, want the job id (every ledger event is job-keyed)", entity)
	}
	var p struct {
		ChainMicros  int64  `json:"chain_budget_micros"`
		DaemonMicros int64  `json:"daemon_budget_micros"`
		VaultJobID   string `json:"vault_job_id"`
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("unmarshalling divergence payload: %v", err)
	}
	if p.ChainMicros != 6_000_000 || p.DaemonMicros != 5_000_000 || p.VaultJobID != "1" {
		t.Errorf("divergence payload = %+v, want both figures and the vault job id", p)
	}

	// It changes no state: the two budgets are deliberately left unreconciled.
	if got := led.Spend("job_104"); got.JobCommittedMicros != 0 {
		t.Errorf("spend = %+v, want zero", got)
	}
}

// ── the intake gate ────────────────────────────────────────────────────────────────────

// The Spend read, the policy evaluation and the hold must be ONE critical section per
// job, or two concurrent intents both evaluate against the same unheld state.
func TestJobGate_SerializesOneJobAndNotTheOthers(t *testing.T) {
	led, _, _ := newLedger(t)

	release := led.JobGate("job_104")

	// A different job must not block behind it.
	other := make(chan struct{})
	go func() {
		led.JobGate("job_205")()
		close(other)
	}()
	select {
	case <-other:
	case <-time.After(2 * time.Second):
		t.Fatal("a gate on job_205 blocked behind job_104: intake is serialized globally, not per job")
	}

	// The same job must.
	same := make(chan struct{})
	go func() {
		led.JobGate("job_104")()
		close(same)
	}()
	select {
	case <-same:
		t.Fatal("two intakes on job_104 ran concurrently: the hold is not atomic with the spend read")
	case <-time.After(50 * time.Millisecond):
	}

	release()
	release() // idempotent: a double release must not unlock a gate someone else holds

	select {
	case <-same:
	case <-time.After(2 * time.Second):
		t.Fatal("releasing the gate did not hand it over")
	}

	// The gate map does not grow with the job count.
	led.gmu.Lock()
	n := len(led.gates)
	led.gmu.Unlock()
	if n != 0 {
		t.Errorf("gates left behind = %d, want 0", n)
	}
}
