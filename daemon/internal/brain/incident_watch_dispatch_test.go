package brain

import (
	"context"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

type recordingWatch struct {
	called bool
	scan   worker.IncidentScan
}

func (r *recordingWatch) Watch(context.Context) (worker.IncidentScan, error) {
	r.called = true
	return r.scan, nil
}

// Router wiring: a job whose worker kind is "incident-watch" dispatches to the registered
// IncidentWatch through the real runTask chokepoint.
func TestRouter_DispatchesIncidentWatchByKind(t *testing.T) {
	b, _, _ := newTestBrain(t)
	src := &recordingWatch{scan: worker.IncidentScan{Connected: true, StreamURL: "http://x", StreamScanned: 3}}
	if err := b.RegisterWorker(worker.NewIncidentWatch(src)); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}

	b.mu.Lock()
	b.jobs["job_iw"] = &jobState{JobID: "job_iw", Scope: "monitor", Stage: StageAssigned, Worker: worker.IncidentWatchKind}
	b.mu.Unlock()

	if err := b.runTask(context.Background(), "job_iw", worker.IncidentWatchKind, nil, nil); err != nil {
		t.Fatalf("dispatch to incident-watch failed: %v", err)
	}
	if !src.called {
		t.Fatal("incident-watch was registered but the router never dispatched to it")
	}
	b.mu.Lock()
	stage := b.jobs["job_iw"].Stage
	b.mu.Unlock()
	if stage != StageComplete {
		t.Fatalf("dispatch ran but the report did not complete the job: stage=%s", stage)
	}
}
