// Package circlewallet signs the daemon's operator transactions through a Circle
// Developer-Controlled Wallet instead of a raw private key in the environment.
//
// WHAT CHANGES AND WHAT DOES NOT: the raw-key lane (chain.Client) holds a secp256k1 key in
// process memory, read from a shell file. Here the key is an MPC key share held by Circle,
// and the daemon holds an API key plus the entity secret that authorizes each request. That
// is a custody move, not custody removal: whoever holds the API key AND the entity secret
// can still sign anything this wallet can. It is DEVELOPER custody, stated openly, and it is
// for the operator's own treasury only. Customer money never goes through it.
//
// THE CHAIN STAYS THE ORACLE: Circle signs and broadcasts; it does not decide the outcome.
// Once Circle reports a transaction hash, the receipt is read from the daemon's own RPC
// (chain.Client.AwaitReceipt) and its STATUS decides success vs Reverted, the same
// discipline as the raw-key lane. A Circle state of FAILED/DENIED/CANCELLED with nothing
// mined is a submission failure (nothing landed); anything unresolved at the deadline is
// left to the restart oracle, exactly as a missing receipt is today.
//
// EOA ONLY, FAIL-CLOSED: for a smart-contract account the hash Circle reports is the
// bundler's handleOps transaction, which can mine with status 1 while the inner call
// reverted. Receipt status would then report a revert as success, so an SCA wallet is a
// refusal at construction, not a caveat at use.
//
// SECRET DISCIPLINE (the owner-token standard): the API key and entity secret enter from
// ENV ONLY, are format-checked at construction, are never logged, and never appear in an
// error. The entity secret is encrypted afresh for every request (Circle rejects a reused
// ciphertext).
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
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/gnanam1990/snapfall/daemon/internal/chain"
)

// The three environment variables a Circle lane needs. The names are exported so the
// daemon's wiring can say which one is missing without restating them.
const (
	EnvAPIKey       = "CIRCLE_API_KEY"
	EnvEntitySecret = "CIRCLE_ENTITY_SECRET"
	EnvWalletID     = "CIRCLE_TREASURY_WALLET_ID"
)

const defaultBaseURL = "https://api.circle.com"

// ReceiptReader is the daemon's own view of the chain. *chain.Client satisfies it.
type ReceiptReader interface {
	AwaitReceipt(ctx context.Context, txHash string) (chain.Receipt, error)
}

// Wallet submits contract calls through one Circle developer-controlled EOA.
type Wallet struct {
	baseURL      string
	apiKey       string
	entitySecret []byte
	walletID     string
	chainID      uint64
	reader       ReceiptReader
	http         *http.Client

	// Filled by Open.
	pub  *rsa.PublicKey
	addr common.Address

	pollEvery time.Duration // between transaction-state reads
	deadline  time.Duration // for Circle to report a mined or terminal state
}

// NewFromEnv builds a wallet lane from the environment. Fail-closed: a missing or malformed
// value is a refusal here, never a stub at use, and every error names the VARIABLE, never
// the value. Call Open before Submit.
func NewFromEnv(chainID uint64, reader ReceiptReader) (*Wallet, error) {
	get := func(name string) (string, error) {
		v, ok := os.LookupEnv(name)
		if !ok || strings.TrimSpace(v) == "" {
			return "", fmt.Errorf("circle wallet: %s is not set — refusing to construct a signer", name)
		}
		return strings.TrimSpace(v), nil
	}
	apiKey, err := get(EnvAPIKey)
	if err != nil {
		return nil, err
	}
	secretHex, err := get(EnvEntitySecret)
	if err != nil {
		return nil, err
	}
	walletID, err := get(EnvWalletID)
	if err != nil {
		return nil, err
	}
	return newWallet(defaultBaseURL, apiKey, secretHex, walletID, chainID, reader)
}

