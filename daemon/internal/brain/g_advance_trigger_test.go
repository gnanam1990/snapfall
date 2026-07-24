package brain

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/advancing"
	"github.com/gnanam1990/snapfall/daemon/internal/approval"
	"github.com/gnanam1990/snapfall/daemon/internal/billing"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
	"github.com/gnanam1990/snapfall/daemon/internal/policy"
)

// The funding-observed advance trigger, seeded-row tested — it has NEVER fired for
// real (no deployment, no JobFunded rows, no vault ids; the chain gap). One proposal
// per job, ever: a decided advance is the owner's answer, and only the owner
// re-proposes.
func TestAdvanceTrigger_ObserveFundingProposesOnce(t *testing.T) {
	b, st, _ := newTestBrain(t)
	b.SetScoper(StubScoper{})
	mem, err := NewMemoryStore(filepath.Join(t.TempDir(), "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	b.memory = mem
	b.SetBilling(billing.New(st, g12Chain, nil))
	life := approval.New(st, time.Now)
	life.Policy = func() (policy.PolicyConfig, string) { return policy.DemoPolicy(), "pol_7" }
	life.Spend = func(string) policy.SpendState { return policy.SpendState{} }
	b.SetAdvanceFlow(advancing.New(life, st, funding.New(), slog.New(slog.NewTextHandler(io.Discard, nil)), "org_demo", 5*time.Minute))
	ctx := context.Background()

	if _, err := b.HandleOwnerRequest(ctx, "job_fund", "Acme Corp"); err != nil {
		t.Fatal(err)
	}
	if err := mem.Update("job_fund", func(jm *JobMemory) { jm.VaultJobID = g12Vault }); err != nil {
		t.Fatal(err)
	}
	// A second job with no vault id: never proposed for.
	if _, err := b.HandleOwnerRequest(ctx, "job_unmapped", "Beta Corp"); err != nil {
		t.Fatal(err)
	}

	// Nothing funded on chain yet: nothing proposed.
	if n, err := b.ObserveFundingOnce(ctx); err != nil || n != 0 {
		t.Fatalf("before funding: n=%d err=%v", n, err)
	}

	seedChainRow(t, st, "JobFunded", g12Vault, "0x0f", 100, map[string]string{"amountAtomic": "25000000"})

	if n, err := b.ObserveFundingOnce(ctx); err != nil || n != 1 {
		t.Fatalf("observe: n=%d err=%v", n, err)
	}
	pending := life.PendingRequests()
	if len(pending) != 1 || pending[0].JobID != "job_fund" || pending[0].Intent.Kind != policy.KindAdvance {
		t.Fatalf("pending after observation: %+v, want one advance for job_fund", pending)
	}
	if pending[0].Intent.AmountMicros != 12_500_000 {
		t.Fatalf("principal %d, want 12500000 (50%% of the 25.00 quote)", pending[0].Intent.AmountMicros)
	}

	// Idempotent: the watcher proposes once, ever — even after a decision.
	if n, _ := b.ObserveFundingOnce(ctx); n != 0 {
		t.Fatalf("re-observe proposed again: %d", n)
	}
	if _, err := life.Decide(ctx, pending[0].ID, approval.DecideReject, "gnanam", "not yet"); err != nil {
		t.Fatal(err)
	}
	if n, _ := b.ObserveFundingOnce(ctx); n != 0 {
		t.Fatal("a rejected advance was re-proposed by the watcher — the owner's answer must stand")
	}
}

// The wiring law: the brain package invokes the advance flow from EXACTLY ONE site
// (ProposeAdvance) — the observer routes through it, same technique as the dispatch
// and billing chokepoints. Doc comments must not carry the token.
func TestAdvanceTrigger_SingleProposeSite(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	token := "flow.Propose(" // the advancing.Flow invocation shape; unique in this package
	count := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		count += strings.Count(string(src), token)
	}
	if count != 1 {
		t.Fatalf("advance-flow invocation sites in brain: %d, want exactly 1 (ProposeAdvance)", count)
	}
}
