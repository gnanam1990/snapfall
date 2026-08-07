package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
)

// scriptedScan returns a different ComplianceScan per repository root, so the worker's output can
// be tested against its input without a real repository.
type scriptedScan struct {
	byRoot map[string]ComplianceScan
}

func (s scriptedScan) Scan(_ context.Context, root string) (ComplianceScan, error) {
	return s.byRoot[root], nil
}

func runScout(t *testing.T, source PolicySource, root string, purchase Purchase) (envelope.Deliverable, map[string]any) {
	t.Helper()
	env, err := envelope.New("job_scout", envelope.RoleBrain, envelope.TypeAssignment, Assignment{Scope: root})
	if err != nil {
		t.Fatal(err)
	}
	var reports []envelope.Envelope
	if err := NewComplianceScout(source).Handle(context.Background(), env,
		func(_ context.Context, e envelope.Envelope) error { reports = append(reports, e); return nil },
		purchase); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 || reports[0].Type != envelope.TypeWorkerProgress || reports[1].Type != envelope.TypeWorkerReport {
		t.Fatalf("reports = %d/%s..%s, want progress then findings", len(reports), reports[0].Type, reports[1].Type)
	}
	var progress map[string]any
	if err := reports[0].Decode(&progress); err != nil {
		t.Fatal(err)
	}
	var findings envelope.Deliverable
	if err := reports[1].Decode(&findings); err != nil {
		t.Fatal(err)
	}
	return findings, progress
}

// THE ACCEPTANCE BAR: two different config sets must produce two different reports. A scan whose
// verdict is invariant to what it scanned is a stub with extra steps.
func TestComplianceScoutOutputMovesWithInput(t *testing.T) {
	source := scriptedScan{byRoot: map[string]ComplianceScan{
		"/clean": {Revision: "aaaaaaaaaaaaaaaa", Dir: "daemon/manifests", Files: 1, Findings: []PolicyFinding{
			{Rule: "no-signing-authority", File: "daemon/manifests/finance.yaml", Subject: "finance", Violation: false, Evidence: "can_sign_payments: false"},
		}},
		"/dirty": {Revision: "bbbbbbbbbbbbbbbb", Dir: "daemon/manifests", Files: 1, Findings: []PolicyFinding{
			{Rule: "no-signing-authority", File: "daemon/manifests/finance.yaml", Subject: "finance", Violation: true, Evidence: "can_sign_payments: true"},
		}},
	}}

	clean, cleanProg := runScout(t, source, "/clean", nil)
	dirty, dirtyProg := runScout(t, source, "/dirty", nil)

	if clean.Summary == dirty.Summary {
		t.Fatalf("output did not move with input: both summaries are %q — a stub with extra steps", clean.Summary)
	}
	if cleanProg["violations"] == dirtyProg["violations"] {
		t.Fatalf("violation count did not move with input: %v both times", cleanProg["violations"])
	}
	if !strings.Contains(dirty.Summary, "violation") || strings.Contains(clean.Summary, "1 policy violation") {
		t.Fatalf("summaries do not reflect the findings: clean=%q dirty=%q", clean.Summary, dirty.Summary)
	}
}

func TestComplianceScoutNeverSpends(t *testing.T) {
	source := scriptedScan{byRoot: map[string]ComplianceScan{
		"/repo": {Revision: "cccccccccccccccc", Dir: "daemon/manifests", Files: 1, Findings: []PolicyFinding{
			{Rule: "escalation-path", File: "daemon/manifests/manager.yaml", Subject: "manager", Violation: false, Evidence: `escalates_to: "human"`},
		}},
	}}
	poison := func(context.Context, PurchaseRequest) (PurchaseOutcome, error) {
		t.Fatal("compliance-scout attempted a purchase — a read-only operator tool must never spend")
		return PurchaseOutcome{}, nil
	}
	findings, _ := runScout(t, source, "/repo", poison)
	if findings.Summary == "" || len(findings.Sources) == 0 {
		t.Fatalf("findings carry no derived evidence: %+v", findings)
	}
	// It must never be mistaken for entity screening.
	if !strings.Contains(findings.Disclaimer, "NOT sanctions or entity screening") {
		t.Fatalf("disclaimer must distinguish this from entity screening: %q", findings.Disclaimer)
	}
}

// The real adapter must derive its verdict from the committed manifests: a manifest that grants
// signing authority is a violation; the same directory without it is not.
func TestManifestPolicySourceDerivesFromCommittedManifests(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "compliance-scout@test.invalid")
	runGit(t, repo, "config", "user.name", "Compliance Scout Test")

	manifestPath := filepath.Join(repo, "manifests", "finance.yaml")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o750); err != nil {
		t.Fatal(err)
	}
	writeManifest := func(canSign bool) {
		t.Helper()
		body := "role: finance\nescalates_to: manager\ncommand_allowlist: []\n" +
			"can_request_advance: false\ncan_sign_payments: " + boolYAML(canSign) + "\n"
		if err := os.WriteFile(manifestPath, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
		runGit(t, repo, "add", "manifests/finance.yaml")
		runGit(t, repo, "commit", "-m", "manifest")
	}

	// Clean posture: no signing authority.
	writeManifest(false)
	clean, err := (ManifestPolicySource{Dir: "manifests"}).Scan(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if v := countViolations(clean); v != 0 {
		t.Fatalf("clean manifest reported %d violation(s): %+v", v, clean.Findings)
	}

	// Grant signing authority — a real least-privilege violation.
	writeManifest(true)
	dirty, err := (ManifestPolicySource{Dir: "manifests"}).Scan(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if countViolations(dirty) != 1 {
		t.Fatalf("signing-authority manifest should be exactly one violation: %+v", dirty.Findings)
	}
	var signed *PolicyFinding
	for i := range dirty.Findings {
		if dirty.Findings[i].Rule == "no-signing-authority" {
			signed = &dirty.Findings[i]
		}
	}
	if signed == nil || !signed.Violation || signed.Subject != "finance" {
		t.Fatalf("the signing-authority violation was not attributed to the finance manifest: %+v", dirty.Findings)
	}
	if clean.Revision == dirty.Revision {
		t.Fatal("two commits produced the same revision — the scan is not reading committed state")
	}
}

// A committed manifest symlink must be refused rather than followed out of the repository.
func TestManifestPolicySourceRejectsManifestSymlink(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "compliance-scout@test.invalid")
	runGit(t, repo, "config", "user.name", "Compliance Scout Test")
	outside := filepath.Join(t.TempDir(), "evil.yaml")
	if err := os.WriteFile(outside, []byte("role: finance\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(repo, "manifests", "finance.yaml")
	if err := os.MkdirAll(filepath.Dir(link), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "manifests/finance.yaml")
	runGit(t, repo, "commit", "-m", "symlinked manifest")

	if _, err := (ManifestPolicySource{Dir: "manifests"}).Scan(context.Background(), repo); err == nil ||
		!strings.Contains(err.Error(), "symlink") {
		t.Fatalf("committed manifest symlink was not rejected: %v", err)
	}
}

func boolYAML(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func countViolations(scan ComplianceScan) int {
	n := 0
	for _, f := range scan.Findings {
		if f.Violation {
			n++
		}
	}
	return n
}
