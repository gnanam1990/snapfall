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
	"errors"
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

// NewReadOnly builds a client that can only read. It holds no key, so Submit fails
// closed on it; use it for view calls (balances, rates, job status) where constructing a
// signer would demand a secret the caller has no reason to hold.
func NewReadOnly(rpcURL string, chainID uint64) (*Client, error) {
	// Store the TRIMMED url. Validating a trimmed copy while keeping the padded original
	// accepts configuration that then fails on the first view call, which is a confusing
	// place to learn about a stray newline in an env var (review: PR #41).
	rpcURL = strings.TrimSpace(rpcURL)
	if rpcURL == "" {
		return nil, fmt.Errorf("chain reader: rpc url is empty")
	}
	return &Client{
		rpcURL: rpcURL, http: &http.Client{Timeout: 30 * time.Second},
		chainID: new(big.Int).SetUint64(chainID),
	}, nil
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

// Broadcast identifies one signed acceptDelivery-style transaction: its hash, the signer
// nonce it occupies, the address that signed it, the signed bytes exactly as sent, and the
// chain head read just before it was signed. All of it is public chain data the moment it
// is broadcast, never key material.
//
// Signer, Raw and SentHead are zero in records written before they were stored. Resolve
// then derives the signer from Raw, and with neither it refuses to classify an unseen
// transaction (ErrSignerUnknown); Rebroadcast refuses (nothing to re-send).
//
// Recorded bytes are verified before ANY use (decoded): they must be the recorded
// transaction (its hash, its nonce) and, when a signer is recorded, signed by it. A record
// that fails the check (ErrBroadcastMismatch) gives no signer, no previous fee and no bytes
// to send.
type Broadcast struct {
	TxHash   string
	Nonce    uint64
	Signer   common.Address
	Raw      []byte
	SentHead uint64
}

// ErrSignerUnknown: a broadcast names no signer and carries no signed bytes to recover one
// from, so no mined nonce can be compared and the transaction cannot be called dropped or
// evicted. The caller treats it as unresolvable, never as "never sent".
var ErrSignerUnknown = errors.New("the broadcast names no signer and has no signed bytes to recover one from")

// ErrSignerMismatch: a broadcast was signed by another key than this client's. This client
// cannot sign a replacement at that account's nonce.
var ErrSignerMismatch = errors.New("the broadcast was signed by another key than this client's")

// ErrNonceUsed: the broadcast's nonce is already mined, so there is nothing to replace.
var ErrNonceUsed = errors.New("the broadcast's nonce is already used on chain")

// ErrBroadcastMismatch: a broadcast's recorded signed bytes are not the recorded
// transaction. They do not decode, they hash to another transaction, they sit at another
// nonce, or they recover to another account than the recorded signer. Nothing in such a
// record is trusted: no signer is taken from it, no fee is read from it, and its bytes are
// never sent.
var ErrBroadcastMismatch = errors.New("the broadcast's recorded signed bytes are not the recorded transaction")

// ErrFeeCapExceeded: a fee replacement would have to pay more than replacementFeeCapMultiple
// times the chain's fresh price. Nothing is signed; the caller records the broadcast stuck.
var ErrFeeCapExceeded = errors.New("a fee replacement would exceed the fee cap")

// replacementFeeCapMultiple caps a fee replacement's gas price at this many times the
// fresh price, max(eth_gasPrice, latest base fee). A replacement that must outbid the
// previous price by a quarter and cannot stay under the cap is not signed.
const replacementFeeCapMultiple = 3

// SubmitTracked is Submit with a write-ahead step: onSigned runs once the transaction is
// SIGNED and BEFORE eth_sendRawTransaction. The hash, nonce, signer and raw bytes are all
// known locally at that point, so a caller that records them from the hook holds a record
// of every transaction that can possibly reach the node. If onSigned returns an error,
// nothing is sent and the error is returned.
//
// Every failure after the hook (the send RPC timing out after the node accepted the bytes,
// a load-balanced endpoint answering with an error from one backend after another took
// it, the receipt wait timing out, the process dying) leaves a transaction that may be out
// and may still mine. The caller resolves it by hash later (Resolve); it never reads a send
// error as "not sent".
//
// The hook runs under the signer mutex. Nothing in Submit's contract changes otherwise.
func (c *Client) SubmitTracked(ctx context.Context, to common.Address, calldata []byte, onSigned func(Broadcast) error) (Receipt, error) {
	return c.submitTracked(ctx, to, calldata, 0, onSigned)
}

// TxState is what ONE look at the chain says about a transaction this signer broadcast.
type TxState int

const (
	// TxPending: no receipt yet, but nothing says the transaction cannot mine. The node
	// knows it (in the mempool, or mined with its receipt not yet readable), or the reads
	// were inconclusive. Nothing about it is known yet.
	TxPending TxState = iota
	// TxMined: a receipt exists. Receipt.Reverted says how it ended.
	TxMined
	// TxDropped: in THIS observation there is no receipt, the node does not know the
	// transaction (eth_getTransactionByHash is null), and the signer's mined nonce has moved
	// past it. That is what a transaction whose nonce went to another one looks like, but it
	// is also what a mined transaction looks like through a load-balanced RPC whose nonce
	// backend is ahead of its receipt backend. One observation is NOT proof: the caller
	// records it and confirms with a second one after the chain head has advanced.
	TxDropped
	// TxEvicted: no receipt, the node does not know the transaction, and the signer's mined
	// nonce has NOT moved past it. The mempool lost it; nothing else has taken the nonce, so
	// the very same signed bytes can still mine if they are sent again (Rebroadcast). Like
	// TxDropped it is one observation, confirmed by the caller before it acts.
	TxEvicted
)

// Resolution is one observation of a broadcast: its state, the receipt when mined, and the
// chain head the observation was taken at, so a caller can require a later observation to
// be taken at a strictly higher head before believing a TxDropped or TxEvicted.
type Resolution struct {
	State   TxState
	Receipt Receipt
	Head    uint64
}

// Resolve reports a broadcast transaction's state without waiting for it. Reads, in order:
// the chain head, the SIGNER's mined nonce (b.Signer, the address recorded with the
// broadcast, or the one recovered from its signed bytes, so a key rotated across a restart
// never compares the wrong account), the receipt, then eth_getTransactionByHash. A receipt
// is TxMined. A transaction the node knows is TxPending. Only a transaction with no receipt
// AND unknown to the node is TxDropped (mined nonce past it) or TxEvicted (mined nonce not
// past it), and both are single observations the caller must confirm. With no signer to
// compare, such a transaction is ErrSignerUnknown, never a state, and with signed bytes
// that are not the recorded transaction it is ErrBroadcastMismatch. Any read failure is
// returned as an error, never guessed as a state.
func (c *Client) Resolve(ctx context.Context, b Broadcast) (Resolution, error) {
	if strings.TrimSpace(b.TxHash) == "" {
		return Resolution{}, fmt.Errorf("resolve: transaction hash is empty")
	}
	// A record whose signed bytes are not its transaction yields no signer: neither the
	// recorded address nor the bytes are trusted. The hash can still be asked about.
	signer, signerErr := b.signer(c.chainID)
	signerKnown := signerErr == nil
	if err := c.VerifyChainID(ctx); err != nil {
		return Resolution{}, err
	}
	head, err := c.headHeight(ctx)
	if err != nil {
		return Resolution{}, fmt.Errorf("chain head: %w", err)
	}
	// The nonce is read BEFORE the receipt: read after a null receipt, it could count this
	// very transaction mining in between and call it dropped.
	var mined uint64
	if signerKnown {
		if mined, err = c.minedNonce(ctx, signer); err != nil {
			return Resolution{}, err
		}
	}
	rec, found, err := c.receiptOnce(ctx, b.TxHash)
	if err != nil {
		return Resolution{}, err
	}
	if found {
		return Resolution{State: TxMined, Receipt: rec, Head: head}, nil
	}
	known, err := c.txKnown(ctx, b.TxHash)
	if err != nil {
		return Resolution{}, err
	}
	res := Resolution{Receipt: Receipt{TxHash: b.TxHash}, Head: head}
	switch {
	case known:
		res.State = TxPending
	case !signerKnown:
		return Resolution{}, fmt.Errorf("resolve %s: %w", b.TxHash, signerErr)
	case mined > b.Nonce:
		res.State = TxDropped
	default:
		res.State = TxEvicted
	}
	return res, nil
}

// signer is the account that signed the broadcast. With signed bytes recorded, they are
// verified first (decoded tx); the signer is the account they recover to, and a recorded
// signer must be that same account. With no bytes, the recorded signer is all there is.
// ErrSignerUnknown when neither is recorded; ErrBroadcastMismatch when the bytes do not
// check out.
func (b Broadcast) signer(chainID *big.Int) (common.Address, error) {
	if len(b.Raw) == 0 {
		if b.Signer == (common.Address{}) {
			return common.Address{}, ErrSignerUnknown
		}
		return b.Signer, nil
	}
	_, from, err := b.decoded(chainID)
	return from, err
}

// decoded verifies a broadcast's recorded signed bytes against the rest of its record
// before anything uses them: they must decode, hash to b.TxHash, sit at b.Nonce, and
// recover to a sender, which must be b.Signer when one is recorded. It returns the decoded
// transaction and its sender; any failure is ErrBroadcastMismatch.
func (b Broadcast) decoded(chainID *big.Int) (*types.Transaction, common.Address, error) {
	tx := new(types.Transaction)
	if err := tx.UnmarshalBinary(b.Raw); err != nil {
		return nil, common.Address{}, fmt.Errorf("%w: %s's bytes do not decode: %v", ErrBroadcastMismatch, b.TxHash, err)
	}
	if !strings.EqualFold(tx.Hash().Hex(), b.TxHash) || tx.Nonce() != b.Nonce {
		return nil, common.Address{}, fmt.Errorf("%w: the bytes recorded for %s at nonce %d are transaction %s at nonce %d",
			ErrBroadcastMismatch, b.TxHash, b.Nonce, tx.Hash().Hex(), tx.Nonce())
	}
	from, err := types.Sender(types.LatestSignerForChainID(chainID), tx)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("%w: %s's signature does not recover: %v", ErrBroadcastMismatch, b.TxHash, err)
	}
	if b.Signer != (common.Address{}) && from != b.Signer {
		return nil, common.Address{}, fmt.Errorf("%w: %s's bytes are signed by %s, not the recorded signer %s",
			ErrBroadcastMismatch, b.TxHash, from.Hex(), b.Signer.Hex())
	}
	return tx, from, nil
}

