package worker

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
)

// ComplianceScoutKind is the Brain worker slot for the config/policy-alignment scan.
const ComplianceScoutKind = "compliance-scout"

// PolicyFinding is one rule evaluated against one committed config file. It is measured evidence:
// the rule, the file it was checked against, and whether that file violated it.
type PolicyFinding struct {
	Rule      string // e.g. "no-signing-authority"
	File      string // repo-relative path of the file checked
	Subject   string // the manifest's role, for a human-readable anchor
	Violation bool   // true when the file breaks the rule
	Evidence  string // the field value the verdict rests on
}

// ComplianceScan is the measured input to a compliance report: every rule evaluated across the
// scanned config files at a committed revision. It is a lint of internal policy alignment, never
// an external entity/sanctions screen (that is StubCompliance's job and stays a labeled stub).
type ComplianceScan struct {
	Root     string
	Revision string
	Dir      string
	Files    int
	Findings []PolicyFinding
}

// PolicySource is the Compliance-Scout's config seam, mirroring BuildProgressSource: a committed
// git adapter supplies production scans; a deterministic adapter drives the variance test.
type PolicySource interface {
	Scan(ctx context.Context, root string) (ComplianceScan, error)
}

// ComplianceScout scans committed configuration for policy alignment and reports the findings to
// Brain. Like Build Monitor it has no release, payment, advance, or signing capability: it reports
// evidence and authorizes nothing. It is an OPERATOR TOOL — no customer, quote, or escrow.
type ComplianceScout struct {
	source PolicySource
}

// NewComplianceScout constructs the worker around its read-only policy source.
func NewComplianceScout(source PolicySource) *ComplianceScout {
	return &ComplianceScout{source: source}
}

// Kind implements Worker.
func (*ComplianceScout) Kind() string { return ComplianceScoutKind }

// Handle scans once and emits a progress envelope (what was scanned) before the findings, the
// same ordering Build Monitor uses. Purchase is accepted by the interface and never used;
// TestComplianceScoutNeverSpends proves it at runtime on top of AT-16's structural guarantee.
func (w *ComplianceScout) Handle(ctx context.Context, assignment envelope.Envelope, report Report, _ Purchase) error {
	if w == nil || w.source == nil {
		return fmt.Errorf("compliance-scout source is not configured")
	}
	var a Assignment
	if err := assignment.Decode(&a); err != nil {
		return err
	}
	// INJECTION BOUNDARY. a.Scope is a repository path handed to git read-only (never a shell).
	// Safe for the same reason Build Monitor is: workerKind is deterministic, so model output never
	// reaches this line as a repository. Re-audit if workerKind is ever made model-selectable.
	root := strings.TrimSpace(a.Scope)
	if root == "" {
		return fmt.Errorf("compliance-scout assignment has no repository")
	}

	scan, err := w.source.Scan(ctx, root)
	if err != nil {
		return fmt.Errorf("scan %s: %w", root, err)
	}
	if strings.TrimSpace(scan.Revision) == "" {
		return fmt.Errorf("scan has no resolved revision")
	}

	violations := 0
	for _, f := range scan.Findings {
		if f.Violation {
			violations++
		}
	}

	progress, err := envelope.New(assignment.JobID, envelope.RoleWorker, envelope.TypeWorkerProgress,
		map[string]any{
			"stage":      "policy-scanned",
			"revision":   scan.Revision,
			"dir":        scan.Dir,
			"files":      scan.Files,
			"checks":     len(scan.Findings),
			"violations": violations,
		})
	if err != nil {
		return err
	}
	if err := report(ctx, progress); err != nil {
		return err
	}

	final, err := envelope.New(assignment.JobID, envelope.RoleWorker, envelope.TypeWorkerReport, complianceReport(scan, violations))
	if err != nil {
		return err
	}
	return report(ctx, final)
}

// complianceReport derives the deliverable from the measured scan. Every claim cites the file it
// was evaluated against, so a reader can verify each verdict. Output is a pure function of the
// findings: a different config set yields different findings and therefore a different report,
// which TestComplianceScoutOutputMovesWithInput asserts.
func complianceReport(scan ComplianceScan, violations int) envelope.Deliverable {
	// Group findings by file for a stable, readable report.
	byFile := map[string][]PolicyFinding{}
	for _, f := range scan.Findings {
		byFile[f.File] = append(byFile[f.File], f)
	}
	files := make([]string, 0, len(byFile))
	for file := range byFile {
		files = append(files, file)
	}
	sort.Strings(files)

	claims := make([]envelope.Claim, 0, len(files))
	sources := make([]string, 0, len(files))
	for _, file := range files {
		src := "git:" + scan.Revision + "#" + file
		sources = append(sources, src)
		bad := make([]string, 0)
		for _, f := range byFile[file] {
			if f.Violation {
				bad = append(bad, fmt.Sprintf("%s (%s)", f.Rule, f.Evidence))
			}
		}
		var text string
		if len(bad) == 0 {
			text = fmt.Sprintf("%s: %d rule(s) aligned", file, len(byFile[file]))
		} else {
			text = fmt.Sprintf("%s: %d violation(s) — %s", file, len(bad), strings.Join(bad, "; "))
		}
		claims = append(claims, envelope.Claim{Text: text, Sources: []string{src}})
	}

	verdict := "aligned: no policy violations in the scanned configuration"
	if violations > 0 {
		verdict = fmt.Sprintf("%d policy violation(s) found", violations)
	}
	// Abbreviate the revision inline rather than via a shared helper, so this worker shares no
	// package-level symbol with the other gallery workers and the branches never collide.
	rev := scan.Revision
	if len(rev) > 12 {
		rev = rev[:12]
	}
	summary := fmt.Sprintf("%d file(s), %d check(s) at %s — %s",
		scan.Files, len(scan.Findings), rev, verdict)

	return envelope.Deliverable{
		Title:   "Policy-alignment findings",
		Summary: summary,
		Claims:  claims,
		Sources: sources,
		Disclaimer: "Least-privilege lint of committed configuration; evidence of internal policy alignment. " +
			"This is NOT sanctions or entity screening — that remains a labeled stub (StubCompliance) until Circle credentials exist.",
	}
}
