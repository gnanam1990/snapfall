package advancing

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/chain"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
)

const vaultRef = "0x" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// fakeLane is a funding.Submitter with a scripted outcome.
type fakeLane struct {
	out      funding.ChainOutcome
	err      error
	calldata [][]byte
}

func (f *fakeLane) Submit(_ context.Context, calldata []byte) (funding.ChainOutcome, error) {
	f.calldata = append(f.calldata, calldata)
	return f.out, f.err
}

// Success: the executed grant drives FloatPool.requestAdvance through Funding's
// treasury lane with the EXACT calldata for the intent's chain ref, and the outcome is
// durable as advance.executed with the transaction hash.
func TestAdvanceChain_SuccessRecordsExecuted(t *testing.T) {
	f, life, st, fund, done := rig(t)
	lane := &fakeLane{out: funding.ChainOutcome{Submitted: true, TxHash: "0xt1", Block: 7, GasUsed: 21000}}
	fund.SetChain(lane, nil)
	ctx := context.Background()

	req, err := f.Propose(ctx, "job_chain", vaultRef, "25.00")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := life.Decide(ctx, req.ID, approval.DecideApprove, "gnanam", "the snap"); err != nil {
		t.Fatal(err)
	}
	<-done

	if n := count(t, st, "advance.executed"); n != 1 {
		t.Fatalf("advance.executed = %d, want 1", n)
	}
	if n := count(t, st, "advance.pending_chain"); n != 0 {
		t.Fatal("a submitted advance must not also record pending_chain")
	}
	id, _ := chain.JobID32(vaultRef)
	want := chain.CalldataRequestAdvance(id)
	if len(lane.calldata) != 1 || fmt.Sprintf("%x", lane.calldata[0]) != fmt.Sprintf("%x", want) {
		t.Fatalf("calldata mismatch: got %x", lane.calldata)
	}
}

// PIN 2 at the flow level: a REVERT records advance.reverted, never advance.executed,
// and the lifecycle marks the intent consumed+failed — surfaced, not completed.
func TestAdvanceChain_RevertSurfacesNeverCompletes(t *testing.T) {
	f, life, st, fund, done := rig(t)
	lane := &fakeLane{out: funding.ChainOutcome{Submitted: true, TxHash: "0xdead", Reverted: true}}
	fund.SetChain(lane, nil)
	ctx := context.Background()

	req, err := f.Propose(ctx, "job_rev", vaultRef, "25.00")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := life.Decide(ctx, req.ID, approval.DecideApprove, "gnanam", "approve into a revert"); err != nil {
		t.Fatal(err)
	}
	<-done

	if n := count(t, st, "advance.reverted"); n != 1 {
		t.Fatalf("advance.reverted = %d, want 1", n)
	}
	if n := count(t, st, "advance.executed"); n != 0 {
		t.Fatal("a reverted advance was recorded as executed")
	}
	if n := count(t, st, "payment.failed"); n != 1 {
		t.Fatal("the lifecycle must mark the consumed intent failed")
	}
}

type fakeOracle struct{ landed bool }

func (f fakeOracle) AdvanceLanded(context.Context, string) (bool, error) { return f.landed, nil }

// PIN 1: claim written, outcome unknown (crash between submission and receipt). The
// CHAIN answers — landed becomes a recovered advance.executed; not-landed escalates to
// the owner and is never auto-resubmitted.
func TestAdvanceChain_OracleResolvesTheCrashWindow(t *testing.T) {
	for _, landed := range []bool{true, false} {
		t.Run(fmt.Sprintf("landed=%v", landed), func(t *testing.T) {
			f, life, st, _, _ := rig(t)
			ctx := context.Background()
			res, err := life.SubmitAdvance(ctx, approval.Intent{
				IntentID: "adv_crash", OrgID: "org_demo", JobID: "job_crash", AgentID: "funding",
				Kind: policy.KindAdvance, ChainRef: vaultRef, Resource: "FloatPool.requestAdvance",
				AmountMicros: 500_000, MaxAmountMicros: 500_000, Purpose: "crash window",
				Nonce: "0x" + strings.Repeat("cd", 32), ExpiresAt: time.Now().Add(5 * time.Minute),
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := life.Decide(ctx, res.Request.ID, approval.DecideApprove, "gnanam", "approved"); err != nil {
				t.Fatal(err)
			}
			// The claim lands durably; the "process dies" before any outcome is recorded.
			snap, _ := life.Snapshot(res.Request.ID)
			if err := life.Execute(ctx, snap.Intent, res.Request.ID, func(context.Context, approval.Grant) error { return nil }); err != nil {
				t.Fatal(err)
			}

			// "Restart": recover a fresh lifecycle, wire the oracle, resolve.
			life2 := approval.New(st, time.Now)
			life2.Policy = life.Policy
			life2.Spend = life.Spend
			if err := life2.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			f2 := New(life2, st, funding.New(), f.log, "org_demo", 5*time.Minute)
			f2.SetOracle(fakeOracle{landed: landed})
			n, err := f2.EscalateInterrupted(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if landed {
				if n != 0 || count(t, st, "advance.executed") != 1 {
					t.Fatalf("landed claim must recover as executed: n=%d executed=%d", n, count(t, st, "advance.executed"))
				}
			} else {
				if n != 1 || count(t, st, "advance.interrupted") != 1 || count(t, st, "advance.executed") != 0 {
					t.Fatalf("unlanded claim must escalate, never resubmit: n=%d", n)
				}
			}
		})
	}
}