// minedNonce is addr's count of MINED transactions (eth_getTransactionCount at "latest").
func (c *Client) minedNonce(ctx context.Context, addr common.Address) (uint64, error) {
	var nonceHex string
	if err := c.rpc(ctx, "eth_getTransactionCount", []any{addr.Hex(), "latest"}, &nonceHex); err != nil {
		return 0, fmt.Errorf("mined nonce: %w", err)
	}
	return new(big.Int).SetBytes(common.FromHex(nonceHex)).Uint64(), nil
}

// Replace re-signs a call at prev's nonce with a strictly higher gas price, for a broadcast
// that the node holds but does not mine (priced under what the chain takes). At most one
// transaction per nonce can mine, so the original and every replacement together settle at
// most once. It is a fresh signature, so it needs this client's key, and refuses when prev
// was signed by another one (ErrSignerMismatch) or when prev's nonce is already mined
// (ErrNonceUsed: nothing to replace, the caller resolves what took it).
//
// prev's recorded bytes are verified before they are used (ErrBroadcastMismatch otherwise),
// and its previous price is read only from verified bytes.
//
// Fees follow Arc's rule: the base fee is read from chain right now, never cached. The new
// gas price is the highest of the node's fresh eth_gasPrice, the latest block's fresh base
// fee, and prev's own price bumped by a quarter (nodes refuse a replacement under a ~10%
// bump), capped at replacementFeeCapMultiple times the fresh price: a bump past the cap
// signs nothing (ErrFeeCapExceeded). The gas limit is estimated fresh: a call that would now
// revert is refused before anything is signed.
//
// onSigned is the same write-ahead hook as SubmitTracked's: it runs after signing and
// before the send, and an error from it sends nothing. Replace does NOT wait for a receipt;
// the returned Broadcast is resolved later like any other. A send error after the hook
// returns that Broadcast with the error, and the caller resolves it by hash.
func (c *Client) Replace(ctx context.Context, to common.Address, calldata []byte, prev Broadcast, onSigned func(Broadcast) error) (Broadcast, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.key == nil {
		return Broadcast{}, fmt.Errorf("refusing to replace: this client is read-only (no signing key)")
	}
	signer, err := prev.signer(c.chainID)
	if err != nil {
		return Broadcast{}, fmt.Errorf("replace %s: %w", prev.TxHash, err)
	}
	if signer != c.addr {
		return Broadcast{}, fmt.Errorf("replace %s: %w", prev.TxHash, ErrSignerMismatch)
	}
	// The previous price is read only from bytes that were just verified to be prev.
	var prevPrice *big.Int
	if len(prev.Raw) > 0 {
		tx, _, err := prev.decoded(c.chainID)
		if err != nil {
			return Broadcast{}, fmt.Errorf("replace %s: %w", prev.TxHash, err)
		}
		prevPrice = tx.GasPrice()
	}
	if err := c.VerifyChainID(ctx); err != nil {
		return Broadcast{}, fmt.Errorf("refusing to sign: %w", err)
	}
	head, err := c.headHeight(ctx)
	if err != nil {
		return Broadcast{}, fmt.Errorf("chain head: %w", err)
	}
	mined, err := c.minedNonce(ctx, c.addr)
	if err != nil {
		return Broadcast{}, err
	}
	if mined > prev.Nonce {
		return Broadcast{}, fmt.Errorf("replace %s: %w (mined nonce %d)", prev.TxHash, ErrNonceUsed, mined)
	}
	price, err := c.replacementGasPrice(ctx, prevPrice)
	if err != nil {
		return Broadcast{}, fmt.Errorf("replace %s: %w", prev.TxHash, err)
	}
	gas, err := c.estimateGas(ctx, to, calldata)
	if err != nil {
		return Broadcast{}, err
	}
	signed, err := types.SignTx(types.NewTx(&types.LegacyTx{
		Nonce: prev.Nonce, To: &to, Value: big.NewInt(0), Gas: gas, GasPrice: price, Data: calldata,
	}), types.NewEIP155Signer(c.chainID), c.key)
	if err != nil {
		return Broadcast{}, fmt.Errorf("signing: %w", err)
	}
	rawTx, err := signed.MarshalBinary()
	if err != nil {
		return Broadcast{}, err
	}
	bc := Broadcast{TxHash: signed.Hash().Hex(), Nonce: prev.Nonce, Signer: c.addr, Raw: rawTx, SentHead: head}
	if onSigned != nil {
		if err := onSigned(bc); err != nil {
			return Broadcast{}, fmt.Errorf("replacement not sent: %w", err)
		}
	}
	if err := c.sendRaw(ctx, rawTx, bc.TxHash); err != nil {
		return bc, err
	}
	return bc, nil
}

