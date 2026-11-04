package circlewallet

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeAdmin is Circle's setup surface: register, wallet sets, wallets, balances. It
// decrypts every ciphertext so a test can prove what was sent without trusting the client.
type fakeAdmin struct {
	t    *testing.T
	priv *rsa.PrivateKey

	mu            sync.Mutex
	wallets       []WalletInfo
	sets          int
	creates       int
	plaintexts    [][]byte
	ciphertexts   map[string]bool
	idemKeys      map[string]bool
	createdAs     string // accountType the fake reports for new wallets ("" = what was asked)
	createdID     string // id the fake reports for new wallets ("" = a real UUID)
	noRecovery    bool
	registerCode  int
	ignoreFilters bool
	mismatch      bool // answer a wallet read with some other wallet
	bodies        []map[string]any
}

func newFakeAdmin(t *testing.T) (*fakeAdmin, *Admin) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeAdmin{t: t, priv: priv, ciphertexts: map[string]bool{}, idemKeys: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	a, err := newAdmin(srv.URL, testAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	return f, a
}

// open decrypts a ciphertext and records it; a reused ciphertext fails the test, as Circle
// would refuse it.
func (f *fakeAdmin) open(ct string) {
	raw, err := base64.StdEncoding.DecodeString(ct)
	if err != nil {
		f.t.Errorf("ciphertext is not base64")
		return
	}
	pt, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, f.priv, raw, nil)
	if err != nil {
		f.t.Errorf("ciphertext does not decrypt under the entity public key")
		return
	}
	if f.ciphertexts[ct] {
		f.t.Errorf("a ciphertext was reused")
	}
	f.ciphertexts[ct] = true
	f.plaintexts = append(f.plaintexts, pt)
}

func (f *fakeAdmin) idem(body map[string]any) {
	k, _ := body["idempotencyKey"].(string)
	if !isUUID(k) || f.idemKeys[k] {
		f.t.Errorf("idempotency key %q is not a fresh UUID", k)
	}
	f.idemKeys[k] = true
}

