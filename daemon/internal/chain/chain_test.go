package chain

import (
	"context"
	"encoding/json"
	"errors"
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

// fakeRPC is a minimal JSON-RPC endpoint: serves chain id, per-signer nonces, receipts and
// transaction lookups. Addresses in revertFor get status-0 receipts (mined-but-reverted).
//
// It mines the way a chain does: CONTIGUOUSLY per signer. A transaction mines only once
// every lower nonce from its signer has mined, so a later nonce never has a receipt while a
// lower one is held, and the "latest" nonce never counts past a gap. One transaction per
// (signer, nonce) is held: a send at a held nonce replaces it when its gas price is at least
// 10% higher ("replacement transaction underpriced" otherwise), and a send at a mined nonce
// is refused ("nonce too low").
type fakeRPC struct {
	mu        sync.Mutex
	chainID   string
	sent      []*types.Transaction
	revertFor map[common.Address]bool
	views     map[string]string // calldata-hex -> return-hex for eth_call
	// unmined holds accepted transactions that may not mine yet. Empty (the default) means
	// every send is mineable at once (still subject to contiguity).
	unmined map[common.Hash]bool
	// holdNext, when set, puts every transaction sent from now on into unmined.
	holdNext bool
	// receiptReads counts eth_getTransactionReceipt calls, so a test can tell what ran
	// before the receipt wait began.
	receiptReads int
	// head is what eth_blockNumber answers (0 reads as 100).
	head uint64
	// hiddenReceipt and hiddenTx model a load-balanced RPC whose receipt / transaction
	// backends lag its nonce backend: the transaction has mined (it counts in the "latest"
	// nonce) but those reads still answer null.
	hiddenReceipt map[common.Hash]bool
	hiddenTx      map[common.Hash]bool
	// rawSends counts every eth_sendRawTransaction, including rejected ones.
	rawSends int
	// ackLost, when set, makes the next eth_sendRawTransaction ACCEPT the transaction and
	// then answer with a non-JSON 504, as a send whose response timed out at a proxy would.
	ackLost bool
	// gasPrice and baseFee are what eth_gasPrice and the latest block's baseFeePerGas
	// answer ("" reads as 1 gwei).
	gasPrice, baseFee string
}

func senderOf(tx *types.Transaction) common.Address {
	from, err := types.Sender(types.LatestSignerForChainID(tx.ChainId()), tx)
	if err != nil {
		return common.Address{}
	}
	return from
}

// atNonce is the transaction the fake holds for addr at nonce, nil for none.
func (f *fakeRPC) atNonce(addr common.Address, nonce uint64) *types.Transaction {
	for _, tx := range f.sent {
		if tx.Nonce() == nonce && senderOf(tx) == addr {
			return tx
		}
	}
	return nil
}

// minedCount is addr's "latest" nonce: the length of the run of mineable transactions from
// nonce 0. A held or missing nonce stops the run, and nothing above it has mined.
func (f *fakeRPC) minedCount(addr common.Address) uint64 {
	n := uint64(0)
	for {
		tx := f.atNonce(addr, n)
		if tx == nil || f.unmined[tx.Hash()] {
			return n
		}
		n++
	}
}

// pendingCount is addr's "pending" nonce: the mined count plus the contiguous run of held
// transactions above it.
func (f *fakeRPC) pendingCount(addr common.Address) uint64 {
	n := f.minedCount(addr)
	for f.atNonce(addr, n) != nil {
		n++
	}
	return n
}

func (f *fakeRPC) mined(tx *types.Transaction) bool {
	return tx.Nonce() < f.minedCount(senderOf(tx))
}

func gwei(hexv string) string {
	if hexv == "" {
		return "0x3b9aca00"
	}
	return hexv
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
	rpcErr := func(msg string) {
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID,
			"error": map[string]any{"code": -32000, "message": msg}})
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch req.Method {
	case "eth_chainId":
		reply(f.chainID)
	case "eth_getTransactionCount":
		var params []string
		_ = json.Unmarshal(req.Params, &params)
		addr := common.HexToAddress(params[0])
		n := f.pendingCount(addr)
		if len(params) > 1 && params[1] == "latest" {
			n = f.minedCount(addr)
		}
		reply(fmt.Sprintf("0x%x", n))
	case "eth_blockNumber":
		h := f.head
		if h == 0 {
			h = 100
		}
		reply(fmt.Sprintf("0x%x", h))
	case "eth_getBlockByNumber":
		h := f.head
		if h == 0 {
			h = 100
		}
		reply(map[string]any{"number": fmt.Sprintf("0x%x", h), "baseFeePerGas": gwei(f.baseFee)})
	case "eth_getTransactionByHash":
		var params []string
		_ = json.Unmarshal(req.Params, &params)
		for _, tx := range f.sent {
			if strings.EqualFold(tx.Hash().Hex(), params[0]) && !f.hiddenTx[tx.Hash()] {
				var block any
				if f.mined(tx) {
					block = "0x64"
				}
				reply(map[string]any{"hash": tx.Hash().Hex(), "nonce": fmt.Sprintf("0x%x", tx.Nonce()), "blockNumber": block})
				return
			}
		}
		reply(nil)
	case "eth_gasPrice":
		reply(gwei(f.gasPrice))
	case "eth_estimateGas":
		reply("0x30000")
	case "eth_sendRawTransaction":
		var params []string
		_ = json.Unmarshal(req.Params, &params)
		f.rawSends++
		tx := new(types.Transaction)
		if err := tx.UnmarshalBinary(common.FromHex(params[0])); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		for _, prev := range f.sent {
			if prev.Hash() == tx.Hash() {
				rpcErr("already known")
				return
			}
		}
		from := senderOf(tx)
		if tx.Nonce() < f.minedCount(from) {
			rpcErr("nonce too low")
			return
		}
		if held := f.atNonce(from, tx.Nonce()); held != nil {
			// Geth's rule: a replacement must raise the price by at least 10%.
			floor := new(big.Int).Div(new(big.Int).Mul(held.GasPrice(), big.NewInt(110)), big.NewInt(100))
			if tx.GasPrice().Cmp(floor) < 0 {
				rpcErr("replacement transaction underpriced")
				return
			}
			kept := f.sent[:0]
			for _, s := range f.sent {
				if s != held {
					kept = append(kept, s)
				}
			}
			f.sent = kept
			delete(f.unmined, held.Hash())
		}
		f.sent = append(f.sent, tx)
		if f.holdNext {
			f.unmined[tx.Hash()] = true
		}
		if f.ackLost {
			f.ackLost = false
			http.Error(w, "upstream request timeout", http.StatusGatewayTimeout)
			return
		}
		reply(tx.Hash().Hex())
	case "eth_getTransactionReceipt":
		f.receiptReads++
		var params []string
		_ = json.Unmarshal(req.Params, &params)
		for _, tx := range f.sent {
			if tx.Hash().Hex() == params[0] {
				if !f.mined(tx) || f.hiddenReceipt[tx.Hash()] {
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
		unmined: map[common.Hash]bool{}, hiddenReceipt: map[common.Hash]bool{}, hiddenTx: map[common.Hash]bool{}}
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

// The write-ahead: the hook runs once the transaction is SIGNED and before the send (no
// eth_sendRawTransaction yet), and it carries exactly the transaction the node then holds.
// When the receipt wait later fails, the caller already holds the hash and nonce of a
// transaction that may still mine.
func TestSubmitTracked_RecordsTheTransactionBeforeTheSend(t *testing.T) {
	f, c := newFake(t)
	f.mu.Lock()
	f.holdNext = true
	f.mu.Unlock()
	var got []Broadcast
	sendsAtHook, readsAtHook := -1, -1
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	_, err := c.SubmitTracked(ctx, common.HexToAddress("0xAA"), []byte{0x01}, func(b Broadcast) error {
		f.mu.Lock()
		sendsAtHook, readsAtHook = f.rawSends, f.receiptReads
		f.mu.Unlock()
		got = append(got, b)
		return nil
	})
	if err == nil {
		t.Fatal("a receipt wait that never saw a receipt returned no error")
	}
	if len(got) != 1 {
		t.Fatalf("hook ran %d times, want 1", len(got))
	}
	if sendsAtHook != 0 || readsAtHook != 0 {
		t.Fatalf("hook ran after %d sends and %d receipt reads, want 0 and 0: it must run before the send", sendsAtHook, readsAtHook)
	}
	f.mu.Lock()
	sent := f.sent[0]
	f.mu.Unlock()
	if got[0].TxHash != sent.Hash().Hex() || got[0].Nonce != sent.Nonce() || got[0].SentHead != 100 {
		t.Fatalf("hook got %+v, want hash %s nonce %d sent at head 100", got[0], sent.Hash().Hex(), sent.Nonce())
	}

	// The held transaction mines; only then can the next nonce from this signer mine, so a
	// plain Submit (no hook) on the same client works.
	f.mu.Lock()
	f.holdNext = false
	f.unmined = map[common.Hash]bool{}
	f.mu.Unlock()
	if _, err := c.Submit(context.Background(), common.HexToAddress("0xAB"), []byte{0x02}); err != nil {
		t.Fatalf("plain submit: %v", err)
	}
}

// The reviewer's case: the node ACCEPTS eth_sendRawTransaction but the response is lost (a
// proxy timeout). The send errors, yet the transaction is out. It was recorded before the
// send, so Resolve finds it by hash: pending, then mined. It is never "not sent".
func TestSubmitTracked_SendAcceptedButUnacknowledgedIsResolvableByHash(t *testing.T) {
	f, c := newFake(t)
	f.mu.Lock()
	f.ackLost = true
	f.holdNext = true
	f.mu.Unlock()
	var b Broadcast
	_, err := c.SubmitTracked(context.Background(), common.HexToAddress("0xAA"), []byte{0x01}, func(x Broadcast) error {
		b = x
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "may still have reached the node") {
		t.Fatalf("lost send acknowledgement: err=%v, want a send error that says the tx may be out", err)
	}
	f.mu.Lock()
	n := len(f.sent)
	f.mu.Unlock()
	if b.TxHash == "" || n != 1 {
		t.Fatalf("recorded %+v, node holds %d: want the one recorded transaction out", b, n)
	}
	if r, err := c.Resolve(context.Background(), b); err != nil || r.State != TxPending {
		t.Fatalf("resolve after a lost ack: %+v err=%v, want pending", r, err)
	}
	f.mu.Lock()
	f.unmined = map[common.Hash]bool{}
	f.mu.Unlock()
	if r, err := c.Resolve(context.Background(), b); err != nil || r.State != TxMined {
		t.Fatalf("resolve once mined: %+v err=%v, want mined", r, err)
	}
}

// A write-ahead hook that fails sends nothing: no record, no transaction.
func TestSubmitTracked_FailedRecordSendsNothing(t *testing.T) {
	f, c := newFake(t)
	_, err := c.SubmitTracked(context.Background(), common.HexToAddress("0xAA"), []byte{0x01}, func(Broadcast) error {
		return fmt.Errorf("disk full")
	})
	if err == nil || !strings.Contains(err.Error(), "not sent") {
		t.Fatalf("err=%v, want a not-sent error", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rawSends != 0 || len(f.sent) != 0 {
		t.Fatalf("a transaction whose record failed reached the node (%d sends)", f.rawSends)
	}
}

// The fake mines contiguously per signer: with nonce 0 held, nonce 1 has no receipt and the
// mined nonce stays 0, so the earlier transaction is pending (known), never "dropped".
func TestFake_MinesContiguouslyPerSigner(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	f.mu.Lock()
	f.holdNext = true
	f.mu.Unlock()
	var first, second Broadcast
	heldSubmit(t, c, common.HexToAddress("0xAA"), []byte{0x01}, &first)
	f.mu.Lock()
	f.holdNext = false
	f.mu.Unlock()
	heldSubmit(t, c, common.HexToAddress("0xAB"), []byte{0x02}, &second)
	if second.Nonce != first.Nonce+1 {
		t.Fatalf("nonces %d then %d, want consecutive", first.Nonce, second.Nonce)
	}
	for _, b := range []Broadcast{first, second} {
		if r, err := c.Resolve(ctx, b); err != nil || r.State != TxPending {
			t.Fatalf("%d behind a held nonce: %+v err=%v, want pending", b.Nonce, r, err)
		}
	}
	f.mu.Lock()
	delete(f.unmined, common.HexToHash(first.TxHash))
	f.mu.Unlock()
	for _, b := range []Broadcast{first, second} {
		if r, err := c.Resolve(ctx, b); err != nil || r.State != TxMined {
			t.Fatalf("%d once the gap closed: %+v err=%v, want mined", b.Nonce, r, err)
		}
	}
}

// Resolve reads one transaction's state without waiting: pending while the node knows it,
// mined (with the receipt's status) once it has, evicted when the node lost it with its
// nonce still open, dropped once the node has no record and its nonce went to another
// transaction.
func TestResolve_PendingMinedRevertedEvictedDropped(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	revertAddr := common.HexToAddress("0x00000000000000000000000000000000000000BB")
	f.mu.Lock()
	f.holdNext = true
	f.revertFor[revertAddr] = true
	f.mu.Unlock()

	var ok, rev Broadcast
	heldSubmit(t, c, common.HexToAddress("0xAA"), []byte{0x01}, &ok)
	heldSubmit(t, c, revertAddr, []byte{0x02}, &rev)

	for _, b := range []Broadcast{ok, rev} {
		if r, err := c.Resolve(ctx, b); err != nil || r.State != TxPending || r.Head != 100 {
			t.Fatalf("unmined %s: %+v err=%v, want pending at head 100", b.TxHash, r, err)
		}
	}

	f.mu.Lock()
	f.unmined = map[common.Hash]bool{}
	f.holdNext = false
	f.mu.Unlock()
	if r, err := c.Resolve(ctx, ok); err != nil || r.State != TxMined || r.Receipt.Reverted || r.Receipt.TxHash != ok.TxHash {
		t.Fatalf("mined: %+v err=%v", r, err)
	}
	if r, err := c.Resolve(ctx, rev); err != nil || r.State != TxMined || !r.Receipt.Reverted {
		t.Fatalf("mined revert: %+v err=%v", r, err)
	}

	// The transaction leaves the mempool with its nonce still open: evicted, not pending,
	// so the caller can re-send the same bytes instead of waiting forever.
	f.mu.Lock()
	f.holdNext = true
	f.mu.Unlock()
	var lost Broadcast
	heldSubmit(t, c, common.HexToAddress("0xAC"), []byte{0x03}, &lost)
	f.mu.Lock()
	f.sent = f.sent[:len(f.sent)-1]
	f.unmined = map[common.Hash]bool{}
	f.holdNext = false
	f.mu.Unlock()
	if r, err := c.Resolve(ctx, lost); err != nil || r.State != TxEvicted {
		t.Fatalf("gone from the mempool, nonce still open: %+v err=%v, want evicted", r, err)
	}
	// The next submission takes its nonce and mines: the first can never mine now.
	if _, err := c.Submit(ctx, common.HexToAddress("0xAD"), []byte{0x04}); err != nil {
		t.Fatal(err)
	}
	if r, err := c.Resolve(ctx, lost); err != nil || r.State != TxDropped {
		t.Fatalf("nonce taken by another transaction: %+v err=%v, want dropped", r, err)
	}
}

// heldSubmit sends one transaction the fake holds unmined and captures its broadcast; the
// receipt wait is cut short on purpose.
func heldSubmit(t *testing.T, c *Client, to common.Address, data []byte, into *Broadcast) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	_, _ = c.SubmitTracked(ctx, to, data, func(b Broadcast) error { *into = b; return nil })
	if into.TxHash == "" {
		t.Fatal("the held submission never broadcast")
	}
}

// The reviewer's repro: a MINED transaction seen through a load-balanced RPC whose receipt
// backend lags its nonce backend. The nonce is past the transaction and the receipt read is
// null, which the old Resolve reported as dropped. The node still knows the transaction, so
// it is pending; and when the transaction lookup lags too, the one observation says dropped,
// which the caller only believes after a second look at a higher head (pinned in brain).
func TestResolve_LaggingReceiptIsNeverDroppedWhileTheNodeKnowsTheTx(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	var b Broadcast
	if _, err := c.SubmitTracked(ctx, common.HexToAddress("0xAA"), []byte{0x01}, func(x Broadcast) error { b = x; return nil }); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.hiddenReceipt[common.HexToHash(b.TxHash)] = true
	f.mu.Unlock()
	if r, err := c.Resolve(ctx, b); err != nil || r.State != TxPending {
		t.Fatalf("mined tx with a lagging receipt: %+v err=%v, want pending", r, err)
	}
	f.mu.Lock()
	f.hiddenTx[common.HexToHash(b.TxHash)] = true
	f.mu.Unlock()
	if r, err := c.Resolve(ctx, b); err != nil || r.State != TxDropped {
		t.Fatalf("receipt and lookup both lagging: %+v err=%v, want a single dropped observation", r, err)
	}
	f.mu.Lock()
	f.hiddenReceipt = map[common.Hash]bool{}
	f.mu.Unlock()
	if r, err := c.Resolve(ctx, b); err != nil || r.State != TxMined {
		t.Fatalf("receipt readable again: %+v err=%v, want mined", r, err)
	}
}

// Resolve compares the nonce of the address that SIGNED the broadcast, not this client's:
// a key rotated across a restart must not read another account's nonce as "moved past".
func TestResolve_ComparesTheRecordedSignersNonce(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	// The old key broadcast nonce 0, and the node lost it; the new key has mined three.
	old, err := newFromKeyHex("0x"+strings.Repeat("11", 32), c.rpcURL, 5_042_002, "OLD_KEY")
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.holdNext = true
	f.mu.Unlock()
	var lost Broadcast
	heldSubmit(t, old, common.HexToAddress("0xAA"), []byte{0x01}, &lost)
	f.mu.Lock()
	f.sent = f.sent[:len(f.sent)-1]
	f.unmined = map[common.Hash]bool{}
	f.holdNext = false
	f.mu.Unlock()
	for i := 0; i < 3; i++ {
		if _, err := c.Submit(ctx, common.HexToAddress("0xAB"), []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if lost.Signer != old.Address() {
		t.Fatalf("broadcast signer %s, want the signing key's %s", lost.Signer.Hex(), old.Address().Hex())
	}
	if r, err := c.Resolve(ctx, lost); err != nil || r.State != TxEvicted {
		t.Fatalf("resolved with the rotated key: %+v err=%v, want evicted (the OLD signer's nonce is still 0)", r, err)
	}
	// A read-only client resolves it too, since the record names the signer.
	ro, err := NewReadOnly(c.rpcURL, 5_042_002)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := ro.Resolve(ctx, lost); err != nil || r.State != TxEvicted {
		t.Fatalf("read-only resolve: %+v err=%v", r, err)
	}
}

// A broadcast whose record names no signer: the signer is recovered from its signed bytes,
// and with no bytes either an unseen transaction is ErrSignerUnknown, never dropped or
// evicted, even on a client whose own (possibly rotated) key has mined past that nonce.
func TestResolve_UnknownSignerIsNeverClassifiedUnseen(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	old, err := newFromKeyHex("0x"+strings.Repeat("11", 32), c.rpcURL, 5_042_002, "OLD_KEY")
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.holdNext = true
	f.mu.Unlock()
	var lost Broadcast
	heldSubmit(t, old, common.HexToAddress("0xAA"), []byte{0x01}, &lost)
	f.mu.Lock()
	f.sent = f.sent[:len(f.sent)-1]
	f.unmined = map[common.Hash]bool{}
	f.holdNext = false
	f.mu.Unlock()
	for i := 0; i < 2; i++ {
		if _, err := c.Submit(ctx, common.HexToAddress("0xAB"), []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	fromBytes := lost
	fromBytes.Signer = common.Address{}
	if r, err := c.Resolve(ctx, fromBytes); err != nil || r.State != TxEvicted {
		t.Fatalf("signer recovered from the bytes: %+v err=%v, want evicted (the OLD signer's nonce is 0)", r, err)
	}
	bare := Broadcast{TxHash: lost.TxHash, Nonce: lost.Nonce}
	if r, err := c.Resolve(ctx, bare); !errors.Is(err, ErrSignerUnknown) {
		t.Fatalf("no signer, no bytes: %+v err=%v, want ErrSignerUnknown (this client's key has mined past nonce 0)", r, err)
	}
}

// heldTx is the transaction the fake holds at hash h.
func heldTx(f *fakeRPC, h string) *types.Transaction {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, tx := range f.sent {
		if strings.EqualFold(tx.Hash().Hex(), h) {
			return tx
		}
	}
	return nil
}

// A transaction the node holds but never mines (underpriced) is replaced at the SAME nonce
// with a higher price: the fresh base fee when it is the highest, else the original's price
// bumped by a quarter. The record hook runs before the send. Once the replacement mines the
// original can never mine (dropped), and only one transaction ever mined at that nonce.
func TestReplace_SameNonceHigherFeeAndOnlyOneMines(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	to := common.HexToAddress("0xAA")
	f.mu.Lock()
	f.holdNext = true
	f.mu.Unlock()
	var stuck Broadcast
	heldSubmit(t, c, to, []byte{0x01}, &stuck)

	var recorded []Broadcast
	sendsAtHook := -1
	repl, err := c.Replace(ctx, to, []byte{0x01}, stuck, func(b Broadcast) error {
		f.mu.Lock()
		sendsAtHook = f.rawSends
		f.mu.Unlock()
		recorded = append(recorded, b)
		return nil
	})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if len(recorded) != 1 || recorded[0].TxHash != repl.TxHash || sendsAtHook != 1 {
		t.Fatalf("hook recorded %+v at send #%d, want the replacement before its own send", recorded, sendsAtHook)
	}
	old, nw := new(types.Transaction), heldTx(f, repl.TxHash)
	if err := old.UnmarshalBinary(stuck.Raw); err != nil || nw == nil {
		t.Fatalf("decode: %v, held replacement %v", err, nw)
	}
	want := new(big.Int).Add(new(big.Int).Div(new(big.Int).Mul(old.GasPrice(), big.NewInt(5)), big.NewInt(4)), big.NewInt(1))
	if repl.Nonce != stuck.Nonce || nw.Nonce() != stuck.Nonce || nw.GasPrice().Cmp(want) != 0 {
		t.Fatalf("replacement nonce %d price %s, want nonce %d price %s", nw.Nonce(), nw.GasPrice(), stuck.Nonce, want)
	}
	if heldTx(f, stuck.TxHash) != nil {
		t.Fatal("the node still holds the replaced transaction")
	}
	// The base fee rose above the bump: the next replacement pays the FRESH base fee.
	f.mu.Lock()
	f.baseFee = "0x174876e800" // 100 gwei
	f.mu.Unlock()
	repl2, err := c.Replace(ctx, to, []byte{0x01}, repl, nil)
	if err != nil {
		t.Fatalf("second replace: %v", err)
	}
	if tx := heldTx(f, repl2.TxHash); tx == nil || tx.GasPrice().Cmp(big.NewInt(100_000_000_000)) != 0 {
		t.Fatalf("second replacement %v, want the fresh 100 gwei base fee", tx)
	}

	// It mines; every earlier transaction at that nonce is now dropped.
	f.mu.Lock()
	f.unmined = map[common.Hash]bool{}
	f.holdNext = false
	f.mu.Unlock()
	if r, err := c.Resolve(ctx, repl2); err != nil || r.State != TxMined {
		t.Fatalf("replacement: %+v err=%v, want mined", r, err)
	}
	for _, b := range []Broadcast{stuck, repl} {
		if r, err := c.Resolve(ctx, b); err != nil || r.State != TxDropped {
			t.Fatalf("replaced %s: %+v err=%v, want dropped", b.TxHash, r, err)
		}
	}
	f.mu.Lock()
	n := len(f.sent)
	f.mu.Unlock()
	if n != 1 {
		t.Fatalf("node holds %d transactions at that nonce, want exactly one", n)
	}

	// Nothing left to replace: the nonce is mined.
	if _, err := c.Replace(ctx, to, []byte{0x01}, repl2, nil); !errors.Is(err, ErrNonceUsed) {
		t.Fatalf("replace of a mined nonce: err=%v, want ErrNonceUsed", err)
	}
}

// Replace refuses before signing anything: another key's broadcast, a failed record.
func TestReplace_RefusesForeignSignerAndFailedRecord(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	other, err := newFromKeyHex("0x"+strings.Repeat("11", 32), c.rpcURL, 5_042_002, "OTHER_KEY")
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.holdNext = true
	f.mu.Unlock()
	var foreign, mine Broadcast
	heldSubmit(t, other, common.HexToAddress("0xAA"), []byte{0x01}, &foreign)
	heldSubmit(t, c, common.HexToAddress("0xAA"), []byte{0x01}, &mine)
	f.mu.Lock()
	sends := f.rawSends
	f.mu.Unlock()
	if _, err := c.Replace(ctx, common.HexToAddress("0xAA"), []byte{0x01}, foreign, nil); !errors.Is(err, ErrSignerMismatch) {
		t.Fatalf("foreign signer: err=%v, want ErrSignerMismatch", err)
	}
	if _, err := c.Replace(ctx, common.HexToAddress("0xAA"), []byte{0x01}, mine, func(Broadcast) error {
		return fmt.Errorf("disk full")
	}); err == nil || !strings.Contains(err.Error(), "not sent") {
		t.Fatalf("failed record: err=%v, want not sent", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rawSends != sends {
		t.Fatalf("a refused replacement reached the node (%d sends)", f.rawSends-sends)
	}
}

// Rebroadcast re-sends the recorded bytes unchanged: the same hash at the same nonce, which
// then mines. Bytes that are not the recorded transaction are refused before anything is
// sent, and a node that still has it ("already known") is not an error.
func TestRebroadcast_SameBytesSameHashAndRefusesMismatch(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	f.mu.Lock()
	f.holdNext = true
	f.mu.Unlock()
	var lost, other Broadcast
	heldSubmit(t, c, common.HexToAddress("0xAA"), []byte{0x01}, &lost)
	f.mu.Lock()
	f.sent = f.sent[:len(f.sent)-1]
	f.unmined = map[common.Hash]bool{}
	f.holdNext = false
	f.mu.Unlock()
	if len(lost.Raw) == 0 {
		t.Fatal("the broadcast carries no signed bytes")
	}

	// Refusals: no bytes, bytes of another transaction, a different recorded signer.
	noBytes := lost
	noBytes.Raw = nil
	if err := c.Rebroadcast(ctx, noBytes); err == nil {
		t.Fatal("rebroadcast with no recorded bytes returned no error")
	}
	f.mu.Lock()
	f.holdNext = true
	f.mu.Unlock()
	heldSubmit(t, c, common.HexToAddress("0xAB"), []byte{0x02}, &other)
	f.mu.Lock()
	f.sent = f.sent[:len(f.sent)-1]
	f.unmined = map[common.Hash]bool{}
	f.holdNext = false
	sendsBefore := f.rawSends
	f.mu.Unlock()
	swapped := lost
	swapped.Raw = other.Raw
	if err := c.Rebroadcast(ctx, swapped); err == nil {
		t.Fatal("rebroadcast of another transaction's bytes returned no error")
	}
	wrongSigner := lost
	wrongSigner.Signer = common.HexToAddress("0x00000000000000000000000000000000000000CC")
	if err := c.Rebroadcast(ctx, wrongSigner); err == nil {
		t.Fatal("rebroadcast under a different recorded signer returned no error")
	}
	f.mu.Lock()
	if f.rawSends != sendsBefore {
		f.mu.Unlock()
		t.Fatalf("a refused rebroadcast reached the node (%d sends)", f.rawSends-sendsBefore)
	}
	f.mu.Unlock()

	if err := c.Rebroadcast(ctx, lost); err != nil {
		t.Fatalf("rebroadcast: %v", err)
	}
	if err := c.Rebroadcast(ctx, lost); err != nil {
		t.Fatalf("rebroadcast of a transaction the node already has: %v", err)
	}
	f.mu.Lock()
	n := len(f.sent)
	last := f.sent[n-1]
	f.mu.Unlock()
	if n != 1 || last.Hash().Hex() != lost.TxHash || last.Nonce() != lost.Nonce {
		t.Fatalf("node holds %d txs, last %s@%d; want exactly the original %s@%d", n, last.Hash().Hex(), last.Nonce(), lost.TxHash, lost.Nonce)
	}
	if r, err := c.Resolve(ctx, lost); err != nil || r.State != TxMined {
		t.Fatalf("after rebroadcast: %+v err=%v, want mined", r, err)
	}
}

// Resolve fails closed: a read failure is an error, never a state, and a key-less client
// refuses a broadcast that names no signer.
func TestResolve_FailsClosed(t *testing.T) {
	f, c := newFake(t)
	f.mu.Lock()
	f.chainID = "0x1"
	f.mu.Unlock()
	if _, err := c.Resolve(context.Background(), Broadcast{TxHash: "0xab", Nonce: 0}); err == nil {
		t.Fatal("resolve on the wrong chain returned no error")
	}
	ro, err := NewReadOnly("http://127.0.0.1:1", 5_042_002)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ro.Resolve(context.Background(), Broadcast{TxHash: "0xab"}); err == nil {
		t.Fatal("a read-only client resolved a broadcast that names no signer")
	}
}
