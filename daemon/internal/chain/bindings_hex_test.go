package chain

import (
	"strings"
	"testing"
)

// JobID32 FAILS CLOSED on non-hex input — common.FromHex would have returned the zero
// id with no error (review: PR #36).
func TestJobID32_RejectsNonHex(t *testing.T) {
	if _, err := JobID32("0x" + strings.Repeat("g", 64)); err == nil {
		t.Fatal("non-hex vault id accepted; want an error")
	}
	// A valid id still parses.
	if id, err := JobID32("0x" + strings.Repeat("ab", 32)); err != nil || id[0] != 0xab {
		t.Fatalf("valid id failed to parse: %x %v", id, err)
	}
}
