package chain

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Hand-rolled ABI encoding for the frozen contracts' exact signatures (ADR-014 —
// contracts/src is the source of truth; compiler-generated full ABIs exist under
// contracts/out for cross-checking). Word-encoding only: every argument below is a
// static 32-byte type, so no dynamic-offset machinery is needed or built.

func selector(sig string) []byte { return crypto.Keccak256([]byte(sig))[:4] }

func word(b []byte) []byte {
	w := make([]byte, 32)
	copy(w[32-len(b):], b)
	return w
}

func addrWord(a common.Address) []byte { return word(a.Bytes()) }
func uintWord(v *big.Int) []byte       { return word(v.Bytes()) }
func b32Word(h [32]byte) []byte        { return h[:] }

func pack(sig string, words ...[]byte) []byte {
	out := selector(sig)
	for _, w := range words {
		out = append(out, w...)
	}
	return out
}

// JobID32 converts the daemon's 0x-hex vault job id into bytes32, fail-closed.
func JobID32(hexID string) ([32]byte, error) {
	var out [32]byte
	h := strings.TrimPrefix(strings.TrimSpace(hexID), "0x")
	if len(h) != 64 {
		return out, fmt.Errorf("vault job id %q is not bytes32 hex", hexID)
	}
	// hex.DecodeString FAILS CLOSED on non-hex characters — common.FromHex would
	// silently return zero/partial bytes for "0xGG…", producing the zero job id with
	// no error, which every chain caller (advance, settlement, invoice, the quote
	// oracle) would then read against a nonexistent job (review: PR #36).
	b, err := hex.DecodeString(h)
	if err != nil {
		return out, fmt.Errorf("vault job id %q is not valid hex: %w", hexID, err)
	}
	copy(out[:], b)
	return out, nil
}

// ── USDC (ERC-20 surface of the predeploy) ──
func CalldataApprove(spender common.Address, amount *big.Int) []byte {
	return pack("approve(address,uint256)", addrWord(spender), uintWord(amount))
}
func CalldataTransfer(to common.Address, amount *big.Int) []byte {
	return pack("transfer(address,uint256)", addrWord(to), uintWord(amount))
}

// ── FloatPool ──
func CalldataDeposit(assets *big.Int, receiver common.Address) []byte {
	return pack("deposit(uint256,address)", uintWord(assets), addrWord(receiver))
}
func CalldataRequestAdvance(jobID [32]byte) []byte {
	return pack("requestAdvance(bytes32)", b32Word(jobID))
}
func CalldataOpenAdvanceOf(jobID [32]byte) []byte {
	return pack("openAdvanceOf(bytes32)", b32Word(jobID))
}
func CalldataAdvanceRate(org common.Address) []byte {
	return pack("advanceRate(address)", addrWord(org))
}

// ── JobVault ──
func CalldataCreateJob(jobID [32]byte, customer, operator common.Address, payment, budget *big.Int, termsHash [32]byte, deadline uint64) []byte {
	return pack("createJob(bytes32,address,address,uint256,uint256,bytes32,uint64)",
		b32Word(jobID), addrWord(customer), addrWord(operator),
		uintWord(payment), uintWord(budget), b32Word(termsHash),
		uintWord(new(big.Int).SetUint64(deadline)))
}
func CalldataFund(jobID [32]byte) []byte      { return pack("fund(bytes32)", b32Word(jobID)) }
func CalldataStartWork(jobID [32]byte) []byte { return pack("startWork(bytes32)", b32Word(jobID)) }
func CalldataRecordExpense(jobID [32]byte, amount *big.Int, receiptHash [32]byte) []byte {
	return pack("recordExpense(bytes32,uint256,bytes32)", b32Word(jobID), uintWord(amount), b32Word(receiptHash))
}
func CalldataSubmitDelivery(jobID [32]byte, deliveryHash [32]byte) []byte {
	return pack("submitDelivery(bytes32,bytes32)", b32Word(jobID), b32Word(deliveryHash))
}
func CalldataAcceptDelivery(jobID [32]byte) []byte {
	return pack("acceptDelivery(bytes32)", b32Word(jobID))
}
func CalldataJobStatus(jobID [32]byte) []byte {
	return pack("jobStatus(bytes32)", b32Word(jobID))
}
func CalldataJobEconomics(jobID [32]byte) []byte {
	return pack("jobEconomics(bytes32)", b32Word(jobID))
}