// replacementGasPrice is max(fresh, prevPrice * 5/4 + 1), where fresh is max(fresh
// eth_gasPrice, fresh latest base fee), and never more than replacementFeeCapMultiple *
// fresh: a bump past the cap is ErrFeeCapExceeded and nothing is signed. prevPrice is nil
// when the previous transaction's bytes were not recorded. All arithmetic is big.Int.
func (c *Client) replacementGasPrice(ctx context.Context, prevPrice *big.Int) (*big.Int, error) {
	var gasPriceHex string
	if err := c.rpc(ctx, "eth_gasPrice", []any{}, &gasPriceHex); err != nil {
		return nil, fmt.Errorf("gas price: %w", err)
	}
	fresh := new(big.Int).SetBytes(common.FromHex(gasPriceHex))
	var block struct {
		BaseFee string `json:"baseFeePerGas"`
	}
	if err := c.rpc(ctx, "eth_getBlockByNumber", []any{"latest", false}, &block); err != nil {
		return nil, fmt.Errorf("base fee: %w", err)
	}
	if block.BaseFee != "" {
		if base := new(big.Int).SetBytes(common.FromHex(block.BaseFee)); base.Cmp(fresh) > 0 {
			fresh = base
		}
	}
	price := new(big.Int).Set(fresh)
	if prevPrice != nil {
		bumped := new(big.Int).Div(new(big.Int).Mul(prevPrice, big.NewInt(5)), big.NewInt(4))
		bumped.Add(bumped, big.NewInt(1))
		limit := new(big.Int).Mul(fresh, big.NewInt(replacementFeeCapMultiple))
		if bumped.Cmp(limit) > 0 {
			return nil, fmt.Errorf("%w: the previous price %s bumped is %s, over %d times the fresh price %s",
				ErrFeeCapExceeded, prevPrice, bumped, replacementFeeCapMultiple, fresh)
		}
		if bumped.Cmp(price) > 0 {
			price = bumped
		}
	}
	return price, nil
}

