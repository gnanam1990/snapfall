// Package chain is the daemon's transaction writer for the deployed Snapfall
// contracts on Arc testnet — the missing half of the chain gap, behind the gates that
// already exist (advances execute only from an approval-minted Grant; settlements only
// from an authenticated customer Accept; both freeze-gated and exactly-once upstream).
//
// KEY DISCIPLINE (the owner-token standard): keys enter from ENV ONLY, are
// format-validated at construction with fail-closed refusal, are never committed, and
// never appear in a log line or event payload — a behavioral test drives a full
// signed submission and asserts the key's hex appears nowhere the daemon writes.
//
// THE CHAIN IS THE RECOVERY ORACLE: after a crash between the durable write-ahead
// claim and the receipt, the daemon does NOT guess and does NOT invent a
// reconciliation heuristic — SC-FP-003 permits exactly one advance per job, so
// FloatPool.openAdvanceOf answers whether the advance landed, and JobVault.jobStatus
// answers whether the settlement did. See Oracle.
//
// MINED IS NOT SUCCEEDED: every submission waits for its receipt and checks STATUS. A
// reverted transaction (status 0) is a third outcome — distinct from success and from
// submission failure — and surfaces to the owner as *.reverted, never recorded as a
// completed action.
//
// ONE SIGNER, SERIAL SUBMISSIONS: concurrent task goroutines reach Funding from
// different jobs; concurrent submissions from one key produce nonce collisions that
// fail confusingly. Client.Submit holds one mutex across nonce-fetch → sign → send →
// receipt, so submissions from a single key are strictly serial (Arc's instant
// finality makes the receipt wait short; simplicity beats pipelining here, and the
// serialization is pinned by a concurrency test over a fake RPC).
package chain

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// Receipt is the daemon's view of one mined transaction.
type Receipt struct {
	TxHash   string
	Block    uint64
	GasUsed  uint64
	Reverted bool // status 0: mined AND failed — never conflate with success
}

// Client signs and submits transactions for ONE key, serially.
type Client struct {
	rpcURL  string
	http    *http.Client
	chainID *big.Int
	key     *ecdsa.PrivateKey
	addr    common.Address

	mu sync.Mutex // the single-signer serialization (see package doc)
}

// NewFromEnv builds a client from a private-key environment variable. Fail-closed:
// missing or malformed key is a refusal at construction, never a stub at use. The key
// value never reaches a return, an error message, or a log.
func NewFromEnv(envName, rpcURL string, chainID uint64) (*Client, error) {
	raw, ok := os.LookupEnv(envName)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("chain writer: %s is not set — refusing to construct a signer", envName)
	}
	return newFromKeyHex(raw, rpcURL, chainID, envName)
}

func newFromKeyHex(raw, rpcURL string, chainID uint64, label string) (*Client, error) {
	k := strings.TrimPrefix(strings.TrimSpace(raw), "0x")
	if len(k) != 64 {
		// The error names the VARIABLE, never the value.
		return nil, fmt.Errorf("chain writer: %s is not a 32-byte hex key", label)
	}
	key, err := crypto.HexToECDSA(k)
	if err != nil {
		return nil, fmt.Errorf("chain writer: %s does not parse as a secp256k1 key", label)
	}
	return &Client{
		rpcURL: rpcURL, http: &http.Client{Timeout: 30 * time.Second},
		chainID: new(big.Int).SetUint64(chainID),
		key:     key, addr: crypto.PubkeyToAddress(key.PublicKey),
	}, nil
}

// Address is the signer's public address (safe to log; the key never is).
func (c *Client) Address() common.Address { return c.addr }

// Submit sends one contract call and waits for its receipt. Serial per client (the
// mutex spans nonce → sign → send → receipt). Mined-but-reverted returns Receipt with
// Reverted=true and a nil error: the caller MUST branch on it — a revert is not a
// submission failure and must never be recorded as success.
func (c *Client) Submit(ctx context.Context, to common.Address, calldata []byte) (Receipt, error) {
	return c.submit(ctx, to, calldata, 0)
}

// SubmitWithGas skips gas estimation and submits with a FIXED gas limit. Estimation
// normally pre-empts a predictable revert before anything is signed (no gas burned) —
// this bypass exists so the receipt-status discipline can be demonstrated against the
// real chain: a deliberately doomed call mines, reverts, and must surface as
// Reverted, never as success. Ops/demo use only.
func (c *Client) SubmitWithGas(ctx context.Context, to common.Address, calldata []byte, gasLimit uint64) (Receipt, error) {
	return c.submit(ctx, to, calldata, gasLimit)
}

