package advancing

import (
	"context"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/approval"
)

// The money-path no-op defect, pinned: an advance proposed with a context that is
// CANCELLED right after the proposal (exactly what the owner-initiated HTTP path does —
// r.Context() dies when the POST returns) must STILL execute when the owner approves.
// Before the fix, the await goroutine was bound to the caller's context and exited on
// that cancellation, leaving the advance approved-but-never-executed.
func TestAdvance_ProposalSurvivesCallerContextCancellation(t *testing.T) {
	f, life, st, _, done := rig(t)

	// Propose with a cancellable context, then cancel it immediately — simulating the
	// HTTP request completing the moment the proposal POST returns.
	ctx, cancel := context.WithCancel(context.Background())
	req, err := f.Propose(ctx, "job_ctx", "", "25.00")
	if err != nil {
		t.Fatal(err)
	}
	cancel() // the "request ended" — the pre-fix await goroutine died right here

	// The owner approves on a SEPARATE (uncancelled) context, as a real decision does.
	if _, err := life.Decide(context.Background(), req.ID, approval.DecideApprove, "gnanam", "approved after the proposal request ended"); err != nil {
		t.Fatal(err)
	}
	<-done // the await goroutine must still be alive to reach its terminal outcome

	// It executed: a pending_chain (no lane wired in this rig) proves the goroutine
	// survived the cancellation and ran the approval to its honest terminal.
	if n := count(t, st, "advance.pending_chain"); n != 1 {
		t.Fatalf("advance.pending_chain = %d, want 1 — the approval must execute despite the caller's context being cancelled", n)
	}
}