// estimateGas estimates a call from this signer and adds 20% headroom (unused gas is refunded).
func (c *Client) estimateGas(ctx context.Context, to common.Address, calldata []byte) (uint64, error) {
	call := map[string]any{"from": c.addr.Hex(), "to": to.Hex(), "data": "0x" + hex.EncodeToString(calldata)}
	var gasHex string
	if err := c.rpc(ctx, "eth_estimateGas", []any{call}, &gasHex); err != nil {
		// Estimation failing usually means the call would revert; surface it as a
		// submission failure (nothing was signed, nothing mined).
		return 0, fmt.Errorf("gas estimate (the call likely reverts): %w", err)
	}
	gas := new(big.Int).SetBytes(common.FromHex(gasHex)).Uint64()
	return gas + gas/5, nil
}

// sendRaw sends signed bytes. An error here does NOT mean the node lacks the transaction:
// the request can time out after the node accepted it, and a load-balanced endpoint can
// answer from a backend that did not take it while another did.
func (c *Client) sendRaw(ctx context.Context, rawTx []byte, wantHash string) error {
	var got string
	if err := c.rpc(ctx, "eth_sendRawTransaction", []any{"0x" + hex.EncodeToString(rawTx)}, &got); err != nil {
		return fmt.Errorf("send (the transaction may still have reached the node; resolve it by hash): %w", err)
	}
	if !strings.EqualFold(got, wantHash) {
		return fmt.Errorf("send: the node reported hash %s for transaction %s", got, wantHash)
	}
	return nil
}

