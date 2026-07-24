package chain

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// testKey is a THROWAWAY key for signing against the fake RPC — never a real wallet.
const testKey = "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"

// fakeRPC is a minimal JSON-RPC endpoint: serves chain id, nonces derived from the
// count of ACCEPTED transactions, and receipts. Addresses in revertFor get status-0
// receipts (mined-but-reverted).
type fakeRPC struct {
	mu        sync.Mutex
	chainID   string
	sent      []*types.Transaction
	revertFor map[common.Address]bool
	views     map[string]string // calldata-hex -> return-hex for eth_call
}

func (f *fakeRPC) handler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     any             `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	reply := func(v any) {
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": v})
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch req.Method {
	case "eth_chainId":
		reply(f.chainID)
	case "eth_getTransactionCount":
		reply(fmt.Sprintf("0x%x", len(f.sent)))
	case "eth_gasPrice":
		reply("0x3b9aca00")
	case "eth_estimateGas":
		reply("0x30000")
	case "eth_sendRawTransaction":
		var params []string
		_ = json.Unmarshal(req.Params, &params)
		tx := new(types.Transaction)
		if err := tx.UnmarshalBinary(common.FromHex(params[0])); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.sent = append(f.sent, tx)
		reply(tx.Hash().Hex())
	case "eth_getTransactionReceipt":
		var params []string
		_ = json.Unmarshal(req.Params, &params)
		for _, tx := range f.sent {
			if tx.Hash().Hex() == params[0] {
				status := "0x1"
				if tx.To() != nil && f.revertFor[*tx.To()] {
					status = "0x0"
				}
				reply(map[string]any{"status": status, "blockNumber": "0x64", "gasUsed": "0x5208"})
				return
			}
		}
		reply(nil)
	case "eth_call":
		var params []json.RawMessage
		_ = json.Unmarshal(req.Params, &params)
		var call struct {
			Data string `json:"data"`
		}
		_ = json.Unmarshal(params[0], &call)
		if out, ok := f.views[strings.ToLower(call.Data)]; ok {
			reply(out)
			return
		}
		reply("0x")
	default:
		http.Error(w, "unknown method "+req.Method, 400)
	}
}

func newFake(t *testing.T) (*fakeRPC, *Client) {
	t.Helper()
	f := &fakeRPC{chainID: "0x4cef52", revertFor: map[common.Address]bool{}, views: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	c, err := newFromKeyHex(testKey, srv.URL, 5_042_002, "TEST_KEY")
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

// PIN 3 — one signer, serial submissions: ten goroutines submit concurrently; every
// mined transaction must carry a DISTINCT nonce. Without the client's serialization,
// concurrent nonce fetches collide and this fails (mutation-verified).
func TestSubmit_ConcurrentCallersNeverCollideOnNonce(t *testing.T) {
	f, c := newFake(t)
	to := common.HexToAddress("0x00000000000000000000000000000000000000AA")
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Submit(context.Background(), to, []byte{0x01}); err != nil {
				t.Errorf("submit: %v", err)
			}
		}()
	}
	wg.Wait()
	seen := map[uint64]bool{}
	for _, tx := range f.sent {
		if seen[tx.Nonce()] {
			t.Fatalf("nonce %d used twice — submissions were not serialized", tx.Nonce())
		}
		seen[tx.Nonce()] = true
	}
	if len(seen) != 10 {
		t.Fatalf("distinct nonces = %d, want 10", len(seen))
	}
}

// PIN 2 — mined is not succeeded: a status-0 receipt returns Reverted=true with a nil
// error, a THIRD outcome distinct from success and from submission failure.
func TestSubmit_RevertIsDistinctFromSuccessAndFailure(t *testing.T) {
	f, c := newFake(t)
	revertAddr := common.HexToAddress("0x00000000000000000000000000000000000000BB")
	f.mu.Lock()
	f.revertFor[revertAddr] = true
	f.mu.Unlock()

	rec, err := c.Submit(context.Background(), revertAddr, []byte{0x02})
	if err != nil {
		t.Fatalf("a mined revert is not a submission failure: %v", err)
	}
	if !rec.Reverted {
		t.Fatal("status-0 receipt must surface as Reverted")
	}
	ok, err := c.Submit(context.Background(), common.HexToAddress("0xCC"), []byte{0x03})
	if err != nil || ok.Reverted {
		t.Fatalf("success path: %+v %v", ok, err)
	}
}

// A wrong chain id at the endpoint refuses BEFORE signing — the same posture as the
// indexer's chain verification.
func TestSubmit_WrongChainIDRefusesBeforeSigning(t *testing.T) {
	f, c := newFake(t)
	f.mu.Lock()
	f.chainID = "0x1"
	f.mu.Unlock()
	if _, err := c.Submit(context.Background(), common.HexToAddress("0xDD"), nil); err == nil ||
		!strings.Contains(err.Error(), "refusing to sign") {
		t.Fatalf("wrong chain id: %v, want a refusal before signing", err)
	}
	if len(f.sent) != 0 {
		t.Fatal("a transaction was signed and sent for the wrong chain")
	}
}

// KEY DISCIPLINE — errors name the VARIABLE, never the value; construction is
// fail-closed for missing and malformed keys.
func TestKey_FailClosedAndNeverEchoed(t *testing.T) {
	if _, err := NewFromEnv("CHAIN_TEST_UNSET_VAR", "http://x", 1); err == nil ||
		!strings.Contains(err.Error(), "CHAIN_TEST_UNSET_VAR") {
		t.Fatalf("missing key: %v, want a named-variable refusal", err)
	}
	secret := "0x" + strings.Repeat("ab", 31) // malformed (31 bytes)
	_, err := newFromKeyHex(secret, "http://x", 1, "MY_KEY_VAR")
	if err == nil {
		t.Fatal("malformed key must refuse")
	}
	if strings.Contains(err.Error(), "ab"+"ab"+"ab") {
		t.Fatalf("the error echoed key material: %v", err)
	}

	// And the oracle decoders round-trip.
	pr := append(append(word(big.NewInt(500_000).Bytes()), word(big.NewInt(10_000).Bytes())...), word(big.NewInt(1).Bytes())...)
	p, fee, open, err := DecodeOpenAdvance(pr)
	if err != nil || p.Int64() != 500_000 || fee.Int64() != 10_000 || !open {
		t.Fatalf("openAdvance decode: %v %v %v %v", p, fee, open, err)
	}
}

// The oracle answers from chain state: an advance row present (even CLOSED — settled
// advances have open=false but nonzero principal) means the advance HAPPENED; a job in
// Accepted means the settlement did.
func TestOracle_ChainStateIsAuthoritative(t *testing.T) {
	f, c := newFake(t)
	fp := common.HexToAddress("0x00000000000000000000000000000000000000F0")
	jv := common.HexToAddress("0x00000000000000000000000000000000000000F1")
	o := Oracle{Reader: c, FloatPool: fp, JobVault: jv}
	vaultID := "0x" + strings.Repeat("aa", 32)
	id, _ := JobID32(vaultID)

	// Advance exists but is CLOSED (settled): principal nonzero, open false.
	closed := "0x" + strings.Repeat("00", 29) + "07a120" + strings.Repeat("00", 30) + "2710" + strings.Repeat("00", 32)
	f.mu.Lock()
	f.views["0x"+strings.ToLower(fmt.Sprintf("%x", CalldataOpenAdvanceOf(id)))] = closed
	f.views["0x"+strings.ToLower(fmt.Sprintf("%x", CalldataJobStatus(id)))] = "0x" + strings.Repeat("00", 31) + "04"
	f.mu.Unlock()

	landed, err := o.AdvanceLanded(context.Background(), vaultID)
	if err != nil || !landed {
		t.Fatalf("a closed (settled) advance still HAPPENED: landed=%v err=%v", landed, err)
	}
	settled, err := o.SettlementLanded(context.Background(), vaultID)
	if err != nil || !settled {
		t.Fatalf("Accepted status must read as settled: %v %v", settled, err)
	}
}
