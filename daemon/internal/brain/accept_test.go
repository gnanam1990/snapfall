package brain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gnanam1990/snapfall/daemon/internal/freeze"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
)

// acceptRig: a job driven to delivery_ready state (directly — QA's path to that stage
// is pinned elsewhere; here the subject is the credential and the Accept transition).
func acceptRig(t *testing.T) (*Brain, *store.Store, string) {
	t.Helper()
	b, st, _ := newTestBrain(t)
	b.SetScoper(StubScoper{})
	ctx := context.Background()
	if _, err := b.HandleOwnerRequest(ctx, "job_acc", "Acme Corp"); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	b.jobs["job_acc"].Stage = StageDeliveryReady
	b.mu.Unlock()
	if err := b.memory.Update("job_acc", func(jm *JobMemory) { jm.Stage = string(StageDeliveryReady) }); err != nil {
		t.Fatal(err)
	}
	return b, st, "job_acc"
}

func countEvents(t *testing.T, st *store.Store, kind, jobID string) int {
	t.Helper()
	var n int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE kind=? AND entity_id=?`, kind, jobID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Minting needs something to accept: a job before delivery_ready refuses.
func TestAccept_MintRequiresDeliveryReady(t *testing.T) {
	b, _, _ := newTestBrain(t)
	b.SetScoper(StubScoper{})
	if _, err := b.HandleOwnerRequest(context.Background(), "job_early", "Acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.MintAcceptCredential(context.Background(), "job_early"); !errors.Is(err, ErrNotDeliveryReady) {
		t.Fatalf("mint before delivery_ready: %v, want ErrNotDeliveryReady", err)
	}
}

// The owner-token lesson, applied in advance: the plaintext exists ONCE, in the mint
// return — the memory file holds only the hash, and no event payload ever carries the
// token. Verification is constant-time against the hash; re-minting rotates.
func TestAccept_PlaintextNeverStoredAndRotationKills(t *testing.T) {
	b, st, jobID := acceptRig(t)
	ctx := context.Background()

	tok1, err := b.MintAcceptCredential(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok1, "act_") || len(tok1) != 4+64 {
		t.Fatalf("token shape: %q", tok1)
	}
	if !b.VerifyAcceptCredential(jobID, tok1) {
		t.Fatal("freshly minted credential must verify")
	}
	if b.VerifyAcceptCredential(jobID, "act_"+strings.Repeat("00", 32)) {
		t.Fatal("wrong credential verified")
	}

	// The plaintext appears NOWHERE durable: not the memory file, not any event.
	memRaw, err := os.ReadFile(filepath.Join(b.memory.dir, jobID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(memRaw), tok1) {
		t.Fatal("plaintext credential found in the memory file")
	}
	var withToken int
	if err := st.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE payload_json LIKE ?`, "%"+tok1+"%").Scan(&withToken); err != nil {
		t.Fatal(err)
	}
	if withToken != 0 {
		t.Fatal("plaintext credential found in the durable event log")
	}

	// Rotation: a second mint kills the first credential.
	tok2, err := b.MintAcceptCredential(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if b.VerifyAcceptCredential(jobID, tok1) {
		t.Fatal("rotated-out credential still verifies")
	}
	if !b.VerifyAcceptCredential(jobID, tok2) {
		t.Fatal("current credential must verify")
	}
}

// Exactly-once under fire: ten concurrent Accepts produce ONE settlement record and
// one accepted stage; later Accepts are the idempotent no-op (G7's same-decision
// precedent), and the honest stop is durable — settlement PENDING the chain, the same
// shape as purchase.pending_settlement.
func TestAccept_ExactlyOnceAndIdempotent(t *testing.T) {
	b, st, jobID := acceptRig(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
				t.Errorf("accept: %v", err)
			}
		}()
	}
	wg.Wait()

	if n := countEvents(t, st, "settlement.pending_chain", jobID); n != 1 {
		t.Fatalf("settlement records = %d, want exactly 1", n)
	}
	js, _ := b.Job(jobID)
	if js.Stage != StageAccepted {
		t.Fatalf("stage %v, want accepted", js.Stage)
	}
	state, err := b.AcceptDelivery(ctx, jobID)
	if err != nil || state != "accepted-pending-chain" {
		t.Fatalf("repeat accept: %q %v, want idempotent accepted-pending-chain", state, err)
	}
	if n := countEvents(t, st, "settlement.pending_chain", jobID); n != 1 {
		t.Fatal("idempotent repeat appended a second settlement record")
	}
}

// AT-09's settlement clause: an engaged freeze refuses the Accept — the kill switch
// stops settlements the same way it stops payments and dispatches.
func TestAccept_FreezeGates(t *testing.T) {
	b, st, jobID := acceptRig(t)
	ctx := context.Background()
	reg, err := freeze.NewRegistry(ctx, st, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	b.SetFreeze(reg, "org_demo")
	if _, err := reg.Engage(ctx, freeze.KindJob, jobID, "gnanam", "incident"); err != nil {
		t.Fatal(err)
	}

	if _, err := b.AcceptDelivery(ctx, jobID); err == nil {
		t.Fatal("a frozen job accepted a settlement")
	}
	if n := countEvents(t, st, "settlement.pending_chain", jobID); n != 0 {
		t.Fatal("a frozen accept left a settlement record")
	}

	// Lift -> the customer's accept proceeds.
	if err := reg.Lift(ctx, freeze.KindJob, jobID, "gnanam", "resolved"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AcceptDelivery(ctx, jobID); err != nil {
		t.Fatalf("accept after lift: %v", err)
	}
}
