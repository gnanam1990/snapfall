package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/gnanam1990/snapfall/daemon/internal/demoseed"
)

// A fresh job id per run is what makes reset -> spine -> reset -> spine work, because
// JobVault.createJob reverts JobExists on a reused id and the chain has no delete. The original
// second-granularity timestamp collided for two takes started in the same second (review: #41).
func TestNewRunIDIsUniqueWithinTheSameSecond(t *testing.T) {
	const n = 500
	seen := make(map[string]bool, n)
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id, err := newRunID()
		if err != nil {
			t.Fatal(err)
		}
		if seen[id] {
			t.Fatalf("newRunID returned a duplicate after %d calls: %q", i, id)
		}
		seen[id] = true
		ids = append(ids, id)
	}

	// These all ran far faster than a second, so the old format would have produced one value.
	if len(seen) != n {
		t.Fatalf("got %d distinct ids out of %d", len(seen), n)
	}

	// The derived job ids must be distinct too, which is what the chain actually sees.
	jobs := make(map[[32]byte]bool, n)
	for _, id := range ids {
		j := demoseed.JobID(id)
		if jobs[j] {
			t.Fatalf("two run ids derived the same vault job id: %q", id)
		}
		jobs[j] = true
	}

	// Shape: nanosecond timestamp plus a 4-byte hex nonce.
	re := regexp.MustCompile(`^\d{8}T\d{6}\.\d{9}Z-[0-9a-f]{8}$`)
	if !re.MatchString(ids[0]) {
		t.Fatalf("run id %q does not match the expected nanosecond+nonce shape", ids[0])
	}
	// Prove the nonce is doing work: strip it and the timestamps may still collide, but with it
	// they must not.
	stripped := make(map[string]bool)
	collisions := 0
	for _, id := range ids {
		base := id[:strings.LastIndex(id, "-")]
		if stripped[base] {
			collisions++
		}
		stripped[base] = true
	}
	t.Logf("timestamp-only collisions among %d ids: %d (the nonce absorbs these)", n, collisions)
}
