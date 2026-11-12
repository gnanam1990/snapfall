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
	"time"

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
	// unmined holds accepted transactions that have not mined yet: no receipt, and not
	// counted in the "latest" nonce. Empty (the default) means every send mines at once.
	unmined map[common.Hash]bool
	// holdNext, when set, puts every transaction sent from now on into unmined.
	holdNext bool
	// receiptReads counts eth_getTransactionReceipt calls, so a test can tell what ran
	// before the receipt wait began.
	receiptReads int
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
		var params []string
		_ = json.Unmarshal(req.Params, &params)
		n := len(f.sent)
		if len(params) > 1 && params[1] == "latest" {
			n -= len(f.unmined)
		}
		reply(fmt.Sprintf("0x%x", n))
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
		if f.holdNext {
			f.unmined[tx.Hash()] = true
		}
		reply(tx.Hash().Hex())
	case "eth_getTransactionReceipt":
		f.receiptReads++
		var params []string
		_ = json.Unmarshal(req.Params, &params)
		for _, tx := range f.sent {
			if tx.Hash().Hex() == params[0] {
				if f.unmined[tx.Hash()] {
					break
				}
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
	f := &fakeRPC{chainID: "0x4cef52", revertFor: map[common.Address]bool{}, views: map[string]string{},
		unmined: map[common.Hash]bool{}}
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
	org := common.HexToAddress("0x00000000000000000000000000000000000000F2")
	o := Oracle{Reader: c, FloatPool: fp, JobVault: jv, Org: org}
	vaultID := "0x" + strings.Repeat("aa", 32)
	id, _ := JobID32(vaultID)

	// Advance exists but is CLOSED (settled): principal nonzero, open false.
	closed := "0x" + strings.Repeat("00", 29) + "07a120" + strings.Repeat("00", 30) + "2710" + strings.Repeat("00", 32)
	f.mu.Lock()
	f.views["0x"+strings.ToLower(fmt.Sprintf("%x", CalldataOpenAdvanceOf(id)))] = closed
	f.views["0x"+strings.ToLower(fmt.Sprintf("%x", CalldataJobStatus(id)))] = "0x" + strings.Repeat("00", 31) + "04"
	f.views["0x"+strings.ToLower(fmt.Sprintf("%x", CalldataAdvanceRate(org)))] = "0x" + strings.Repeat("00", 30) + "1770"
	f.mu.Unlock()

	landed, err := o.AdvanceLanded(context.Background(), vaultID)
	if err != nil || !landed {
		t.Fatalf("a closed (settled) advance still HAPPENED: landed=%v err=%v", landed, err)
	}
	settled, err := o.SettlementLanded(context.Background(), vaultID)
	if err != nil || !settled {
		t.Fatalf("Accepted status must read as settled: %v %v", settled, err)
	}
	rate, err := o.AdvanceRateBps(context.Background())
	if err != nil || rate != 6_000 {
		t.Fatalf("advance rate = %d err=%v, want 6000", rate, err)
	}
}

// DeliveryAnchored is true only for Delivered, the one status acceptDelivery accepts; the
// settled terminal is not "delivered" for the purpose of submitting again.
func TestOracle_DeliveryAnchoredOnlyWhenDelivered(t *testing.T) {
	f, c := newFake(t)
	jv := common.HexToAddress("0x00000000000000000000000000000000000000F1")
	o := Oracle{Reader: c, JobVault: jv}
	vaultID := "0x" + strings.Repeat("bb", 32)
	id, _ := JobID32(vaultID)
	key := "0x" + strings.ToLower(fmt.Sprintf("%x", CalldataJobStatus(id)))
	for status, want := range map[string]bool{"02": false, "03": true, "04": false, "05": false} {
		f.mu.Lock()
		f.views[key] = "0x" + strings.Repeat("00", 31) + status
		f.mu.Unlock()
		got, err := o.DeliveryAnchored(context.Background(), vaultID)
		if err != nil || got != want {
			t.Fatalf("status %s: anchored=%v err=%v, want %v", status, got, err, want)
		}
	}
	if _, err := o.DeliveryAnchored(context.Background(), "not-a-job-id"); err == nil {
		t.Fatal("a malformed vault id must error, not read as undelivered")
	}
}

// A broadcast is reported BEFORE the receipt wait: when the wait then fails, the caller
// already holds the hash and nonce of a transaction that may still mine.
func TestSubmitTracked_ReportsTheBroadcastBeforeTheReceiptWait(t *testing.T) {
	f, c := newFake(t)
	f.mu.Lock()
	f.holdNext = true
	f.mu.Unlock()
	var got []Broadcast
	readsAtHook := -1
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	_, err := c.SubmitTracked(ctx, common.HexToAddress("0xAA"), []byte{0x01}, func(b Broadcast) {
		f.mu.Lock()
		readsAtHook = f.receiptReads
		f.mu.Unlock()
		got = append(got, b)
	})
	if err == nil {
		t.Fatal("a receipt wait that never saw a receipt returned no error")
	}
	if len(got) != 1 {
		t.Fatalf("broadcast hook ran %d times, want 1", len(got))
	}
	if readsAtHook != 0 {
		t.Fatalf("hook ran after %d receipt reads, want 0: it must run before the wait", readsAtHook)
	}
	f.mu.Lock()
	sent := f.sent[0]
	f.mu.Unlock()
	if got[0].TxHash != sent.Hash().Hex() || got[0].Nonce != sent.Nonce() {
		t.Fatalf("hook got %+v, want hash %s nonce %d", got[0], sent.Hash().Hex(), sent.Nonce())
	}

	// Submit (no hook) is unchanged and still works on the same client.
	f.mu.Lock()
	f.holdNext = false
	f.mu.Unlock()
	if _, err := c.Submit(context.Background(), common.HexToAddress("0xAB"), []byte{0x02}); err != nil {
		t.Fatalf("plain submit: %v", err)
	}
}

// Resolve reads one transaction's state without waiting: pending while it can still mine,
// mined (with the receipt's status) once it has, dropped once its nonce went to another
// transaction.
func TestResolve_PendingMinedRevertedDropped(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	revertAddr := common.HexToAddress("0x00000000000000000000000000000000000000BB")
	f.mu.Lock()
	f.holdNext = true
	f.revertFor[revertAddr] = true
	f.mu.Unlock()

	short := func() (context.Context, context.CancelFunc) { return context.WithTimeout(ctx, 600*time.Millisecond) }
	var ok, rev Broadcast
	c1, cancel1 := short()
	_, _ = c.SubmitTracked(c1, common.HexToAddress("0xAA"), []byte{0x01}, func(b Broadcast) { ok = b })
	cancel1()
	c2, cancel2 := short()
	_, _ = c.SubmitTracked(c2, revertAddr, []byte{0x02}, func(b Broadcast) { rev = b })
	cancel2()

	for _, b := range []Broadcast{ok, rev} {
		if st, _, err := c.Resolve(ctx, b); err != nil || st != TxPending {
			t.Fatalf("unmined %s: state=%v err=%v, want pending", b.TxHash, st, err)
		}
	}

	f.mu.Lock()
	f.unmined = map[common.Hash]bool{}
	f.holdNext = false
	f.mu.Unlock()
	if st, rec, err := c.Resolve(ctx, ok); err != nil || st != TxMined || rec.Reverted || rec.TxHash != ok.TxHash {
		t.Fatalf("mined: state=%v rec=%+v err=%v", st, rec, err)
	}
	if st, rec, err := c.Resolve(ctx, rev); err != nil || st != TxMined || !rec.Reverted {
		t.Fatalf("mined revert: state=%v rec=%+v err=%v", st, rec, err)
	}

	// Dropped: the transaction leaves the mempool, and the next submission takes its nonce
	// and mines. The first can never mine now.
	f.mu.Lock()
	f.holdNext = true
	f.mu.Unlock()
	var lost Broadcast
	c3, cancel3 := short()
	_, _ = c.SubmitTracked(c3, common.HexToAddress("0xAC"), []byte{0x03}, func(b Broadcast) { lost = b })
	cancel3()
	f.mu.Lock()
	f.sent = f.sent[:len(f.sent)-1]
	f.unmined = map[common.Hash]bool{}
	f.holdNext = false
	f.mu.Unlock()
	if st, _, err := c.Resolve(ctx, lost); err != nil || st != TxPending {
		t.Fatalf("gone from the mempool but nonce still open: state=%v err=%v, want pending", st, err)
	}
	if _, err := c.Submit(ctx, common.HexToAddress("0xAD"), []byte{0x04}); err != nil {
		t.Fatal(err)
	}
	if st, _, err := c.Resolve(ctx, lost); err != nil || st != TxDropped {
		t.Fatalf("nonce taken by another transaction: state=%v err=%v, want dropped", st, err)
	}
}

// Resolve fails closed: a read failure is an error, never a state, and a key-less client
// refuses outright.
func TestResolve_FailsClosed(t *testing.T) {
	f, c := newFake(t)
	f.mu.Lock()
	f.chainID = "0x1"
	f.mu.Unlock()
	if _, _, err := c.Resolve(context.Background(), Broadcast{TxHash: "0xab", Nonce: 0}); err == nil {
		t.Fatal("resolve on the wrong chain returned no error")
	}
	ro, err := NewReadOnly("http://127.0.0.1:1", 5_042_002)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ro.Resolve(context.Background(), Broadcast{TxHash: "0xab"}); err == nil {
		t.Fatal("a read-only client resolved a broadcast it has no nonce for")
	}
}
