package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/brain"
	"github.com/gnanam1990/snapfall/daemon/internal/funding"
	"github.com/gnanam1990/snapfall/daemon/internal/ownerapi"
	"github.com/gnanam1990/snapfall/daemon/internal/store"
	"github.com/gnanam1990/snapfall/daemon/internal/worker"
)

func hireGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestBuildMonitorHireStartsRepositoryWatcher(t *testing.T) {
	repository := t.TempDir()
	hireGit(t, repository, "init", "-b", "main")
	hireGit(t, repository, "config", "user.email", "hire@test.invalid")
	hireGit(t, repository, "config", "user.name", "Hire Test")
	checklist := filepath.Join(repository, ".snapfall", "milestone.json")
	if err := os.MkdirAll(filepath.Dir(checklist), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checklist,
		[]byte(`{"checks":[{"name":"release","path":"reports/release.txt"}]}`), 0o640); err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(repository, "reports", "release.txt")
	if err := os.MkdirAll(filepath.Dir(release), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, []byte("ready"), 0o640); err != nil {
		t.Fatal(err)
	}
	hireGit(t, repository, "add", ".snapfall/milestone.json", "reports/release.txt")
	hireGit(t, repository, "commit", "-m", "add milestone evidence")

	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "hire.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	memory, err := brain.NewMemoryStore(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	b := brain.New(slog.New(slog.NewTextHandler(io.Discard, nil)), st, memory, funding.New())
	if err := b.RegisterWorker(worker.NewBuildMonitor(worker.GitChecklistSource{})); err != nil {
		t.Fatal(err)
	}

	result, err := buildMonitorHire(b)(ctx, ownerapi.HireWorkerRequest{
		ManifestID: worker.BuildMonitorKind,
		Repository: repository,
		QuoteUSDC:  "25.00",
		By:         "anandan",
	})
	if err != nil {
		t.Fatal(err)
	}
	if (result.State != "assigned" && result.State != "complete") ||
		result.JobID == "" || result.VaultJobID == "" {
		t.Fatalf("hire result = %+v", result)
	}
	if err := b.AwaitTask(result.JobID); err != nil {
		t.Fatalf("watcher task: %v", err)
	}
	job, ok := b.Job(result.JobID)
	if !ok || job.Worker != worker.BuildMonitorKind || job.Stage != brain.StageComplete {
		t.Fatalf("watcher job = %+v, exists=%t", job, ok)
	}
	var progress int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events WHERE kind='brain.msg.worker.progress' AND entity_id=?`,
		result.JobID).Scan(&progress); err != nil {
		t.Fatal(err)
	}
	if progress != 1 {
		t.Fatalf("watcher progress events = %d, want 1", progress)
	}
}

func TestBuildMonitorHireResumesPersistedUnconfirmedMilestone(t *testing.T) {
	repository := t.TempDir()
	hireGit(t, repository, "init", "-b", "main")
	hireGit(t, repository, "config", "user.email", "hire@test.invalid")
	hireGit(t, repository, "config", "user.name", "Hire Test")
	checklist := filepath.Join(repository, ".snapfall", "milestone.json")
	if err := os.MkdirAll(filepath.Dir(checklist), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checklist,
		[]byte(`{"checks":[{"name":"release","path":"reports/release.txt"}]}`), 0o640); err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(repository, "reports", "release.txt")
	if err := os.MkdirAll(filepath.Dir(release), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(release, []byte("ready"), 0o640); err != nil {
		t.Fatal(err)
	}
	hireGit(t, repository, "add", ".snapfall/milestone.json", "reports/release.txt")
	hireGit(t, repository, "commit", "-m", "add empty milestone")

	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "hire.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	memory, err := brain.NewMemoryStore(filepath.Join(t.TempDir(), "memory"))
	if err != nil {
		t.Fatal(err)
	}
	b := brain.New(slog.New(slog.NewTextHandler(io.Discard, nil)), st, memory, funding.New())
	if err := b.RegisterWorker(worker.NewBuildMonitor(worker.GitChecklistSource{})); err != nil {
		t.Fatal(err)
	}

	req := ownerapi.HireWorkerRequest{
		ManifestID: worker.BuildMonitorKind,
		Repository: repository,
		QuoteUSDC:  "25.00",
		By:         "anandan",
	}
	opened, err := b.OpenMilestone(ctx, brain.Milestone{
		StandingInstructionID: "hire:" + repository,
		Number:                1,
		Repository:            repository,
		QuoteUSDC:             "25.00",
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := buildMonitorHire(b)(ctx, req)
	if err != nil {
		t.Fatalf("retrying persisted unconfirmed hire: %v", err)
	}
	if result.JobID != opened.JobID || result.VaultJobID != opened.VaultJobID {
		t.Fatalf("retry created a different cycle: result=%+v opened=%+v", result, opened)
	}
	if err := b.AwaitTask(result.JobID); err != nil {
		t.Fatal(err)
	}

	again, err := buildMonitorHire(b)(ctx, req)
	if err != nil {
		t.Fatalf("repeating completed hire: %v", err)
	}
	if again.JobID != result.JobID || again.State != "complete" {
		t.Fatalf("completed hire retry = %+v, want same job in complete state", again)
	}
	activations, err := buildMonitorActivations(b)(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(activations) != 1 || activations[0].JobID != result.JobID ||
		activations[0].Repository != repository || activations[0].State != "complete" {
		t.Fatalf("durable activation projection = %+v", activations)
	}
}