// Rebroadcast sends a broadcast's recorded signed bytes again, unchanged: the same nonce,
// the same hash, the same signature. It cannot create a second settlement. If the original
// is still in some mempool the node answers "already known" (success here); if it or any
// other transaction has taken the nonce, the node refuses the bytes. The bytes are checked
// against the record before anything is sent: they must decode to b.TxHash at b.Nonce,
// signed by b.Signer when one is recorded. It needs no key, but holds the signer mutex so it
// never interleaves with a fresh submission's nonce-fetch-to-send.
func (c *Client) Rebroadcast(ctx context.Context, b Broadcast) error {
	if len(b.Raw) == 0 {
		return fmt.Errorf("rebroadcast %s: no signed bytes were recorded with this broadcast", b.TxHash)
	}
	if _, _, err := b.decoded(c.chainID); err != nil {
		return fmt.Errorf("rebroadcast %s: %w", b.TxHash, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.VerifyChainID(ctx); err != nil {
		return err
	}
	var got string
	if err := c.rpc(ctx, "eth_sendRawTransaction", []any{"0x" + hex.EncodeToString(b.Raw)}, &got); err != nil {
		if m := strings.ToLower(err.Error()); strings.Contains(m, "already known") || strings.Contains(m, "known transaction") {
			return nil
		}
		return fmt.Errorf("rebroadcast %s: %w", b.TxHash, err)
	}
	if !strings.EqualFold(got, b.TxHash) {
		return fmt.Errorf("rebroadcast %s: the node reported hash %s", b.TxHash, got)
	}
	return nil
}

// txKnown reports whether the node has any record of the transaction (mempool or mined).
func (c *Client) txKnown(ctx context.Context, txHash string) (bool, error) {
	var tx json.RawMessage
	found := false
	if err := c.rpcMaybeNull(ctx, "eth_getTransactionByHash", []any{txHash}, &tx, &found); err != nil {
		return false, fmt.Errorf("transaction lookup: %w", err)
	}
	return found, nil
}

// headHeight is the current chain head as a number (BlockNumber's guard against 0x0 holds).
func (c *Client) headHeight(ctx context.Context) (uint64, error) {
	tag, err := c.BlockNumber(ctx)
	if err != nil {
		return 0, err
	}
	return new(big.Int).SetBytes(common.FromHex(tag)).Uint64(), nil
}

func (c *Client) submit(ctx context.Context, to common.Address, calldata []byte, fixedGas uint64) (Receipt, error) {
	return c.submitTracked(ctx, to, calldata, fixedGas, nil)
}

func (c *Client) submitTracked(ctx context.Context, to common.Address, calldata []byte, fixedGas uint64, onSigned func(Broadcast) error) (Receipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// A read-only client holds no key: refuse here rather than panicking in the signer.
	if c.key == nil {
		return Receipt{}, fmt.Errorf("refusing to submit: this client is read-only (no signing key)")
	}

	// Verify the endpoint is the chain we signed up for — refuse to sign for a
	// different chain id than configured (same posture as the indexer).
	if err := c.VerifyChainID(ctx); err != nil {
		var mismatch chainIDMismatch
		if errors.As(err, &mismatch) {
			return Receipt{}, fmt.Errorf("refusing to sign: %w", err)
		}
		return Receipt{}, err
	}

	// A tracked submission records the head it was signed at, so the caller can tell how
	// long the transaction has sat unmined.
	var head uint64
	if onSigned != nil {
		h, err := c.headHeight(ctx)
		if err != nil {
			return Receipt{}, fmt.Errorf("chain head: %w", err)
		}
		head = h
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
		g, err := c.estimateGas(ctx, to, calldata)
		if err != nil {
			return Receipt{}, err
		}
		gas = g
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
	txHash := signed.Hash().Hex()
	if onSigned != nil {
		// Write-ahead: the record exists before the bytes can reach any node.
		if err := onSigned(Broadcast{TxHash: txHash, Nonce: nonce, Signer: c.addr, Raw: rawTx, SentHead: head}); err != nil {
			return Receipt{}, fmt.Errorf("not sent: %w", err)
		}
	}
	if err := c.sendRaw(ctx, rawTx, txHash); err != nil {
		return Receipt{}, err
	}

	return c.AwaitReceipt(ctx, txHash)
}

// AwaitReceipt polls this client's endpoint for txHash's receipt and reports its STATUS
// (mined-but-reverted is Reverted=true with a nil error, as for Submit). It needs no key: a
// signer that broadcasts somewhere else (the Circle wallet lane) still reads the outcome from
// the chain itself, so the chain stays the one authority on whether a call succeeded.
func (c *Client) AwaitReceipt(ctx context.Context, txHash string) (Receipt, error) {
	// Arc finalizes on inclusion; poll briefly for the receipt.
	deadline := time.Now().Add(60 * time.Second)
	for {
		rec, found, err := c.receiptOnce(ctx, txHash)
		if err != nil {
			return Receipt{}, err
		}
		if found {
			return rec, nil
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

// receiptOnce reads txHash's receipt a single time; found=false means the node has none yet.
func (c *Client) receiptOnce(ctx context.Context, txHash string) (Receipt, bool, error) {
	var rec struct {
		Status      string `json:"status"`
		BlockNumber string `json:"blockNumber"`
		GasUsed     string `json:"gasUsed"`
	}
	found := false
	if err := c.rpcMaybeNull(ctx, "eth_getTransactionReceipt", []any{txHash}, &rec, &found); err != nil {
		return Receipt{}, false, fmt.Errorf("receipt: %w", err)
	}
	if !found {
		return Receipt{}, false, nil
	}
	return Receipt{
		TxHash:   txHash,
		Block:    new(big.Int).SetBytes(common.FromHex(rec.BlockNumber)).Uint64(),
		GasUsed:  new(big.Int).SetBytes(common.FromHex(rec.GasUsed)).Uint64(),
		Reverted: new(big.Int).SetBytes(common.FromHex(rec.Status)).Sign() == 0,
	}, true, nil
}

// CallView executes a read-only eth_call.
func (c *Client) CallView(ctx context.Context, to common.Address, calldata []byte) ([]byte, error) {
	return c.CallViewAt(ctx, to, calldata, "latest")
}

// CallViewAt is CallView pinned to a block tag ("latest", "finalized", or a 0x-hex height).
//
// Separate eth_calls are separate points in time. Anything that reads several values and then
// reasons about them TOGETHER has to pin them to one block, or it can compute a confident answer
// from a position that never existed: the demo seed reads pool assets and two outstanding
// balances to size a floor, and a mid-sequence advance by another org silently invalidates it
// (review: PR #41).
func (c *Client) CallViewAt(ctx context.Context, to common.Address, calldata []byte, block string) ([]byte, error) {
	if strings.TrimSpace(block) == "" {
		return nil, fmt.Errorf("CallViewAt: block tag is empty")
	}
	var out string
	call := map[string]any{"to": to.Hex(), "data": "0x" + hex.EncodeToString(calldata)}
	if err := c.rpc(ctx, "eth_call", []any{call, block}, &out); err != nil {
		return nil, err
	}
	return common.FromHex(out), nil
}

type chainIDMismatch struct{ got, want *big.Int }

func (e chainIDMismatch) Error() string {
	return fmt.Sprintf("endpoint chain id %s != configured %s", e.got, e.want)
}

// VerifyChainID checks that the RPC endpoint serves the configured chain. A read-only
// caller uses it before trusting a view: the same contract address can exist on another
// network and answer with a plausible value.
func (c *Client) VerifyChainID(ctx context.Context) error {
	var chainHex string
	if err := c.rpc(ctx, "eth_chainId", []any{}, &chainHex); err != nil {
		return fmt.Errorf("chain id check: %w", err)
	}
	if got := new(big.Int).SetBytes(common.FromHex(chainHex)); got.Cmp(c.chainID) != 0 {
		return chainIDMismatch{got: got, want: c.chainID}
	}
	return nil
}

// BlockNumber reads the current head height and returns it as the 0x-hex tag eth_call wants,
// so a caller can take one snapshot and read every value against it.
func (c *Client) BlockNumber(ctx context.Context) (string, error) {
	var out string
	if err := c.rpc(ctx, "eth_blockNumber", []any{}, &out); err != nil {
		return "", err
	}
	h := new(big.Int).SetBytes(common.FromHex(out))
	if h.Sign() == 0 {
		// Genesis is not a plausible head for a live network, and a node that answers 0x0 is
		// more likely still syncing than genuinely at block zero.
		return "", fmt.Errorf("eth_blockNumber returned %q; the node may still be syncing", out)
	}
	return "0x" + h.Text(16), nil
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
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("%s: HTTP %d, undecodable body: %w", method, resp.StatusCode, err)
	}
	if envelope.Error != nil {
		return fmt.Errorf("%s: RPC error %d: %s%s (HTTP %d)", method, envelope.Error.Code, envelope.Error.Message,
			revertSuffix(envelope.Error.Data), resp.StatusCode)
	}
	if string(envelope.Result) == "null" || len(envelope.Result) == 0 {
		*found = false
		return nil
	}
	*found = true
	return json.Unmarshal(envelope.Result, result)
}