func (c *Client) submit(ctx context.Context, to common.Address, calldata []byte, fixedGas uint64) (Receipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Verify the endpoint is the chain we signed up for — refuse to sign for a
	// different chain id than configured (same posture as the indexer).
	var chainHex string
	if err := c.rpc(ctx, "eth_chainId", []any{}, &chainHex); err != nil {
		return Receipt{}, fmt.Errorf("chain id check: %w", err)
	}
	if got := new(big.Int).SetBytes(common.FromHex(chainHex)); got.Cmp(c.chainID) != 0 {
		return Receipt{}, fmt.Errorf("refusing to sign: endpoint chain id %s != configured %s", got, c.chainID)
	}

	var nonceHex string
	if err := c.rpc(ctx, "eth_getTransactionCount", []any{c.addr.Hex(), "pending"}, &nonceHex); err != nil {
		return Receipt{}, fmt.Errorf("nonce: %w", err)
	}
	nonce := new(big.Int).SetBytes(common.FromHex(nonceHex)).Uint64()

	var gasPriceHex string
	if err := c.rpc(ctx, "eth_gasPrice", []any{}, &gasPriceHex); err != nil {
		return Receipt{}, fmt.Errorf("gas price: %w", err)
	}
	gasPrice := new(big.Int).SetBytes(common.FromHex(gasPriceHex))

	gas := fixedGas
	if gas == 0 {
		call := map[string]any{"from": c.addr.Hex(), "to": to.Hex(), "data": "0x" + hex.EncodeToString(calldata)}
		var gasHex string
		if err := c.rpc(ctx, "eth_estimateGas", []any{call}, &gasHex); err != nil {
			// Estimation failing usually means the call would revert; surface it as a
			// submission failure (nothing was signed, nothing mined).
			return Receipt{}, fmt.Errorf("gas estimate (the call likely reverts): %w", err)
		}
		gas = new(big.Int).SetBytes(common.FromHex(gasHex)).Uint64()
		gas += gas / 5 // 20% headroom; unused gas is refunded
	}

	tx := types.NewTx(&types.LegacyTx{
		Nonce: nonce, To: &to, Value: big.NewInt(0),
		Gas: gas, GasPrice: gasPrice, Data: calldata,
	})
	signed, err := types.SignTx(tx, types.NewEIP155Signer(c.chainID), c.key)
	if err != nil {
		return Receipt{}, fmt.Errorf("signing: %w", err)
	}
	rawTx, err := signed.MarshalBinary()
	if err != nil {
		return Receipt{}, err
	}
	var txHash string
	if err := c.rpc(ctx, "eth_sendRawTransaction", []any{"0x" + hex.EncodeToString(rawTx)}, &txHash); err != nil {
		return Receipt{}, fmt.Errorf("send: %w", err)
	}

	// Arc finalizes on inclusion; poll briefly for the receipt.
	deadline := time.Now().Add(60 * time.Second)
	for {
		var rec struct {
			Status      string `json:"status"`
			BlockNumber string `json:"blockNumber"`
			GasUsed     string `json:"gasUsed"`
		}
		found := false
		if err := c.rpcMaybeNull(ctx, "eth_getTransactionReceipt", []any{txHash}, &rec, &found); err != nil {
			return Receipt{}, fmt.Errorf("receipt: %w", err)
		}
		if found {
			return Receipt{
				TxHash:   txHash,
				Block:    new(big.Int).SetBytes(common.FromHex(rec.BlockNumber)).Uint64(),
				GasUsed:  new(big.Int).SetBytes(common.FromHex(rec.GasUsed)).Uint64(),
				Reverted: new(big.Int).SetBytes(common.FromHex(rec.Status)).Sign() == 0,
			}, nil
		}
		if time.Now().After(deadline) {
			return Receipt{}, fmt.Errorf("transaction %s submitted but receipt not found within 60s — the restart oracle resolves it (chain state is authoritative)", txHash)
		}
		select {
		case <-ctx.Done():
			return Receipt{}, fmt.Errorf("interrupted awaiting receipt for %s — the restart oracle resolves it: %w", txHash, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// CallView executes a read-only eth_call.
func (c *Client) CallView(ctx context.Context, to common.Address, calldata []byte) ([]byte, error) {
	var out string
	call := map[string]any{"to": to.Hex(), "data": "0x" + hex.EncodeToString(calldata)}
	if err := c.rpc(ctx, "eth_call", []any{call, "latest"}, &out); err != nil {
		return nil, err
	}
	return common.FromHex(out), nil
}

// ── minimal JSON-RPC (runbook rule: HTTP 200 does NOT mean RPC success) ──

func (c *Client) rpc(ctx context.Context, method string, params []any, result any) error {
	found := false
	if err := c.rpcMaybeNull(ctx, method, params, result, &found); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s returned null", method)
	}
	return nil
}

func (c *Client) rpcMaybeNull(ctx context.Context, method string, params []any, result any, found *bool) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.rpcURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("%s: HTTP %d, undecodable body: %w", method, resp.StatusCode, err)
	}
	if envelope.Error != nil {
		return fmt.Errorf("%s: RPC error %d: %s (HTTP %d)", method, envelope.Error.Code, envelope.Error.Message, resp.StatusCode)
	}
	if string(envelope.Result) == "null" || len(envelope.Result) == 0 {
		*found = false
		return nil
	}
	*found = true
	return json.Unmarshal(envelope.Result, result)
}
