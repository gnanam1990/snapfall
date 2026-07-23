package brain

import (
	"context"
	"reflect"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

// fakePurchaser captures the (Brain-stamped) intent and returns a configured outcome.
type fakePurchaser struct {
	gotIntent PurchaseIntent
	outcome   worker.PurchaseOutcome
}

func (f *fakePurchaser) Decide(_ context.Context, intent PurchaseIntent) (worker.PurchaseOutcome, error) {
	f.gotIntent = intent
	return f.outcome, nil
}

// buyingWorker makes one purchase and stashes the outcome for the test to inspect.
type buyingWorker struct {
	req  worker.PurchaseRequest
	got  worker.PurchaseOutcome
	done chan struct{}
}

func (w *buyingWorker) Kind() string { return "due-diligence" }
func (w *buyingWorker) Handle(ctx context.Context, _ envelope.Envelope, _ worker.Report, purchase worker.Purchase) error {
	out, err := purchase(ctx, w.req)
	w.got = out
	close(w.done)
	return err
}

// TestPurchase_StructuredReasonFlowsBackAndJobStamped: the policy decision reason reaches
// the worker INTACT (so it can adapt — AT-04), and Brain stamps the jobID/agent identity.
func TestPurchase_StructuredReasonFlowsBackAndJobStamped(t *testing.T) {
	b, _, _ := newTestBrain(t)
	fp := &fakePurchaser{outcome: worker.PurchaseOutcome{
		Decision: "DENY",
		Reason:   "per-transaction limit exceeded: 4.00 > 5.00 USDC",
		Code:     "per-tx-limit",
		Status:   "denied",
	}}
	b.SetPurchaser(fp)

	bw := &buyingWorker{
		req: worker.PurchaseRequest{
			Merchant: "api.premium.example", Resource: "GET /v1/premium-dataset",
			AmountMicros: 4_000_000, Purpose: "premium dataset",
		},
		done: make(chan struct{}),
	}
	b.mu.Lock()
	b.workers["due-diligence"] = bw
	b.mu.Unlock()

	ctx := context.Background()
	if _, err := b.HandleOwnerRequest(ctx, "job_buy", "Acme Corp"); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := b.Confirm(ctx, "job_buy", "gnanam"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	waitJob(b, "job_buy")

	// The worker can distinguish "denied by policy" from "needs approval" / "expired"
	// because the structured fields arrived intact — not one opaque error.
	if bw.got.Decision != "DENY" || bw.got.Code != "per-tx-limit" || bw.got.Reason == "" {
		t.Fatalf("structured outcome not delivered intact: %+v", bw.got)
	}
	// jobID and agent kind were stamped BY BRAIN — the worker never supplied them.
	if fp.gotIntent.JobID != "job_buy" {
		t.Fatalf("intent jobID = %q, want job_buy (Brain must stamp it)", fp.gotIntent.JobID)
	}
	if fp.gotIntent.AgentKind != "due-diligence" {
		t.Fatalf("intent agent kind = %q, want due-diligence", fp.gotIntent.AgentKind)
	}
}

// TestPurchase_WorkerCannotSpendAgainstAnotherJob is the purchase analogue of
// TestReport_CrossJobReportRefused: the capability is bound to one job by Brain's closure,
// and PurchaseRequest has NO jobID for a worker to swap — the cross-job refusal is
// structural, stronger than a runtime check.
func TestPurchase_WorkerCannotSpendAgainstAnotherJob(t *testing.T) {
	b, _, _ := newTestBrain(t)
	fp := &fakePurchaser{outcome: worker.PurchaseOutcome{Decision: "AUTO_APPROVE", Status: "approved-pending-integration"}}
	b.SetPurchaser(fp)

	// The capability minted for job_A stamps job_A no matter what the request says.
	buy := b.purchaseFor("due-diligence", "job_A")
	if _, err := buy(context.Background(), worker.PurchaseRequest{Merchant: "m", AmountMicros: 40_000}); err != nil {
		t.Fatalf("purchase: %v", err)
	}
	if fp.gotIntent.JobID != "job_A" {
		t.Fatalf("spend landed on job %q, want job_A — a worker must not reach another job's budget", fp.gotIntent.JobID)
	}

	// Structural guarantee: there is no field on PurchaseRequest for a worker to target a
	// different job. If one is ever added, this fails — the binding would no longer be
	// unforgeable.
	if _, hasJob := reflect.TypeOf(worker.PurchaseRequest{}).FieldByName("JobID"); hasJob {
		t.Fatal("PurchaseRequest gained a JobID field — a worker could now spend against another job's budget")
	}
}

// TestPurchase_NoPurchaserWiredRefuses: with no pipeline wired, a spend is refused, not
// silently dropped or (worse) executed.
func TestPurchase_NoPurchaserWiredRefuses(t *testing.T) {
	b, _, _ := newTestBrain(t)
	buy := b.purchaseFor("due-diligence", "job_x")
	if _, err := buy(context.Background(), worker.PurchaseRequest{Merchant: "m", AmountMicros: 1}); err == nil {
		t.Fatal("a purchase with no purchaser wired must be refused")
	}
}
