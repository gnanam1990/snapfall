package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

// scripts/spine_run grades its beats against three of DemoPolicy()'s thresholds, and bash cannot
// import this package, so it mirrors them as shell variables. A mirror drifts silently: someone
// raises ApprovalAboveMicros here, spine_run keeps checking against the old number, and its
// preflight starts approving a figure set that makes beat 4 auto-approve instead of escalate.
// The run would print PASS for a spine that never tested the rejection path.
//
// This is the same guard DemoMerchant* already relies on, for the same reason: a failing test
// instead of a failing take.
func TestSpineRunMirrorsDemoPolicyThresholds(t *testing.T) {
	path := filepath.Join("..", "..", "..", "scripts", "spine_run")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	cfg := DemoPolicy()
	for _, want := range []struct {
		shellVar string
		value    int64
		goField  string
	}{
		{"policy_approval_above", cfg.ApprovalAboveMicros, "ApprovalAboveMicros"},
		{"policy_per_tx_limit", cfg.PerTxLimitMicros, "PerTxLimitMicros"},
		{"policy_job_budget", cfg.JobBudgetMicros, "JobBudgetMicros"},
	} {
		got, err := shellInt(string(src), want.shellVar)
		if err != nil {
			t.Errorf("%s: %v", want.shellVar, err)
			continue
		}
		if got != want.value {
			t.Errorf(
				"scripts/spine_run has %s=%d but DemoPolicy().%s is %d.\n"+
					"spine_run's check_figures() grades every demo spend against its copy, so while "+
					"these disagree its preflight is approving figure sets the engine will treat "+
					"differently. Update the shell variable.",
				want.shellVar, got, want.goField, want.value,
			)
		}
	}
}

// The demo spends themselves live in spine_run and are deliberately NOT scaled by --price,
// because they are graded against absolute thresholds. Assert here that the shipped figures
// actually land where each beat needs them to, so the relationship is pinned in Go too and not
// only inside the bash it describes.
func TestSpineRunDemoSpendsLandOnTheRightSideOfEveryThreshold(t *testing.T) {
	path := filepath.Join("..", "..", "..", "scripts", "spine_run")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	profile, err := shellInt(string(src), "profile_micros")
	if err != nil {
		t.Fatal(err)
	}
	premium, err := shellInt(string(src), "premium_micros")
	if err != nil {
		t.Fatal(err)
	}
	bench, err := shellInt(string(src), "bench_micros")
	if err != nil {
		t.Fatal(err)
	}

	cfg := DemoPolicy()

	// Beats 3 and 5 must auto-approve, or the demo shows a prompt where it promises none.
	if profile >= cfg.ApprovalAboveMicros {
		t.Errorf("beat 3 spends %d, at or above the %d approval threshold: it would escalate", profile, cfg.ApprovalAboveMicros)
	}
	if bench >= cfg.ApprovalAboveMicros {
		t.Errorf("beat 5 spends %d, at or above the %d approval threshold: the adaptation would need a second prompt", bench, cfg.ApprovalAboveMicros)
	}

	// Beat 4 must ESCALATE: above the approval threshold, but under both of the rules that
	// would deny it outright before the owner is ever asked.
	if premium <= cfg.ApprovalAboveMicros {
		t.Errorf("beat 4's bait is %d, at or under the %d approval threshold: it would auto-approve and there would be nothing to reject", premium, cfg.ApprovalAboveMicros)
	}
	if premium > cfg.PerTxLimitMicros {
		t.Errorf("beat 4's bait is %d, over the %d per-tx limit: it would be denied, not escalated", premium, cfg.PerTxLimitMicros)
	}
	if premium > cfg.JobBudgetMicros {
		t.Errorf("beat 4's bait is %d, over the %d job budget: it would be denied on budget, not escalated", premium, cfg.JobBudgetMicros)
	}
	if profile+bench > cfg.JobBudgetMicros {
		t.Errorf("beats 3 and 5 spend %d together, over the %d job budget", profile+bench, cfg.JobBudgetMicros)
	}

	// Budget headroom is a CUMULATIVE property, so assert it the way the engine sees it:
	// with beat 3 already committed, beat 5 must still auto-approve. Checking profile+bench
	// against JobBudgetMicros by hand would pass even if Evaluate counted committed spend
	// differently, which is the whole reason to ask the engine instead.
	afterProfile := SpendState{JobCommittedMicros: profile}
	in := wouldAutoApprove()
	in.AmountMicros = bench
	if got := Evaluate(cfg, afterProfile, in).Outcome; got != AutoApprove {
		t.Errorf("beat 5 gave %v with beat 3's %d already committed, want %v: the adaptation would stall mid-demo",
			got, profile, AutoApprove)
	}
	// And the bait still escalates rather than tipping into a budget denial at that point,
	// which is the case that turns beat 4 into a different beat entirely.
	in.AmountMicros = premium
	if got := Evaluate(cfg, afterProfile, in).Outcome; got != HumanApprovalRequired {
		t.Errorf("beat 4's bait gave %v with beat 3's %d already committed, want %v: the owner would never be asked",
			got, profile, HumanApprovalRequired)
	}

	// The engine is the authority, so drive the real evaluator rather than trusting the
	// inequalities above to stay equivalent to it.
	for _, tc := range []struct {
		name   string
		amount int64
		want   Outcome
		beat   string
	}{
		{"beat 3 profile", profile, AutoApprove, "the auto-approved source"},
		{"beat 4 premium", premium, HumanApprovalRequired, "the escalation bait"},
		{"beat 5 benchmark", bench, AutoApprove, "the cheaper alternative"},
	} {
		in := wouldAutoApprove()
		in.AmountMicros = tc.amount
		if got := Evaluate(cfg, SpendState{}, in).Outcome; got != tc.want {
			t.Errorf("%s (%s): Evaluate gave %v at %d micros, want %v", tc.name, tc.beat, got, tc.amount, tc.want)
		}
	}
}

// shellInt reads `name=<digits>` from the script, ignoring any trailing comment.
//
// The match is anchored through end-of-line on purpose. Without it `profile_micros=40000bogus`
// parses as 40000 and this test goes green over an assignment bash would not treat as a number
// at all -- the exact failure mode the test exists to catch, one level down.
func shellInt(src, name string) (int64, error) {
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `=(\d+)[ \t]*(?:#[^\r\n]*)?\r?$`)
	m := re.FindStringSubmatch(src)
	if m == nil {
		return 0, fmt.Errorf("scripts/spine_run no longer assigns %s=<digits>; if it was renamed, update this test with it", name)
	}
	return strconv.ParseInt(m[1], 10, 64)
}
