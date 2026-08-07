package brain

import (
	"context"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

// recordingHistory notes whether the worker's source was reached, so the dispatch test proves the
// router invoked the worker rather than merely that it was registered.
type recordingHistory struct {
	called bool
	h      worker.ReleaseHistory
}

func (r *recordingHistory) History(context.Context, string, string) (worker.ReleaseHistory, error) {
	r.called = true
	return r.h, nil
}

// Router wiring: a job whose worker kind is "release-scribe" dispatches to the registered
// ReleaseScribe through the real runTask chokepoint. Without the RegisterWorker wiring the lookup
// misses and runTask returns "no worker registered for kind".
func TestRouter_DispatchesReleaseScribeByKind(t *testing.T) {
	b, _, _ := newTestBrain(t)
	src := &recordingHistory{h: worker.ReleaseHistory{
		HeadRev: "abcdef0123456789",
		Commits: []worker.ReleaseCommit{{Hash: "1111111111111111", Subject: "feat: x (#1)", Category: "feat", PR: "#1"}},
	}}
	if err := b.RegisterWorker(worker.NewReleaseScribe(src)); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}

	// StageAssigned is the stage at which a worker report is accepted (chat.go). With no QA slot
	// registered here, the no-QA flow records the report and completes the job.
	b.mu.Lock()
	b.jobs["job_rs"] = &jobState{JobID: "job_rs", Scope: "/repo", Stage: StageAssigned, Worker: worker.ReleaseScribeKind}
	b.mu.Unlock()

	if err := b.runTask(context.Background(), "job_rs", worker.ReleaseScribeKind, nil, nil); err != nil {
		t.Fatalf("dispatch to release-scribe failed: %v", err)
	}
	if !src.called {
		t.Fatal("release-scribe was registered but the router never dispatched to it")
	}
	b.mu.Lock()
	stage := b.jobs["job_rs"].Stage
	b.mu.Unlock()
	if stage != StageComplete {
		t.Fatalf("dispatch ran but the report did not complete the job: stage=%s", stage)
	}
}
