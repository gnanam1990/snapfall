package brain

import (
	"context"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

type recordingScan struct {
	called bool
	s      worker.ComplianceScan
}

func (r *recordingScan) Scan(context.Context, string) (worker.ComplianceScan, error) {
	r.called = true
	return r.s, nil
}

// Router wiring: a job whose worker kind is "compliance-scout" dispatches to the registered
// ComplianceScout through the real runTask chokepoint.
func TestRouter_DispatchesComplianceScoutByKind(t *testing.T) {
	b, _, _ := newTestBrain(t)
	src := &recordingScan{s: worker.ComplianceScan{
		Revision: "abcdef0123456789", Dir: "daemon/manifests", Files: 1,
		Findings: []worker.PolicyFinding{{
			Rule: "no-signing-authority", File: "daemon/manifests/finance.yaml", Subject: "finance",
			Violation: false, Evidence: "can_sign_payments: false",
		}},
	}}
	if err := b.RegisterWorker(worker.NewComplianceScout(src)); err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}

	b.mu.Lock()
	b.jobs["job_cs"] = &jobState{JobID: "job_cs", Scope: "/repo", Stage: StageAssigned, Worker: worker.ComplianceScoutKind}
	b.mu.Unlock()

	if err := b.runTask(context.Background(), "job_cs", worker.ComplianceScoutKind, nil, nil); err != nil {
		t.Fatalf("dispatch to compliance-scout failed: %v", err)
	}
	if !src.called {
		t.Fatal("compliance-scout was registered but the router never dispatched to it")
	}
	b.mu.Lock()
	stage := b.jobs["job_cs"].Stage
	b.mu.Unlock()
	if stage != StageComplete {
		t.Fatalf("dispatch ran but the report did not complete the job: stage=%s", stage)
	}
}