func newWallet(baseURL, apiKey, secretHex, walletID string, chainID uint64, reader ReceiptReader) (*Wallet, error) {
	if reader == nil {
		return nil, fmt.Errorf("circle wallet: no chain reader — the receipt is read from the chain, never taken from Circle")
	}
	if _, ok := blockchainFor(chainID); !ok {
		return nil, fmt.Errorf("circle wallet: chain id %d is not an Arc network Circle wallets serve", chainID)
	}
	// Circle API keys are PREFIX:ID:SECRET. Checking the shape catches a pasted entity
	// secret or a truncated key before the first request does.
	if strings.Count(apiKey, ":") != 2 {
		return nil, fmt.Errorf("circle wallet: %s is not in Circle's PREFIX:ID:SECRET form", EnvAPIKey)
	}
	secret, err := hex.DecodeString(strings.TrimPrefix(secretHex, "0x"))
	if err != nil || len(secret) != 32 {
		return nil, fmt.Errorf("circle wallet: %s is not a 32-byte hex entity secret", EnvEntitySecret)
	}
	if !isUUID(walletID) {
		return nil, fmt.Errorf("circle wallet: %s is not a wallet id (UUID)", EnvWalletID)
	}
	return &Wallet{
		baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, entitySecret: secret,
		walletID: walletID, chainID: chainID, reader: reader,
		http:      &http.Client{Timeout: 30 * time.Second},
		pollEvery: time.Second, deadline: 90 * time.Second,
	}, nil
}

// blockchainFor maps the deployment's chain id to Circle's blockchain name. Only Arc is
// listed: this lane signs for Snapfall's contracts, and they live on Arc.
func blockchainFor(chainID uint64) (string, bool) {
	switch chainID {
	case 5_042:
		return "ARC", true
	case 5_042_002:
		return "ARC-TESTNET", true
	}
	return "", false
}

// Open fetches Circle's entity public key and the wallet, and refuses a wallet that cannot
// safely be this lane: not LIVE, not developer-custodied, a smart-contract account, or on a
// different network from the deployment.
func (w *Wallet) Open(ctx context.Context) error {
	var pk struct {
		Data struct {
			PublicKey string `json:"publicKey"`
		} `json:"data"`
	}
	if err := w.call(ctx, http.MethodGet, "/v1/w3s/config/entity/publicKey", nil, &pk); err != nil {
		return fmt.Errorf("circle wallet: entity public key: %w", err)
	}
	pub, err := parseRSAPublicKey(pk.Data.PublicKey)
	if err != nil {
		return fmt.Errorf("circle wallet: entity public key: %w", err)
	}

	var wr struct {
		Data struct {
			Wallet struct {
				ID          string `json:"id"`
				Address     string `json:"address"`
				Blockchain  string `json:"blockchain"`
				State       string `json:"state"`
				CustodyType string `json:"custodyType"`
				AccountType string `json:"accountType"`
			} `json:"wallet"`
		} `json:"data"`
	}
	if err := w.call(ctx, http.MethodGet, "/v1/w3s/wallets/"+w.walletID, nil, &wr); err != nil {
		return fmt.Errorf("circle wallet: wallet %s: %w", w.walletID, err)
	}
	got := wr.Data.Wallet
	want, _ := blockchainFor(w.chainID)
	switch {
	case got.Blockchain != want:
		return fmt.Errorf("circle wallet: wallet %s is on %q but the deployment is %s (chain %d)", w.walletID, got.Blockchain, want, w.chainID)
	case got.State != "LIVE":
		return fmt.Errorf("circle wallet: wallet %s is %q, not LIVE", w.walletID, got.State)
	case got.CustodyType != "DEVELOPER":
		return fmt.Errorf("circle wallet: wallet %s is %q custody; this lane signs only for a developer-controlled wallet", w.walletID, got.CustodyType)
	case got.AccountType != "EOA":
		// See the package doc: an SCA's reported hash can succeed while the call reverted.
		return fmt.Errorf("circle wallet: wallet %s is a %q account; only an EOA's receipt status is the call's outcome", w.walletID, got.AccountType)
	case !common.IsHexAddress(got.Address):
		return fmt.Errorf("circle wallet: wallet %s reported address %q", w.walletID, got.Address)
	}
	w.pub, w.addr = pub, common.HexToAddress(got.Address)
	return nil
}

// Address is the wallet's on-chain address (safe to log). Zero until Open succeeds.
func (w *Wallet) Address() common.Address { return w.addr }

