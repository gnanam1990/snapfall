package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
)

// scriptedHistory returns a different ReleaseHistory per range, so a test can prove the worker's
// output tracks its input without depending on a real repository.
type scriptedHistory struct {
	byRange map[string]ReleaseHistory
}

func (s scriptedHistory) History(_ context.Context, _, revisionRange string) (ReleaseHistory, error) {
	return s.byRange[revisionRange], nil
}

func runScribe(t *testing.T, source ReleaseHistorySource, assignment Assignment, purchase Purchase) []envelope.Envelope {
	t.Helper()
	env, err := envelope.New("job_release", envelope.RoleBrain, envelope.TypeAssignment, assignment)
	if err != nil {
		t.Fatal(err)
	}
	var reports []envelope.Envelope
	if err := NewReleaseScribe(source).Handle(context.Background(), env,
		func(_ context.Context, e envelope.Envelope) error { reports = append(reports, e); return nil },
		purchase); err != nil {
		t.Fatal(err)
	}
	return reports
}

func summaryOf(t *testing.T, reports []envelope.Envelope) (envelope.Deliverable, map[string]any) {
	t.Helper()
	if len(reports) != 2 {
		t.Fatalf("reports = %d, want progress then notes", len(reports))
	}
	if reports[0].Type != envelope.TypeWorkerProgress || reports[1].Type != envelope.TypeWorkerReport {
		t.Fatalf("report types = %s,%s", reports[0].Type, reports[1].Type)
	}
	var progress map[string]any
	if err := reports[0].Decode(&progress); err != nil {
		t.Fatal(err)
	}
	var notes envelope.Deliverable
	if err := reports[1].Decode(&notes); err != nil {
		t.Fatal(err)
	}
	return notes, progress
}

// THE ACCEPTANCE BAR: two different inputs must produce two different outputs. A release-notes
// worker whose summary is invariant to the range it is given is a stub with extra steps.
func TestReleaseScribeOutputMovesWithInput(t *testing.T) {
	source := scriptedHistory{byRange: map[string]ReleaseHistory{
		"a1..a2": {HeadRev: "a2a2a2a2a2a2a2a2", Range: "a1..a2", Commits: []ReleaseCommit{
			{Hash: "1111111111111111", Subject: "feat(brain): scoper (#80)", Category: "feat", PR: "#80"},
		}},
		"a2..a3": {HeadRev: "a3a3a3a3a3a3a3a3", Range: "a2..a3", Commits: []ReleaseCommit{
			{Hash: "2222222222222222", Subject: "daemon(h2): heartbeat cut (#86)", Category: "daemon", PR: "#86"},
			{Hash: "3333333333333333", Subject: "dashboard: dashes (#87)", Category: "dashboard", PR: "#87"},
		}},
	}}

	first, _ := summaryOf(t, runScribe(t, source, Assignment{Scope: "/repo", Range: "a1..a2"}, nil))
	second, _ := summaryOf(t, runScribe(t, source, Assignment{Scope: "/repo", Range: "a2..a3"}, nil))

	if first.Summary == second.Summary {
		t.Fatalf("output did not move with input: both summaries are %q — a stub with extra steps", first.Summary)
	}
	if len(first.Claims) == len(second.Claims) && len(first.Sources) == len(second.Sources) {
		t.Fatalf("claims/sources did not move with input: %d claims / %d sources both times",
			len(first.Claims), len(first.Sources))
	}
	// The evidence must be the commits of THAT range, not a shared constant.
	if strings.Join(first.Sources, ",") == strings.Join(second.Sources, ",") {
		t.Fatalf("both ranges cite the same sources %v — the notes are not derived from the range", first.Sources)
	}
}

// poisonPurchase fails the test if a worker ever tries to spend. Runtime proof, on top of AT-16's
// structural guarantee, that the scribe has no payment path.
func poisonPurchase(t *testing.T) Purchase {
	return func(context.Context, PurchaseRequest) (PurchaseOutcome, error) {
		t.Fatal("release-scribe attempted a purchase — a read-only worker must never reach the spend path")
		return PurchaseOutcome{}, nil
	}
}

