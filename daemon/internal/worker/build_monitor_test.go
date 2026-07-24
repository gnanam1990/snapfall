package worker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
)

type fixedBuildProgress struct {
	snapshot BuildSnapshot
}

func runGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestGitChecklistSourceDoesNotAttributeUncommittedArtifactsToHead(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "build-monitor@test.invalid")
	runGit(t, repo, "config", "user.name", "Build Monitor Test")
	checklist := filepath.Join(repo, ".snapfall", "milestone.json")
	if err := os.MkdirAll(filepath.Dir(checklist), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checklist,
		[]byte(`{"checks":[{"name":"release","path":"reports/release.txt"}]}`), 0o640); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".snapfall/milestone.json")
	runGit(t, repo, "commit", "-m", "add milestone checklist")
	revision := runGit(t, repo, "rev-parse", "HEAD")

	// Exists in the working tree only. It must remain pending for HEAD evidence.
	release := filepath.Join(repo, "reports", "release.txt")
	if err := os.MkdirAll(filepath.Dir(release), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, []byte("not committed"), 0o640); err != nil {
		t.Fatal(err)
	}

	got, err := (GitChecklistSource{}).Snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != revision || got.CompletionPct != 0 ||
		len(got.Pending) != 1 || got.Pending[0] != "release" {
		t.Fatalf("uncommitted artifact was attributed to HEAD: %+v", got)
	}
}

func TestGitChecklistSourceRejectsCommittedArtifactSymlink(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "build-monitor@test.invalid")
	runGit(t, repo, "config", "user.name", "Build Monitor Test")
	checklist := filepath.Join(repo, ".snapfall", "milestone.json")
	if err := os.MkdirAll(filepath.Dir(checklist), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checklist,
		[]byte(`{"checks":[{"name":"release","path":"reports/release.txt"}]}`), 0o640); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "release.txt")
	if err := os.WriteFile(outside, []byte("outside repository"), 0o640); err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(repo, "reports", "release.txt")
	if err := os.MkdirAll(filepath.Dir(release), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, release); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".snapfall/milestone.json", "reports/release.txt")
	runGit(t, repo, "commit", "-m", "add symlinked milestone evidence")

	if _, err := (GitChecklistSource{}).Snapshot(context.Background(), repo); err == nil {
		t.Fatal("committed artifact symlink was accepted as repository-contained evidence")
	}
}

func TestGitChecklistSourceRejectsCommittedChecklistSymlink(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "build-monitor@test.invalid")
	runGit(t, repo, "config", "user.name", "Build Monitor Test")
	outside := filepath.Join(t.TempDir(), "milestone.json")
	if err := os.WriteFile(outside,
		[]byte(`{"checks":[{"name":"release","path":"release.txt"}]}`), 0o640); err != nil {
		t.Fatal(err)
	}
	checklist := filepath.Join(repo, ".snapfall", "milestone.json")
	if err := os.MkdirAll(filepath.Dir(checklist), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, checklist); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".snapfall/milestone.json")
	runGit(t, repo, "commit", "-m", "add symlinked milestone checklist")

	if _, err := (GitChecklistSource{}).Snapshot(context.Background(), repo); err == nil ||
		!strings.Contains(err.Error(), "is a symlink") {
		t.Fatalf("committed checklist symlink was not explicitly rejected: %v", err)
	}
}

func TestGitChecklistSourceRejectsDirectoryAsArtifact(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "build-monitor@test.invalid")
	runGit(t, repo, "config", "user.name", "Build Monitor Test")
	checklist := filepath.Join(repo, ".snapfall", "milestone.json")
	if err := os.MkdirAll(filepath.Dir(checklist), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checklist,
		[]byte(`{"checks":[{"name":"release","path":"reports"}]}`), 0o640); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(repo, "reports", "release.txt")
	if err := os.MkdirAll(filepath.Dir(report), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(report, []byte("committed child"), 0o640); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".snapfall/milestone.json", "reports/release.txt")
	runGit(t, repo, "commit", "-m", "add directory-shaped milestone evidence")

	if _, err := (GitChecklistSource{}).Snapshot(context.Background(), repo); err == nil {
		t.Fatal("Git tree directory was accepted as a completed artifact")
	}
}

func TestGitChecklistSourceMeasuresCommittedRepositoryArtifacts(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "build-monitor@test.invalid")
	runGit(t, repo, "config", "user.name", "Build Monitor Test")
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
	mustWrite(".snapfall/milestone.json", `{
	  "checks": [
	    {"name":"contract","path":"dist/contract.json"},
	    {"name":"integration","path":"reports/integration.txt"},
	    {"name":"release","path":"reports/release.txt"}
	  ]
	}`)
	mustWrite("dist/contract.json", "{}")
	mustWrite("reports/integration.txt", "green")
	runGit(t, repo, "add", ".snapfall/milestone.json", "dist/contract.json", "reports/integration.txt")
	runGit(t, repo, "commit", "-m", "add milestone evidence")
	revision := runGit(t, repo, "rev-parse", "HEAD")

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
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "build-monitor@test.invalid")
	runGit(t, repo, "config", "user.name", "Build Monitor Test")
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
	mustWrite(".snapfall/milestone.json", `{"checks":[{"name":"ready","path":"ready.txt"}]}`)
	mustWrite("ready.txt", "yes")
	runGit(t, repo, "add", ".snapfall/milestone.json", "ready.txt")
	runGit(t, repo, "commit", "-m", "add ready milestone")
	revision := runGit(t, repo, "rev-parse", "HEAD")
	runGit(t, repo, "pack-refs", "--all")

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