// Submit executes one contract call and waits for its receipt. Same contract as
// chain.Client.Submit: mined-but-reverted is Reverted=true with a nil error, and the caller
// MUST branch on it.
func (w *Wallet) Submit(ctx context.Context, to common.Address, calldata []byte) (chain.Receipt, error) {
	if w.pub == nil {
		return chain.Receipt{}, fmt.Errorf("refusing to submit: the Circle wallet lane was never opened")
	}
	ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, w.pub, w.entitySecret, nil)
	if err != nil {
		return chain.Receipt{}, fmt.Errorf("circle wallet: encrypting the entity secret failed")
	}
	key, err := newUUIDv4()
	if err != nil {
		return chain.Receipt{}, err
	}
	// A fresh idempotency key per Submit, matching the raw-key lane (each Submit is a new
	// transaction). Duplicates are prevented upstream, by the write-ahead claim and the
	// chain oracle (SC-FP-003 allows one advance per job), not by Circle.
	req := map[string]any{
		"idempotencyKey":         key,
		"walletId":               w.walletID,
		"contractAddress":        to.Hex(),
		"callData":               "0x" + hex.EncodeToString(calldata),
		"feeLevel":               "MEDIUM",
		"entitySecretCiphertext": base64.StdEncoding.EncodeToString(ciphertext),
	}
	var created struct {
		Data struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"data"`
	}
	if err := w.call(ctx, http.MethodPost, "/v1/w3s/developer/transactions/contractExecution", req, &created); err != nil {
		return chain.Receipt{}, fmt.Errorf("circle wallet: create transaction: %w", err)
	}
	if created.Data.ID == "" {
		return chain.Receipt{}, fmt.Errorf("circle wallet: create transaction returned no id")
	}

	txHash, err := w.awaitHash(ctx, created.Data.ID)
	if err != nil {
		return chain.Receipt{}, err
	}
	return w.reader.AwaitReceipt(ctx, txHash)
}

// awaitHash polls Circle until the transaction is mined or terminal, returning the hash
// whose receipt decides the outcome.
//
// On Arc a transaction can go SENT → COMPLETE with no CONFIRMED in between, so the loop
// accepts any of CONFIRMED, COMPLETE and FAILED once a hash exists (FAILED with a hash is
// usually a mined revert, and the receipt says which). CANCELLED and DENIED never count as
// landed: a cancelled transaction's original hash may never mine.
func (w *Wallet) awaitHash(ctx context.Context, id string) (string, error) {
	deadline := time.Now().Add(w.deadline)
	for {
		var tr struct {
			Data struct {
				Transaction struct {
					State       string `json:"state"`
					TxHash      string `json:"txHash"`
					ErrorReason string `json:"errorReason"`
				} `json:"transaction"`
			} `json:"data"`
		}
		if err := w.call(ctx, http.MethodGet, "/v1/w3s/transactions/"+id, nil, &tr); err != nil {
			return "", fmt.Errorf("circle wallet: transaction %s: %w", id, err)
		}
		t := tr.Data.Transaction
		switch t.State {
		case "CONFIRMED", "COMPLETE", "FAILED":
			if t.TxHash != "" {
				return t.TxHash, nil
			}
			if t.State == "FAILED" {
				return "", fmt.Errorf("circle wallet: transaction %s FAILED before anything was mined (%s)", id, reason(t.ErrorReason))
			}
		case "CANCELLED", "DENIED":
			return "", fmt.Errorf("circle wallet: transaction %s %s, nothing landed (%s)", id, t.State, reason(t.ErrorReason))
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("circle wallet: transaction %s still %s after %s — the restart oracle resolves it (chain state is authoritative)", id, t.State, w.deadline)
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("circle wallet: interrupted awaiting transaction %s — the restart oracle resolves it: %w", id, ctx.Err())
		case <-time.After(w.pollEvery):
		}
	}
}

func reason(r string) string {
	if r == "" {
		return "no reason given"
	}
	return r
}

// call does one Circle API request. A non-2xx answer becomes an error carrying Circle's
// code and message; the request body (which holds the ciphertext) and the API key are never
// part of it.
func (w *Wallet) call(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, w.baseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+w.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		return fmt.Errorf("HTTP %d: code %d: %s", resp.StatusCode, e.Code, e.Message)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("HTTP %d, undecodable body: %w", resp.StatusCode, err)
	}
	return nil
}

func parseRSAPublicKey(p string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(p))
	if block == nil {
		return nil, fmt.Errorf("not a PEM block")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rk, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not an RSA key")
	}
	return rk, nil
}

func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("idempotency key: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

func isUUID(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	return err == nil
}
