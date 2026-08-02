package ownerapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// The daemon half of the H2 Overview aggregate wire contract.
//
// This is the regression the principalAtomic-vs-principalUsdc bug needed: the aggregate sent
// keys the dashboard did not read, nothing was red, and the column silently blanked. Here the
// daemon's OWN structs are marshaled and their JSON keys are asserted to match, exactly, the
// `emits` set in docs/handshakes/h2-overview-aggregates.contract.json — the single source of
// truth the dashboard's aggregate-contract.test.ts binds to its DTOs with keyof. Rename a JSON
// tag here without updating the contract (and the DTO) and this fails, not a demo three days on.
func TestAggregateWireContract(t *testing.T) {
	path := filepath.Join("..", "..", "..", "docs", "handshakes", "h2-overview-aggregates.contract.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read contract: %v", err)
	}
	var contract struct {
		Emits map[string][]string `json:"emits"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatalf("parse contract: %v", err)
	}

	// The nested money shapes, marshaled as the wire carries them. Zero values are fine — this
	// asserts KEYS, not values, and none of these structs use omitempty.
	cases := []struct {
		name string
		v    any
	}{
		{"pool", poolAgg{}},
		{"openAdvance", openAdvance{}},
		{"activeJob", activeJob{}},
	}
	for _, c := range cases {
		want, ok := contract.Emits[c.name]
		if !ok {
			t.Fatalf("contract has no emits entry for %q", c.name)
		}
		got := jsonKeys(t, c.v)
		sort.Strings(want)
		if !equalStrings(got, want) {
			t.Errorf("%s wire keys = %v, contract emits %v\n"+
				"The dashboard reads the contract keys; a mismatch blanks that column. "+
				"Change aggregates.go and the contract (and the DTO) together.", c.name, got, want)
		}
	}
}

func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
