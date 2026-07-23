package discovery

import (
	"context"
	"reflect"
	"testing"
)

func find(t *testing.T, need string, capMicros int64) []Match {
	t.Helper()
	got, err := NewMatcher(V2StandIn()).Find(context.Background(), need, capMicros)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// The G10 done-criterion, pinned: the committed demo need selects the premium dataset
// BY DESCRIPTION — no merchant or resource name appears in the need — and with a
// margin, so a catalog or need-text edit that erodes the separation fails here instead
// of flipping a take on camera (scripted-mode guarantee, design pin 3).
func TestG10_DemoPrimaryFindsPremiumByDescription(t *testing.T) {
	got := find(t, DemoNeed, 0)
	if len(got) < 1 || got[0].Resource != "GET /v1/premium-dataset" {
		t.Fatalf("primary match: %+v, want the premium dataset first", got)
	}
	if got[0].Merchant == "" || got[0].AmountMicros != 4_000_000 {
		t.Fatalf("the match must carry everything a PurchaseRequest needs: %+v", got[0])
	}
	// Margin floor: the winner must beat the runner-up decisively, not by float luck.
	if len(got) > 1 && got[0].Score-got[1].Score < 0.05 {
		t.Fatalf("margin eroded: %.3f vs %.3f — the take is no longer guaranteed", got[0].Score, got[1].Score)
	}
	// The company profile shares no description token with the need: it must not appear.
	for _, m := range got {
		if m.Resource == "GET /v1/company-profile" {
			t.Fatalf("company profile matched a need it shares no language with: %+v", m)
		}
	}
}

// The cheaper re-query (the adaptation after the $4.00 rejection): a price cap below
// the rejected amount leaves the benchmark as the ONLY match — the profile scores zero
// against this need and the threshold keeps it out, so the beat lands on $0.06.
func TestG10_CheaperRequeryFindsBenchmarkOnly(t *testing.T) {
	got := find(t, DemoNeed, 3_999_999)
	if len(got) != 1 || got[0].Resource != "GET /v1/benchmark-summary" || got[0].AmountMicros != 60_000 {
		t.Fatalf("cheaper re-query: %+v, want exactly the benchmark summary", got)
	}
}

// Discovered alternatives may simply not exist (design addition 1): a cap below every
// affordable candidate returns EMPTY WITHOUT ERROR — the worker's source-abandoned
// path, a first-class honest outcome, not a crash and not a spin.
func TestG10_NothingAffordableIsEmptyNotError(t *testing.T) {
	if got := find(t, DemoNeed, 30_000); len(got) != 0 {
		t.Fatalf("cap below every candidate: %+v, want none", got)
	}
}

// The judge-types-their-own-need answer: language the catalog has never seen finds
// NOTHING — below-threshold matches are dropped, so a garbage need cannot buy the
// least-irrelevant service. No purchase is the correct result.
func TestG10_NonsenseNeedFindsNothing(t *testing.T) {
	if got := find(t, "quantum yoga catering for llamas", 0); len(got) != 0 {
		t.Fatalf("nonsense need matched: %+v", got)
	}
	if got := find(t, "", 0); len(got) != 0 {
		t.Fatalf("empty need matched: %+v", got)
	}
}

// Determinism is total: repeated calls are byte-identical, and ties cannot shift a
// take — equal scores order by price then resource, an explicit rule, not map luck.
func TestG10_DeterministicOrderingAndTieBreaks(t *testing.T) {
	a := find(t, DemoNeed, 0)
	b := find(t, DemoNeed, 0)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("same query, different results:\n%+v\n%+v", a, b)
	}

	// Two services with identical descriptions differ only in price: cheaper wins the
	// tie; identical price falls back to lexicographic resource.
	twins := Static{
		{Merchant: "m", Resource: "GET /v1/twin-b", Description: "identical twin data", AmountMicros: 200},
		{Merchant: "m", Resource: "GET /v1/twin-a", Description: "identical twin data", AmountMicros: 100},
		{Merchant: "m", Resource: "GET /v1/twin-c", Description: "identical twin data", AmountMicros: 100},
	}
	got, err := NewMatcher(twins).Find(context.Background(), "identical twin data", 0)
	if err != nil {
		t.Fatal(err)
	}
	order := []string{}
	for _, m := range got {
		order = append(order, m.Resource)
	}
	want := []string{"GET /v1/twin-a", "GET /v1/twin-c", "GET /v1/twin-b"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("tie-break order %v, want %v (cheaper first, then resource)", order, want)
	}
}

// Scores are cap-independent: the benchmark's score in the capped re-query equals its
// score in the unbounded query (IDF is computed over the full catalog, so a cap
// filters candidates — it never re-scores them).
func TestG10_CapFiltersWithoutRescoring(t *testing.T) {
	unbounded := find(t, DemoNeed, 0)
	capped := find(t, DemoNeed, 3_999_999)
	var benchUnbounded float64
	for _, m := range unbounded {
		if m.Resource == "GET /v1/benchmark-summary" {
			benchUnbounded = m.Score
		}
	}
	if benchUnbounded == 0 || capped[0].Score != benchUnbounded {
		t.Fatalf("cap changed a score: %.4f vs %.4f", capped[0].Score, benchUnbounded)
	}
}