func TestReleaseScribeNeverSpends(t *testing.T) {
	source := scriptedHistory{byRange: map[string]ReleaseHistory{
		"": {HeadRev: "cccccccccccccccc", Commits: []ReleaseCommit{
			{Hash: "4444444444444444", Subject: "docs: readme (#85)", Category: "docs", PR: "#85"},
		}},
	}}
	notes, _ := summaryOf(t, runScribe(t, source, Assignment{Scope: "/repo"}, poisonPurchase(t)))
	if notes.Summary == "" || len(notes.Sources) == 0 {
		t.Fatalf("notes carry no derived evidence: %+v", notes)
	}
}

// The real adapter must genuinely read committed history: two ranges over the same repository
// yield two different commit sets. This is the git-backed counterpart to the scripted variance
// test above — it proves the derivation is real, not that the worker copies a fixture.
func TestGitLogSourceDerivesFromCommittedHistory(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "release-scribe@test.invalid")
	runGit(t, repo, "config", "user.name", "Release Scribe Test")

	commit := func(name, subject string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(subject), 0o640); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo, "add", name)
		runGit(t, repo, "commit", "-m", subject)
		return runGit(t, repo, "rev-parse", "HEAD")
	}
	c1 := commit("a.txt", "feat(brain): scoper (#80)")
	c2 := commit("b.txt", "daemon(h2): heartbeat cut (#86)")
	c3 := commit("c.txt", "dashboard: dashes (#87)")

	early, err := (GitLogSource{}).History(context.Background(), repo, c1+".."+c2)
	if err != nil {
		t.Fatal(err)
	}
	late, err := (GitLogSource{}).History(context.Background(), repo, c2+".."+c3)
	if err != nil {
		t.Fatal(err)
	}
	if len(early.Commits) != 1 || early.Commits[0].Subject != "daemon(h2): heartbeat cut (#86)" {
		t.Fatalf("early range = %+v", early.Commits)
	}
	if early.Commits[0].Category != "daemon" || early.Commits[0].PR != "#86" {
		t.Fatalf("subject was not parsed into category/PR: %+v", early.Commits[0])
	}
	if len(late.Commits) != 1 || late.Commits[0].Subject != "dashboard: dashes (#87)" {
		t.Fatalf("late range = %+v", late.Commits)
	}
	if early.Commits[0].Hash == late.Commits[0].Hash {
		t.Fatal("two disjoint ranges returned the same commit — the source is not reading the range")
	}
}

// The working tree must not leak into committed history: an uncommitted change is not a release.
func TestGitLogSourceReadsCommittedNotWorkingTree(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "release-scribe@test.invalid")
	runGit(t, repo, "config", "user.name", "Release Scribe Test")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one"), 0o640); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "a.txt")
	runGit(t, repo, "commit", "-m", "docs: first (#1)")

	// Uncommitted — must not appear.
	if err := os.WriteFile(filepath.Join(repo, "b.txt"), []byte("two"), 0o640); err != nil {
		t.Fatal(err)
	}

	got, err := (GitLogSource{}).History(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Commits) != 1 || got.Commits[0].Subject != "docs: first (#1)" {
		t.Fatalf("working-tree change leaked into committed history: %+v", got.Commits)
	}
}

// A range that could be read as a git flag must be refused before it reaches git.
func TestGitLogSourceRejectsFlagShapedRange(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "release-scribe@test.invalid")
	runGit(t, repo, "config", "user.name", "Release Scribe Test")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one"), 0o640); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "a.txt")
	runGit(t, repo, "commit", "-m", "init")

	if _, err := (GitLogSource{}).History(context.Background(), repo, "--output=/tmp/x"); err == nil {
		t.Fatal("a flag-shaped revision range was accepted")
	}
}
