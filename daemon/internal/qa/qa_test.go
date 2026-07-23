package qa

import (
	"strings"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/envelope"
)

func goodDeliverable() envelope.Deliverable {
	return envelope.Deliverable{
		Title:   "Due-diligence report",
		Summary: "All findings sourced.",
		Claims: []envelope.Claim{
			{Text: "registry entry located", Sources: []string{"registry:1"}},
			{Text: "no adverse media", Sources: []string{"media:1"}},
		},
		Sources: []string{"registry:1", "media:1"},
	}
}

func TestReview_PassesACleanDeliverable(t *testing.T) {
	v := Reviewer{}.Review(goodDeliverable())
	if !v.Passed || len(v.Reasons) != 0 {
		t.Fatalf("clean deliverable bounced: %+v", v)
	}
	if v.Checked != 2 {
		t.Errorf("checked %d claims, want 2", v.Checked)
	}
}

// The demo's planted claim: one claim with no source bounces the draft, with a reason
// naming the exact claim — the bounce is never silent (FR-QA-001).
func TestReview_PlantedUnsupportedClaimBounces(t *testing.T) {
	d := goodDeliverable()
	d.Claims = append(d.Claims, envelope.Claim{Text: "churn is 40% annually", Sources: nil})

	v := Reviewer{}.Review(d)
	if v.Passed {
		t.Fatal("unsupported claim passed QA")
	}
	if len(v.Reasons) != 1 || !strings.Contains(v.Reasons[0], "churn is 40% annually") {
		t.Fatalf("reason must name the claim: %v", v.Reasons)
	}
	if !strings.Contains(v.Reasons[0], "unsupported claim") {
		t.Fatalf("reason must say WHY: %v", v.Reasons)
	}
}

func TestReview_CompletenessChecks(t *testing.T) {
	v := Reviewer{}.Review(envelope.Deliverable{})
	if v.Passed {
		t.Fatal("an empty deliverable passed")
	}
	// All four completeness reasons present.
	joined := strings.Join(v.Reasons, "\n")
	for _, want := range []string{"title is empty", "summary is empty", "no claims", "no sources"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing completeness reason %q in %v", want, v.Reasons)
		}
	}
}

// A claim citing a source that is not in the deliverable's source list is a coverage
// failure — the citation must be auditable, not just present.
func TestReview_SourceCoverageChecksCitations(t *testing.T) {
	d := goodDeliverable()
	d.Claims = append(d.Claims, envelope.Claim{Text: "funding round confirmed", Sources: []string{"rumor:1"}})

	v := Reviewer{}.Review(d)
	if v.Passed {
		t.Fatal("uncited source passed")
	}
	if !strings.Contains(strings.Join(v.Reasons, "\n"), `"rumor:1"`) {
		t.Fatalf("reason must name the missing source: %v", v.Reasons)
	}
}

func TestReview_CustomerDataLeakage(t *testing.T) {
	d := goodDeliverable()
	d.Claims = append(d.Claims, envelope.Claim{Text: "per ACME-INTERNAL-KEY-7 the target is in breach", Sources: []string{"registry:1"}})

	v := Reviewer{LeakMarkers: []string{"ACME-INTERNAL-KEY-7"}}.Review(d)
	if v.Passed {
		t.Fatal("leaked customer marker passed QA")
	}
	if !strings.Contains(strings.Join(v.Reasons, "\n"), "leakage") {
		t.Fatalf("reason must say leakage: %v", v.Reasons)
	}
}

// Review batch: "anywhere" includes the source lists — a confidential marker used as
// a source identifier must bounce too.
func TestReview_CustomerDataLeakageInSources(t *testing.T) {
	r := Reviewer{LeakMarkers: []string{"ACME-INTERNAL-KEY-7"}}

	d := goodDeliverable()
	d.Sources = append(d.Sources, "ACME-INTERNAL-KEY-7/export.csv")
	if v := r.Review(d); v.Passed {
		t.Fatal("marker in the deliverable source list passed QA")
	}

	d2 := goodDeliverable()
	d2.Claims[0].Sources = []string{"registry:1", "ref:ACME-INTERNAL-KEY-7"}
	d2.Sources = append(d2.Sources, "ref:ACME-INTERNAL-KEY-7")
	if v := r.Review(d2); v.Passed {
		t.Fatal("marker in a claim's citation list passed QA")
	}
}

// G9 pin 3: EVERY verdict — pass or fail — carries the evidence-not-guarantee
// disclaimer. A pass is a review result, not a certification.
func TestReview_DisclaimerOnEveryVerdict(t *testing.T) {
	pass := Reviewer{}.Review(goodDeliverable())
	fail := Reviewer{}.Review(envelope.Deliverable{})

	for name, v := range map[string]envelope.QAVerdict{"pass": pass, "fail": fail} {
		if v.Disclaimer != Disclaimer {
			t.Errorf("%s verdict missing the disclaimer: %q", name, v.Disclaimer)
		}
	}
	if !strings.Contains(Disclaimer, "not a guarantee") {
		t.Fatal("the disclaimer must say 'not a guarantee' in those words")
	}
}

// Determinism: same draft, same verdict, byte-identical reasons — the demo beat lands
// the same on every take.
func TestReview_Deterministic(t *testing.T) {
	d := goodDeliverable()
	d.Claims = append(d.Claims, envelope.Claim{Text: "planted", Sources: nil})

	r := Reviewer{LeakMarkers: []string{"SECRET"}}
	v1, v2 := r.Review(d), r.Review(d)
	if v1.Passed != v2.Passed || strings.Join(v1.Reasons, "|") != strings.Join(v2.Reasons, "|") {
		t.Fatalf("same draft, different verdicts:\n%v\n%v", v1, v2)
	}
}
