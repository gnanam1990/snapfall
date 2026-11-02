package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/gnanam1990/snapfall/daemon/internal/chain"
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

// fakeViews answers eth_call by (contract, selector): a uint, or a revert when the view is absent.
func fakeViews(t *testing.T, views map[string]int64) *chain.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Params []json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var call struct{ To, Data string }
		_ = json.Unmarshal(req.Params[0], &call)
		key := strings.ToLower(call.To) + ":" + call.Data[:10]
		v, ok := views[key]
		if !ok {
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"code":3,"message":"execution reverted"}}`)
			return
		}
		word := common.LeftPadBytes(big.NewInt(v).Bytes(), 32)
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":"0x%s"}`, hex.EncodeToString(word))
	}))
	t.Cleanup(srv.Close)
	c, err := chain.NewReadOnly(srv.URL, 5042002)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

var (
	testPool  = common.HexToAddress("0x00000000000000000000000000000000000000f1")
	testVault = common.HexToAddress("0x00000000000000000000000000000000000000f2")
)

func view(addr common.Address, calldata []byte) string {
	return strings.ToLower(addr.Hex()) + ":0x" + hex.EncodeToString(calldata[:4])
}

func TestReadControls(t *testing.T) {
	ctx := context.Background()

	// The Arc testnet pool today: no paused() at all, so there are no controls to check.
	got, err := readControls(ctx, fakeViews(t, map[string]int64{}), testPool, testVault, "0x10")
	if err != nil || got.Present {
		t.Fatalf("a pool that predates the controls: %+v, %v", got, err)
	}

	all := map[string]int64{
		view(testPool, chain.CalldataPaused()):           0,
		view(testPool, chain.CalldataMaxTotalExposure()): 50_000_000,
		view(testPool, chain.CalldataMaxAdvance()):       12_500_000,
		view(testPool, chain.CalldataTotalWrittenOff()):  27_500_000,
		view(testVault, chain.CalldataMaxJobPayment()):   25_000_000,
		view(testVault, chain.CalldataPaused()):          1,
	}
	got, err = readControls(ctx, fakeViews(t, all), testPool, testVault, "0x10")
	if err != nil {
		t.Fatal(err)
	}
	want := demoseed.Controls{Present: true, VaultPaused: true,
		MaxTotalExposure: big.NewInt(50_000_000), MaxAdvance: big.NewInt(12_500_000),
		TotalWrittenOff: big.NewInt(27_500_000), MaxJobPayment: big.NewInt(25_000_000)}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("controls = %+v, want %+v", got, want)
	}

	// paused() answers but a cap view reverts: a half-matching contract is an error, never "no controls".
	partial := map[string]int64{}
	for k, v := range all {
		partial[k] = v
	}
	delete(partial, view(testPool, chain.CalldataTotalWrittenOff()))
	if _, err := readControls(ctx, fakeViews(t, partial), testPool, testVault, "0x10"); err == nil ||
		!strings.Contains(err.Error(), "totalWrittenOff") {
		t.Fatalf("a pool missing totalWrittenOff was accepted: %v", err)
	}
}
