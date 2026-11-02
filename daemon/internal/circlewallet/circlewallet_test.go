package circlewallet

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/gnanam1990/snapfall/daemon/internal/chain"
)

// THROWAWAY values for the fake API — never real Circle credentials.
const (
	testAPIKey   = "INERT:not-a-circle-key:inert-fake-api-secret"
	testSecret   = "abababababababababababababababababababababababababababababababab" // 32 bytes, deliberately patterned so no scanner reads it as a key
	testWalletID = "8a1f6c2e-3b4d-4e5f-9a0b-1c2d3e4f5a6b"
	testAddr     = "0x00000000000000000000000000000000000000A1"
)

// fakeCircle serves the four endpoints the lane uses. Each transaction walks through
// states, one per GET, so the poll loop is exercised; final decides how it ends.
type fakeCircle struct {
	t    *testing.T
	priv *rsa.PrivateKey

	mu          sync.Mutex
	wallet      map[string]any
	states      []string // the GET sequence for every transaction
	txHash      string   // reported from SENT onward; empty = nothing was ever broadcast
	errorReason string
	createCode  int // non-zero: the create call fails with this HTTP status

	creates     []map[string]any
	ciphertexts []string
	polls       int
	authBad     bool
}

func newFakeCircle(t *testing.T) (*fakeCircle, *httptest.Server) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeCircle{
		t: t, priv: priv,
		wallet: map[string]any{
			"id": testWalletID, "address": testAddr, "blockchain": "ARC-TESTNET",
			"state": "LIVE", "custodyType": "DEVELOPER", "accountType": "EOA",
		},
		states: []string{"INITIATED", "SENT", "COMPLETE"},
		txHash: "0x" + strings.Repeat("ab", 32),
	}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeCircle) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+testAPIKey {
		f.authBad = true
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":401,"message":"Malformed authorization."}`))
		return
	}
	reply := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": v}) }
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/w3s/config/entity/publicKey":
		der, _ := x509.MarshalPKIXPublicKey(&f.priv.PublicKey)
		reply(map[string]any{"publicKey": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/w3s/wallets/"+testWalletID:
		reply(map[string]any{"wallet": f.wallet})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/w3s/developer/transactions/contractExecution":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.creates = append(f.creates, body)
		ct, _ := body["entitySecretCiphertext"].(string)
		f.ciphertexts = append(f.ciphertexts, ct)
		if f.createCode != 0 {
			w.WriteHeader(f.createCode)
			_, _ = w.Write([]byte(`{"code":156001,"message":"entity secret ciphertext was already used"}`))
			return
		}
		w.WriteHeader(http.StatusCreated)
		reply(map[string]any{"id": "c4d1da72-111e-4d52-bdbf-2e74a2d803d5", "state": "INITIATED"})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/w3s/transactions/"):
		i := f.polls
		if i >= len(f.states) {
			i = len(f.states) - 1
		}
		f.polls++
		st := f.states[i]
		tx := map[string]any{"id": strings.TrimPrefix(r.URL.Path, "/v1/w3s/transactions/"), "state": st}
		// Circle reports the hash from broadcast onward, so a SENT/STUCK/CANCELLED
		// transaction carries one too and the loop must not treat it as landed.
		if st != "INITIATED" && st != "CLEARED" && st != "QUEUED" && f.txHash != "" {
			tx["txHash"] = f.txHash
		}
		if f.errorReason != "" {
			tx["errorReason"] = f.errorReason
		}
		reply(map[string]any{"transaction": tx})
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

// fakeReader is the daemon's own RPC: it decides the outcome from the "receipt".
type fakeReader struct {
	mu       sync.Mutex
	reverted bool
	asked    []string
}

func (r *fakeReader) AwaitReceipt(_ context.Context, txHash string) (chain.Receipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, txHash)
	return chain.Receipt{TxHash: txHash, Block: 100, GasUsed: 21_000, Reverted: r.reverted}, nil
}

func openTest(t *testing.T, srv *httptest.Server, rd ReceiptReader) *Wallet {
	t.Helper()
	w, err := newWallet(srv.URL, testAPIKey, testSecret, testWalletID, 5_042_002, rd)
	if err != nil {
		t.Fatal(err)
	}
	w.pollEvery, w.deadline = time.Millisecond, 2*time.Second
	if err := w.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	return w
}

// The happy path, end to end: the request carries the wallet, target and calldata; every
// ciphertext decrypts to the entity secret and none is ever reused; the outcome comes from
// the chain reader, asked about exactly the hash Circle reported.
func TestSubmit_EncryptsFreshlyAndReadsTheOutcomeFromTheChain(t *testing.T) {
	f, srv := newFakeCircle(t)
	rd := &fakeReader{}
	w := openTest(t, srv, rd)
	if w.Address() != common.HexToAddress(testAddr) {
		t.Fatalf("address = %s, want the wallet's", w.Address().Hex())
	}
	to := common.HexToAddress("0x00000000000000000000000000000000000000F0")
	for i := 0; i < 2; i++ {
		f.mu.Lock()
		f.polls = 0
		f.mu.Unlock()
		rec, err := w.Submit(context.Background(), to, []byte{0xde, 0xad, 0xbe, 0xef})
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		if rec.Reverted || rec.TxHash != f.txHash {
			t.Fatalf("receipt %+v, want success for %s", rec, f.txHash)
		}
	}

	if len(f.creates) != 2 {
		t.Fatalf("creates = %d, want 2", len(f.creates))
	}
	keys := map[any]bool{}
	for i, body := range f.creates {
		if body["walletId"] != testWalletID || body["contractAddress"] != to.Hex() ||
			body["callData"] != "0xdeadbeef" || body["feeLevel"] != "MEDIUM" {
			t.Fatalf("create %d body: %v", i, body)
		}
		if _, both := body["abiFunctionSignature"]; both {
			t.Fatal("callData and abiFunctionSignature are mutually exclusive")
		}
		k, _ := body["idempotencyKey"].(string)
		if !isUUID(k) || k[14] != '4' {
			t.Fatalf("idempotency key %q is not a UUID v4", k)
		}
		keys[k] = true
		raw, err := base64.StdEncoding.DecodeString(f.ciphertexts[i])
		if err != nil {
			t.Fatal(err)
		}
		plain, err := rsa.DecryptOAEP(sha256.New(), nil, f.priv, raw, nil)
		if err != nil {
			t.Fatalf("ciphertext %d does not decrypt with RSA-OAEP/SHA-256: %v", i, err)
		}
		if fmt.Sprintf("%x", plain) != testSecret {
			t.Fatalf("ciphertext %d decrypts to the wrong secret", i)
		}
	}
	if len(keys) != 2 {
		t.Fatal("idempotency keys repeated across submissions")
	}
	if f.ciphertexts[0] == f.ciphertexts[1] {
		t.Fatal("a ciphertext was reused; Circle rejects that")
	}
	if len(rd.asked) != 2 || rd.asked[0] != f.txHash {
		t.Fatalf("the chain reader was asked %v, want the reported hash twice", rd.asked)
	}
}

// Mined is not succeeded: a status-0 receipt surfaces as Reverted with a nil error, even
// though Circle called the transaction COMPLETE. The chain decides, not Circle.
func TestSubmit_ChainRevertWinsOverCircleState(t *testing.T) {
	_, srv := newFakeCircle(t)
	w := openTest(t, srv, &fakeReader{reverted: true})
	rec, err := w.Submit(context.Background(), common.HexToAddress("0xBB"), []byte{0x01})
	if err != nil {
		t.Fatalf("a mined revert is not a submission failure: %v", err)
	}
	if !rec.Reverted {
		t.Fatal("status-0 receipt must surface as Reverted")
	}
}

// Arc can skip CONFIRMED (SENT -> COMPLETE), and FAILED with a hash is a mined outcome the
// receipt resolves. Both must reach the chain reader.
func TestSubmit_SkippedAndFailedStatesStillReadTheReceipt(t *testing.T) {
	for _, seq := range [][]string{{"SENT", "COMPLETE"}, {"QUEUED", "FAILED"}, {"CONFIRMED"}} {
		f, srv := newFakeCircle(t)
		f.states = seq
		rd := &fakeReader{reverted: seq[len(seq)-1] == "FAILED"}
		w := openTest(t, srv, rd)
		rec, err := w.Submit(context.Background(), common.HexToAddress("0xCC"), nil)
		if err != nil {
			t.Fatalf("%v: %v", seq, err)
		}
		if len(rd.asked) != 1 || rec.Reverted != rd.reverted {
			t.Fatalf("%v: receipt %+v asked %v", seq, rec, rd.asked)
		}
	}
}

// Nothing mined is a submission FAILURE, not a revert and not success: the chain reader is
// never consulted, and Circle's reason is passed on.
func TestSubmit_TerminalWithoutHashIsAFailureAndNeverReadsAReceipt(t *testing.T) {
	for _, final := range []string{"FAILED", "DENIED", "CANCELLED"} {
		f, srv := newFakeCircle(t)
		f.states, f.txHash, f.errorReason = []string{"INITIATED", final}, "", "INSUFFICIENT_NATIVE_TOKEN"
		rd := &fakeReader{}
		w := openTest(t, srv, rd)
		_, err := w.Submit(context.Background(), common.HexToAddress("0xDD"), nil)
		if err == nil || !strings.Contains(err.Error(), "INSUFFICIENT_NATIVE_TOKEN") {
			t.Fatalf("%s: %v, want a failure naming Circle's reason", final, err)
		}
		if len(rd.asked) != 0 {
			t.Fatalf("%s: the chain reader was asked about a transaction that never mined", final)
		}
	}
	// CANCELLED with a hash still never counts: the original hash may never mine.
	f, srv := newFakeCircle(t)
	f.states = []string{"SENT", "CANCELLED"}
	rd := &fakeReader{}
	w := openTest(t, srv, rd)
	if _, err := w.Submit(context.Background(), common.HexToAddress("0xDD"), nil); err == nil {
		t.Fatal("a cancelled transaction must not read as landed")
	}
	if len(rd.asked) != 0 {
		t.Fatal("a cancelled hash was treated as the outcome")
	}
}

// Unresolved at the deadline: an error that hands off to the restart oracle, never a guess.
func TestSubmit_StuckHandsOffToTheRestartOracle(t *testing.T) {
	f, srv := newFakeCircle(t)
	f.states = []string{"STUCK"}
	w := openTest(t, srv, &fakeReader{})
	w.deadline = 20 * time.Millisecond
	_, err := w.Submit(context.Background(), common.HexToAddress("0xEE"), nil)
	if err == nil || !strings.Contains(err.Error(), "restart oracle") {
		t.Fatalf("stuck: %v, want a hand-off to the restart oracle", err)
	}
}

// Open refuses every wallet that cannot be this lane.
func TestOpen_RefusesAWalletThatCannotBeTheTreasuryLane(t *testing.T) {
	cases := map[string]map[string]any{
		"wrong network": {"blockchain": "ARC"},
		"frozen":        {"state": "FROZEN"},
		"user custody":  {"custodyType": "ENDUSER"},
		"smart account": {"accountType": "SCA"},
		"bad address":   {"address": "0x123"},
	}
	for name, patch := range cases {
		f, srv := newFakeCircle(t)
		for k, v := range patch {
			f.wallet[k] = v
		}
		w, err := newWallet(srv.URL, testAPIKey, testSecret, testWalletID, 5_042_002, &fakeReader{})
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Open(context.Background()); err == nil {
			t.Fatalf("%s: Open accepted the wallet", name)
		}
		if _, err := w.Submit(context.Background(), common.HexToAddress("0xEE"), nil); err == nil {
			t.Fatalf("%s: an unopened lane submitted", name)
		}
	}
}

// SECRET DISCIPLINE: construction is fail-closed and names the variable, and no error the
// lane can produce (a refused construction, a rejected key, a rejected create) contains the
// API key or the entity secret.
func TestSecrets_FailClosedAndNeverEchoed(t *testing.T) {
	t.Setenv(EnvAPIKey, "")
	if _, err := NewFromEnv(5_042_002, &fakeReader{}); err == nil || !strings.Contains(err.Error(), EnvAPIKey) {
		t.Fatalf("missing api key: %v, want a named-variable refusal", err)
	}

	var errs []error
	for _, c := range []struct{ key, secret, wallet string }{
		{"no-colons-" + testSecret, testSecret, testWalletID},
		{testAPIKey, testSecret[:62], testWalletID},
		{testAPIKey, "zz" + testSecret[2:], testWalletID},
		{testAPIKey, testSecret, "not-a-wallet"},
	} {
		_, err := newWallet("http://x", c.key, c.secret, c.wallet, 5_042_002, &fakeReader{})
		if err == nil {
			t.Fatalf("malformed config %q accepted", c.wallet)
		}
		errs = append(errs, err)
	}
	if _, err := newWallet("http://x", testAPIKey, testSecret, testWalletID, 1, &fakeReader{}); err == nil {
		t.Fatal("a non-Arc chain id was accepted")
	}

	f, srv := newFakeCircle(t)
	bad, _ := newWallet(srv.URL, "WRONG:KEY:"+testSecret[:16], testSecret, testWalletID, 5_042_002, &fakeReader{})
	errs = append(errs, bad.Open(context.Background()))
	w := openTest(t, srv, &fakeReader{})
	f.createCode = http.StatusBadRequest
	_, err := w.Submit(context.Background(), common.HexToAddress("0xFF"), nil)
	errs = append(errs, err)

	for _, err := range errs {
		if err == nil {
			t.Fatal("expected an error")
		}
		for _, s := range []string{testSecret, testSecret[:16], "inert-fake-api-secret", f.ciphertexts[0]} {
			if strings.Contains(err.Error(), s) {
				t.Fatalf("an error echoed secret material: %v", err)
			}
		}
	}
	if !strings.Contains(errs[len(errs)-1].Error(), "156001") {
		t.Fatalf("Circle's error code should surface: %v", errs[len(errs)-1])
	}
}