func (f *fakeAdmin) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+testAPIKey {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	reply := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": v}) }
	var body map[string]any
	if r.Method == http.MethodPost {
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.bodies = append(f.bodies, body)
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/w3s/config/entity/publicKey":
		der, _ := x509.MarshalPKIXPublicKey(&f.priv.PublicKey)
		reply(map[string]any{"publicKey": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/w3s/config/entity/entitySecret":
		f.open(body["entitySecretCiphertext"].(string))
		if f.registerCode != 0 {
			w.WriteHeader(f.registerCode)
			_, _ = w.Write([]byte(`{"code":156015,"message":"entity secret has already been set"}`))
			return
		}
		if f.noRecovery {
			reply(map[string]any{})
			return
		}
		reply(map[string]any{"recoveryFile": "RECOVERY-FILE-CONTENT"})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/w3s/developer/walletSets":
		f.open(body["entitySecretCiphertext"].(string))
		f.idem(body)
		f.sets++
		reply(map[string]any{"walletSet": map[string]any{"id": fmt.Sprintf("00000000-0000-4000-8000-%012d", f.sets), "custodyType": "DEVELOPER"}})
	case r.Method == http.MethodPost && r.URL.Path == "/v1/w3s/developer/wallets":
		f.open(body["entitySecretCiphertext"].(string))
		f.idem(body)
		f.creates++
		md := body["metadata"].([]any)[0].(map[string]any)
		acct := body["accountType"].(string)
		if f.createdAs != "" {
			acct = f.createdAs
		}
		nw := WalletInfo{
			ID:         fmt.Sprintf("11111111-0000-4000-8000-%012d", len(f.wallets)+1),
			Address:    fmt.Sprintf("0x%040x", len(f.wallets)+1),
			Blockchain: body["blockchains"].([]any)[0].(string),
			State:      "LIVE", CustodyType: "DEVELOPER", AccountType: acct,
			WalletSetID: body["walletSetId"].(string),
			Name:        md["name"].(string), RefID: md["refId"].(string),
		}
		if f.createdID != "" {
			nw.ID = f.createdID
		}
		f.wallets = append(f.wallets, nw)
		reply(map[string]any{"wallets": []WalletInfo{nw}})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/w3s/wallets":
		var out []WalletInfo
		for _, wl := range f.wallets {
			if f.ignoreFilters || (wl.Blockchain == r.URL.Query().Get("blockchain") && wl.RefID == r.URL.Query().Get("refId")) {
				out = append(out, wl)
			}
		}
		reply(map[string]any{"wallets": out})
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/balances"):
		reply(map[string]any{"tokenBalances": []map[string]any{{"amount": "12.5", "token": map[string]any{"symbol": "USDC", "isNative": true}}}})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/w3s/wallets/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/w3s/wallets/")
		for _, wl := range f.wallets {
			if wl.ID == id {
				if f.mismatch {
					wl.ID = "66666666-0000-4000-8000-000000000001"
				}
				reply(map[string]any{"wallet": wl})
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":156004,"message":"Cannot find target wallet"}`))
	default:
		http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
	}
}

func testSecretBytes(t *testing.T) []byte {
	t.Helper()
	b, err := hex.DecodeString(testSecret)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Registration sends the secret encrypted under Circle's key and hands back the recovery
// file; a reply without one is NOT success, and Circle's refusal is passed through.
func TestRegisterEntitySecret(t *testing.T) {
	f, a := newFakeAdmin(t)
	secret := testSecretBytes(t)
	rec, err := a.RegisterEntitySecret(context.Background(), secret)
	if err != nil || rec != "RECOVERY-FILE-CONTENT" {
		t.Fatalf("register = %q, %v", rec, err)
	}
	if len(f.plaintexts) != 1 || !bytes.Equal(f.plaintexts[0], secret) {
		t.Fatalf("Circle did not receive the secret under its key")
	}

	f.noRecovery = true
	if _, err := a.RegisterEntitySecret(context.Background(), secret); err == nil || !strings.Contains(err.Error(), "no recovery file") {
		t.Fatalf("a registration without a recovery file must fail, got %v", err)
	}
	f.noRecovery, f.registerCode = false, http.StatusConflict
	_, err = a.RegisterEntitySecret(context.Background(), secret)
	var he *HTTPError
	if err == nil || !strings.Contains(err.Error(), "already been set") || !errors.As(err, &he) || he.Status != http.StatusConflict {
		t.Fatalf("Circle's refusal must pass through as an HTTPError, got %v", err)
	}
	if strings.Contains(err.Error(), testSecret) || strings.Contains(err.Error(), testAPIKey) {
		t.Fatalf("error echoes a secret: %v", err)
	}
	if _, err := a.RegisterEntitySecret(context.Background(), secret[:31]); err == nil {
		t.Fatalf("a short secret must be refused before any request")
	}
}

// A fresh account gets one wallet set and one EOA per role, each request with a fresh
// ciphertext of the right secret and a fresh idempotency key.
func TestEnsureWallets_CreatesOneSetAndOneEOAPerRole(t *testing.T) {
	f, a := newFakeAdmin(t)
	secret := testSecretBytes(t)
	got, err := a.EnsureWallets(context.Background(), secret, "ARC-TESTNET", Roles)
	if err != nil {
		t.Fatal(err)
	}
	if f.sets != 1 || f.creates != 2 || len(got) != 2 {
		t.Fatalf("sets=%d creates=%d results=%d, want 1, 2, 2", f.sets, f.creates, len(got))
	}
	for i, p := range got {
		if !p.Created || p.Role != Roles[i] || p.Wallet.RefID != Roles[i].RefID || p.Wallet.AccountType != "EOA" || p.Wallet.Blockchain != "ARC-TESTNET" {
			t.Fatalf("result %d = %+v", i, p)
		}
		if p.Wallet.WalletSetID != got[0].Wallet.WalletSetID {
			t.Fatalf("roles landed in different wallet sets")
		}
	}
	for _, b := range f.bodies {
		if b["blockchains"] != nil && (b["accountType"] != "EOA" || b["count"].(float64) != 1) {
			t.Fatalf("wallet request %v is not one EOA", b)
		}
	}
	for _, pt := range f.plaintexts {
		if !bytes.Equal(pt, secret) {
			t.Fatalf("a request carried a different secret")
		}
	}
}

// Running it again creates nothing; with one role already present, only the other is
// created, in the same set.
func TestEnsureWallets_IsSafeToRerun(t *testing.T) {
	f, a := newFakeAdmin(t)
	secret := testSecretBytes(t)
	first, err := a.EnsureWallets(context.Background(), secret, "ARC-TESTNET", Roles[:1])
	if err != nil {
		t.Fatal(err)
	}
	both, err := a.EnsureWallets(context.Background(), secret, "ARC-TESTNET", Roles)
	if err != nil {
		t.Fatal(err)
	}
	if f.sets != 1 || f.creates != 2 {
		t.Fatalf("sets=%d creates=%d, want 1 set and 2 wallets in all", f.sets, f.creates)
	}
	if both[0].Created || both[0].Wallet.ID != first[0].Wallet.ID || !both[1].Created || both[1].Wallet.WalletSetID != first[0].Wallet.WalletSetID {
		t.Fatalf("second run = %+v", both)
	}
	again, err := a.EnsureWallets(context.Background(), secret, "ARC-TESTNET", Roles)
	if err != nil || f.creates != 2 || again[0].Created || again[1].Created {
		t.Fatalf("third run created something: creates=%d %+v %v", f.creates, again, err)
	}
	// The same refIds on the other network are different wallets.
	if _, err := a.EnsureWallets(context.Background(), secret, "ARC", Roles); err != nil || f.creates != 4 {
		t.Fatalf("mainnet wallets: creates=%d %v", f.creates, err)
	}
}

// What it will not do: guess between two wallets under one refId, adopt an existing wallet
// the lane would refuse, accept a new wallet that came back as an SCA, or trust a server
// that ignored the filter.
func TestEnsureWallets_Refusals(t *testing.T) {
	secret := testSecretBytes(t)
	ctx := context.Background()

	f, a := newFakeAdmin(t)
	dup := WalletInfo{Blockchain: "ARC-TESTNET", RefID: "snapfall-treasury", State: "LIVE", CustodyType: "DEVELOPER", AccountType: "EOA", Address: "0x00000000000000000000000000000000000000a1"}
	dup.ID = "22222222-0000-4000-8000-000000000001"
	f.wallets = append(f.wallets, dup)
	dup.ID = "22222222-0000-4000-8000-000000000002"
	f.wallets = append(f.wallets, dup)
	if _, err := a.EnsureWallets(ctx, secret, "ARC-TESTNET", Roles); err == nil || !strings.Contains(err.Error(), "2 ARC-TESTNET wallets carry refId") || f.creates+f.sets != 0 {
		t.Fatalf("duplicate refId: err=%v creates=%d sets=%d", err, f.creates, f.sets)
	}

	f, a = newFakeAdmin(t)
	sca := WalletInfo{ID: "33333333-0000-4000-8000-000000000001", Blockchain: "ARC-TESTNET", RefID: "snapfall-relayer", State: "LIVE", CustodyType: "DEVELOPER", AccountType: "SCA", Address: "0x00000000000000000000000000000000000000b2"}
	f.wallets = append(f.wallets, sca)
	if _, err := a.EnsureWallets(ctx, secret, "ARC-TESTNET", Roles); err == nil || !strings.Contains(err.Error(), "existing snapfall-relayer wallet") || f.creates+f.sets != 0 {
		t.Fatalf("existing SCA: err=%v creates=%d sets=%d", err, f.creates, f.sets)
	}

	f, a = newFakeAdmin(t)
	f.createdAs = "SCA"
	if _, err := a.EnsureWallets(ctx, secret, "ARC-TESTNET", Roles); err == nil || !strings.Contains(err.Error(), "new snapfall-treasury wallet") || f.creates != 1 {
		t.Fatalf("new wallet came back SCA: err=%v creates=%d", err, f.creates)
	}

	f, a = newFakeAdmin(t)
	f.createdID = "not-a-uuid"
	if _, err := a.EnsureWallets(ctx, secret, "ARC-TESTNET", Roles); err == nil || !strings.Contains(err.Error(), "not a wallet id") {
		t.Fatalf("a created wallet without a wallet id was reported: %v", err)
	}

	f, a = newFakeAdmin(t)
	f.ignoreFilters = true
	f.wallets = append(f.wallets, WalletInfo{ID: "44444444-0000-4000-8000-000000000001", Blockchain: "ETH-SEPOLIA", RefID: "someone-else", State: "LIVE", CustodyType: "DEVELOPER", AccountType: "EOA", Address: "0x00000000000000000000000000000000000000c3"})
	got, err := a.EnsureWallets(ctx, secret, "ARC-TESTNET", Roles)
	if err != nil || !got[0].Created || !got[1].Created {
		t.Fatalf("a stranger returned by an unfiltered list was adopted: %+v %v", got, err)
	}

	if _, err := a.EnsureWallets(ctx, secret[:16], "ARC-TESTNET", Roles); err == nil {
		t.Fatalf("a short secret must be refused")
	}
}

func TestAdminReads(t *testing.T) {
	f, a := newFakeAdmin(t)
	ctx := context.Background()
	if err := a.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	f.wallets = append(f.wallets, WalletInfo{ID: testWalletID, Blockchain: "ARC-TESTNET", State: "LIVE", CustodyType: "DEVELOPER", AccountType: "EOA", Address: testAddr})
	w, err := a.Wallet(ctx, testWalletID)
	if err != nil || CheckLane(w, "ARC-TESTNET") != nil || CheckLane(w, "ARC") == nil {
		t.Fatalf("wallet read/check: %+v %v", w, err)
	}
	if _, err := a.Wallet(ctx, "not-a-uuid"); err == nil {
		t.Fatalf("a non-UUID id must be refused before any request")
	}
	if _, err := a.Wallet(ctx, "55555555-0000-4000-8000-000000000009"); err == nil || !strings.Contains(err.Error(), "Cannot find target wallet") {
		t.Fatalf("unknown wallet: %v", err)
	}
	bal, err := a.Balances(ctx, testWalletID)
	if err != nil || len(bal) != 1 || bal[0].Amount != "12.5" || !bal[0].Token.IsNative {
		t.Fatalf("balances = %+v, %v", bal, err)
	}
	// A wallet answer for some other id is refused, not believed.
	f.mismatch = true
	if _, err := a.Wallet(ctx, testWalletID); err == nil || !strings.Contains(err.Error(), "Circle answered with") {
		t.Fatalf("a wallet read answered with another wallet was believed: %v", err)
	}
}

func TestKeyEnvironment(t *testing.T) {
	for in, want := range map[string]string{
		"TEST_API_KEY:abc:def": "testnet",
		"LIVE_API_KEY:abc:def": "mainnet",
		"SAND_API_KEY:abc:def": "",
		testAPIKey:             "",
		"":                     "",
	} {
		if got := KeyEnvironment(in); got != want {
			t.Errorf("KeyEnvironment(%q) = %q, want %q", in, got, want)
		}
	}
}