// ── view decoders (the restart oracle's answers) ──

// DecodeOpenAdvance parses openAdvanceOf's (uint256 principal, uint256 fee, bool open).
func DecodeOpenAdvance(ret []byte) (principal, fee *big.Int, open bool, err error) {
	if len(ret) != 96 {
		return nil, nil, false, fmt.Errorf("openAdvanceOf returned %d bytes, want 96", len(ret))
	}
	principal = new(big.Int).SetBytes(ret[0:32])
	fee = new(big.Int).SetBytes(ret[32:64])
	open = new(big.Int).SetBytes(ret[64:96]).Sign() != 0
	return principal, fee, open, nil
}

// DecodeJobEconomics parses jobEconomics's (address operator, uint256 customerPayment,
// uint256 maxOperatingBudget). The customerPayment is the chain-authoritative quote.
func DecodeJobEconomics(ret []byte) (operator common.Address, customerPayment, maxBudget *big.Int, err error) {
	if len(ret) != 96 {
		return common.Address{}, nil, nil, fmt.Errorf("jobEconomics returned %d bytes, want 96", len(ret))
	}
	operator = common.BytesToAddress(ret[0:32])
	customerPayment = new(big.Int).SetBytes(ret[32:64])
	maxBudget = new(big.Int).SetBytes(ret[64:96])
	return operator, customerPayment, maxBudget, nil
}

// DecodeJobStatus parses jobStatus's enum. 4 = Accepted (the settled terminal).
func DecodeJobStatus(ret []byte) (uint8, error) {
	if len(ret) != 32 {
		return 0, fmt.Errorf("jobStatus returned %d bytes, want 32", len(ret))
	}
	return uint8(new(big.Int).SetBytes(ret).Uint64()), nil
}

// JobStatusAccepted is JobVault's enum value for the settled terminal state.
const JobStatusAccepted uint8 = 4

// Oracle answers the crash-window question from CHAIN STATE — never from a heuristic:
// SC-FP-003 permits exactly one advance per job, so openAdvanceOf says whether an
// advance landed; JobVault's state machine says whether a settlement did.
type Oracle struct {
	Reader    *Client // any client works for views; no key material is used
	FloatPool common.Address
	JobVault  common.Address
	Org       common.Address
}

// AdvanceRateBps reads the standing pipeline organization's current Float rate.
func (o Oracle) AdvanceRateBps(ctx context.Context) (uint64, error) {
	if o.Org == (common.Address{}) {
		return 0, fmt.Errorf("advance-rate organization is not configured")
	}
	ret, err := o.Reader.CallView(ctx, o.FloatPool, CalldataAdvanceRate(o.Org))
	if err != nil {
		return 0, err
	}
	if len(ret) != 32 {
		return 0, fmt.Errorf("advanceRate returned %d bytes, want 32", len(ret))
	}
	value := new(big.Int).SetBytes(ret)
	if !value.IsUint64() {
		return 0, fmt.Errorf("advanceRate does not fit uint64")
	}
	return value.Uint64(), nil
}

// AdvanceLanded reports whether the job's one permitted advance exists on chain.
// NOTE: after settlement the advance closes (open=false) — a nonzero principal with
// open=false still means the advance HAPPENED. Absence is principal==0.
func (o Oracle) AdvanceLanded(ctx context.Context, vaultJobID string) (bool, error) {
	id, err := JobID32(vaultJobID)
	if err != nil {
		return false, err
	}
	ret, err := o.Reader.CallView(ctx, o.FloatPool, CalldataOpenAdvanceOf(id))
	if err != nil {
		return false, err
	}
	principal, _, _, err := DecodeOpenAdvance(ret)
	if err != nil {
		return false, err
	}
	return principal.Sign() != 0, nil
}

// SettlementLanded reports whether the job reached the Accepted terminal on chain.
func (o Oracle) SettlementLanded(ctx context.Context, vaultJobID string) (bool, error) {
	id, err := JobID32(vaultJobID)
	if err != nil {
		return false, err
	}
	ret, err := o.Reader.CallView(ctx, o.JobVault, CalldataJobStatus(id))
	if err != nil {
		return false, err
	}
	status, err := DecodeJobStatus(ret)
	if err != nil {
		return false, err
	}
	return status == JobStatusAccepted, nil
}
