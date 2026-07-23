// Command qa-demo runs the G9 QA loop live and prints the bounce, its reasons, the
// revision, and the passing verdict — plus the exhausted-escalation path with a
// hopeless author (Step-6 manual verification).
//
//	go run ./cmd/qa-demo
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/gnanam1990/snapfall/daemon/internal/brain"
	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
	"github.com/gnanam1990/snapfall/daemon/internal/qa"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

func banner(s string) {
	fmt.Printf("\n══ %s ══════════════════════════════════════\n", s)
}

func newBrain(dir string) (*brain.Brain, *brain.MemoryStore, *store.Store, error) {
	st, err := store.Open(context.Background(), filepath.Join(dir, "qa-demo.db"))
	if err != nil {
		return nil, nil, nil, err
	}
	mem, err := brain.NewMemoryStore(filepath.Join(dir, "jobs"))
	if err != nil {
		return nil, nil, nil, err
	}
	b := brain.New(slog.New(slog.NewTextHandler(io.Discard, nil)), st, mem, funding.New())
	b.SetScoper(brain.StubScoper{})
	return b, mem, st, nil
}

func printTrail(mem *brain.MemoryStore, jobID string) {
	jm, _ := mem.Get(jobID)
	fmt.Printf("   stage=%s completion=%d%% revisions=%d\n", jm.Stage, jm.CompletionPct, jm.RevisionCount)
	for i, n := range jm.QANotes {
		fmt.Printf("   qa[%d]: %s\n", i+1, n)
	}
	if jm.QADisclaimer != "" {
		fmt.Printf("   disclaimer: %s\n", jm.QADisclaimer)
	}
}

// hopelessWorker never fixes its unsourced claim.
type hopelessWorker struct{}

func (hopelessWorker) Kind() string { return "due-diligence" }
func (hopelessWorker) Handle(ctx context.Context, a envelope.Envelope, report worker.Report) error {
	draft := envelope.Deliverable{
		Title: "Hopeless report", Summary: "still unsourced",
		Claims:  []envelope.Claim{{Text: "unfixable claim", Sources: nil}},
		Sources: []string{"src:1"},
	}
	e, err := envelope.New(a.JobID, envelope.RoleWorker, envelope.TypeWorkerReport, draft)
	if err != nil {
		return err
	}
	return report(ctx, e)
}

func main() {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "snapfall-qa-demo")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	// ── The demo beat: planted claim bounces once, revision passes. ──
	banner("AT-19 — the demo beat: planted claim -> bounce -> revision -> pass")
	b, mem, st, err := newBrain(dir)
	if err != nil {
		panic(err)
	}
	defer st.Close()
	b.RegisterWorker(worker.StubDD{})
	b.RegisterQAWorker(qa.Worker{})

	if _, err := b.HandleOwnerRequest(ctx, "job_demo", "Acme Corp acquisition target"); err != nil {
		panic(err)
	}
	if err := b.Confirm(ctx, "job_demo", "gnanam"); err != nil {
		panic(err)
	}
	printTrail(mem, "job_demo")

	// ── Termination: a hopeless author escalates loudly after max revisions. ──
	banner("PIN 2 — hopeless author: bounded bounces, loud escalation")
	dir2, err := os.MkdirTemp("", "snapfall-qa-demo2")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir2)
	b2, mem2, st2, err := newBrain(dir2)
	if err != nil {
		panic(err)
	}
	defer st2.Close()
	b2.RegisterWorker(hopelessWorker{})
	b2.RegisterQAWorker(qa.Worker{})
	b2.SetMaxRevisions(2)

	if _, err := b2.HandleOwnerRequest(ctx, "job_doomed", "Doomed Co"); err != nil {
		panic(err)
	}
	if err := b2.Confirm(ctx, "job_doomed", "gnanam"); err != nil {
		panic(err)
	}
	printTrail(mem2, "job_doomed")

	fmt.Println("\n   the loop TERMINATED: escalated to the owner, visibly — not spinning, not silent.")
}
