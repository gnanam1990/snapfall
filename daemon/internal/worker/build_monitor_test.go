package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
)

type fixedBuildProgress struct {
	snapshot BuildSnapshot
}

func TestGitChecklistSourceMeasuresCommittedRepositoryArtifacts(t *testing.T) {
	repo := t.TempDir()
	mustWrite := func(name, content string) {
		t.Helper()
		path := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	revision := "0123456789abcdef0123456789abcdef01234567"
	mustWrite(".git/HEAD", "ref: refs/heads/main\n")
	mustWrite(".git/refs/heads/main", revision+"\n")
	mustWrite(".snapfall/milestone.json", `{
	  "checks": [
	    {"name":"contract","path":"dist/contract.json"},
	    {"name":"integration","path":"reports/integration.txt"},
	    {"name":"release","path":"reports/release.txt"}
	  ]
	}`)
	mustWrite("dist/contract.json", "{}")
	mustWrite("reports/integration.txt", "green")

	got, err := (GitChecklistSource{}).Snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if got.CompletionPct != 66 || got.Revision != revision {
		t.Fatalf("snapshot = %+v", got)
	}
	if len(got.Completed) != 2 || len(got.Pending) != 1 || got.Pending[0] != "release" {
		t.Fatalf("check classification = %+v", got)
	}
}

func TestGitChecklistSourceReadsPackedHeadRef(t *testing.T) {
	repo := t.TempDir()
	mustWrite := func(name, content string) {
		t.Helper()
		path := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	revision := "fedcba9876543210fedcba9876543210fedcba98"
	mustWrite(".git/HEAD", "ref: refs/heads/main\n")
	mustWrite(".git/packed-refs", "# pack-refs with: peeled fully-peeled sorted\n"+revision+" refs/heads/main\n")
	mustWrite(".snapfall/milestone.json", `{"checks":[{"name":"ready","path":"ready.txt"}]}`)
	mustWrite("ready.txt", "yes")

	got, err := (GitChecklistSource{}).Snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != revision || got.CompletionPct != 100 {
		t.Fatalf("snapshot = %+v", got)
	}
}

func (f fixedBuildProgress) Snapshot(context.Context, string) (BuildSnapshot, error) {
	return f.snapshot, nil
}

func TestBuildMonitorReportsMeasuredProgressBeforeItsReleaseRecommendation(t *testing.T) {
	monitor := NewBuildMonitor(fixedBuildProgress{snapshot: BuildSnapshot{
		CompletionPct: 67,
		Revision:      "a1b2c3d",
		Completed:     []string{"contract", "integration"},
		Pending:       []string{"release"},
	}})
	assignment, err := envelope.New("pipeline_acme_m2", envelope.RoleBrain, envelope.TypeAssignment,
		Assignment{Scope: "/work/acme"})
	if err != nil {
		t.Fatal(err)
	}

	var reports []envelope.Envelope
	err = monitor.Handle(context.Background(), assignment, func(_ context.Context, e envelope.Envelope) error {
		reports = append(reports, e)
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 {
		t.Fatalf("reports = %d, want progress then recommendation", len(reports))
	}
	if reports[0].Type != envelope.TypeWorkerProgress {
		t.Fatalf("first report = %s, want %s", reports[0].Type, envelope.TypeWorkerProgress)
	}
	var progress struct {
		Stage         string `json:"stage"`
		CompletionPct int    `json:"completion_pct"`
		Revision      string `json:"revision"`
	}
	if err := reports[0].Decode(&progress); err != nil {
		t.Fatal(err)
	}
	if progress.Stage != "build-monitored" || progress.CompletionPct != 67 || progress.Revision != "a1b2c3d" {
		t.Fatalf("progress = %+v", progress)
	}
	if reports[1].Type != envelope.TypeWorkerReport {
		t.Fatalf("second report = %s, want %s", reports[1].Type, envelope.TypeWorkerReport)
	}
	var deliverable envelope.Deliverable
	if err := reports[1].Decode(&deliverable); err != nil {
		t.Fatal(err)
	}
	if deliverable.Summary == "" || len(deliverable.Sources) == 0 {
		t.Fatalf("release recommendation lacks measured evidence: %+v", deliverable)
	}
}
