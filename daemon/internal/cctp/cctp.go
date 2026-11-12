// Package cctp moves USDC off Arc with Circle's CCTP V2: the operator's payout to a wallet on
// another chain, without a bridge or an exchange in between.
//
// THE FLOW, three steps on two chains: approve TokenMessengerV2 and depositForBurn on Arc (the
// burn), wait for Circle's attestation (Iris), then receiveMessage on the destination (the
// mint). The burn is signed by the operator's own lane (a raw key or the Circle treasury
// wallet, whichever chainops was given). destinationCaller is zero, so the mint may be
// submitted by anyone holding destination gas, including Circle's own forwarder; this package
// only produces what is needed to submit it.
//
// FEES ARE READ, NOT ASSUMED: Arc-source transfers currently quote zero at both finality
// levels, but the fee table is read for every payout and maxFee is set from it, so a fee that
// appears later is paid rather than causing a burn that waits forever for an unpaid fee.
//
// V2 ONLY. Addresses are Circle's CCTP V2 table; V1 is being retired and is not supported.
package cctp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Finality thresholds (Circle: 1000 Fast "confirmed", 2000 Standard "finalized").
const (
	FastFinality     uint32 = 1000
	StandardFinality uint32 = 2000
)

// ArcDomain is Arc's CCTP domain on both networks.
const ArcDomain uint32 = 26

// Network is the CCTP side of one Arc deployment.
type Network struct {
	IrisBase           string
	TokenMessenger     common.Address
	MessageTransmitter common.Address
	// Domains this package will pay out to, by name, so a typo is refused rather than burned
	// to a domain nobody can mint on.
	Domains map[uint32]string
}

// ForArc returns the CCTP V2 network for an Arc chain id.
func ForArc(chainID uint64) (Network, error) {
	switch chainID {
	case 5_042_002:
		return Network{
			IrisBase:           "https://iris-api-sandbox.circle.com",
			TokenMessenger:     common.HexToAddress("0x8FE6B999Dc680CcFDD5Bf7EB0974218be2542DAA"),
			MessageTransmitter: common.HexToAddress("0xE737e5cEBEEBa77EFE34D4aa090756590b1CE275"),
			Domains:            map[uint32]string{0: "Ethereum Sepolia", 3: "Arbitrum Sepolia", 6: "Base Sepolia"},
		}, nil
	case 5_042:
		return Network{
			IrisBase:           "https://iris-api.circle.com",
			TokenMessenger:     common.HexToAddress("0x28b5a0e9C621a5BadaA536219b3a228C8168cf5d"),
			MessageTransmitter: common.HexToAddress("0x81D40F21F12A8F0E3252Bccb954D722d4c464B64"),
			Domains:            map[uint32]string{0: "Ethereum", 6: "Base"},
		}, nil
	}
	return Network{}, fmt.Errorf("cctp: chain %d is not an Arc network", chainID)
}

var (
	bytes32T, _ = abi.NewType("bytes32", "", nil)
	bytesT, _   = abi.NewType("bytes", "", nil)
	uint256T, _ = abi.NewType("uint256", "", nil)
	uint32T, _  = abi.NewType("uint32", "", nil)
	addressT, _ = abi.NewType("address", "", nil)

	depositForBurnArgs = abi.Arguments{
		{Type: uint256T}, {Type: uint32T}, {Type: bytes32T}, {Type: addressT},
		{Type: bytes32T}, {Type: uint256T}, {Type: uint32T},
	}
	receiveMessageArgs = abi.Arguments{{Type: bytesT}, {Type: bytesT}}
)

// Selectors, derived from the signatures in Circle's contract interface reference.
var (
	depositForBurnSel = crypto.Keccak256([]byte("depositForBurn(uint256,uint32,bytes32,address,bytes32,uint256,uint32)"))[:4]
	receiveMessageSel = crypto.Keccak256([]byte("receiveMessage(bytes,bytes)"))[:4]
)

// AddressWord left-pads an EVM address to the bytes32 CCTP uses for mintRecipient.
func AddressWord(a common.Address) [32]byte {
	var w [32]byte
	copy(w[12:], a.Bytes())
	return w
}

// CalldataDepositForBurn burns amount of burnToken on this chain for recipient on destDomain.
// destinationCaller is zero: anyone may complete the mint.
func CalldataDepositForBurn(amount *big.Int, destDomain uint32, recipient common.Address, burnToken common.Address, maxFee *big.Int, finality uint32) ([]byte, error) {
	packed, err := depositForBurnArgs.Pack(amount, destDomain, AddressWord(recipient), burnToken, [32]byte{}, maxFee, finality)
	if err != nil {
		return nil, err
	}
	return append(append([]byte{}, depositForBurnSel...), packed...), nil
}

// CalldataReceiveMessage mints an attested burn on the destination chain.
func CalldataReceiveMessage(message, attestation []byte) ([]byte, error) {
	packed, err := receiveMessageArgs.Pack(message, attestation)
	if err != nil {
		return nil, err
	}
	return append(append([]byte{}, receiveMessageSel...), packed...), nil
}

// MaxFee is ceil(amount * bps / 10000): the cap that lets the protocol charge its full fee.
// Circle quotes fractional bps; they are rounded UP before this, never down.
func MaxFee(amount *big.Int, bps uint64) *big.Int {
	n := new(big.Int).Mul(amount, new(big.Int).SetUint64(bps))
	n.Add(n, big.NewInt(9_999))
	return n.Quo(n, big.NewInt(10_000))
}

// Iris is Circle's attestation service for one network.
type Iris struct {
	Base string
	HTTP *http.Client
	// PollEvery is the wait between attestation reads.
	PollEvery time.Duration
}

func (i Iris) get(ctx context.Context, path string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(i.Base, "/")+path, nil)
	if err != nil {
		return 0, err
	}
	c := i.HTTP
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return resp.StatusCode, nil
	}
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, fmt.Errorf("iris %s: HTTP %d", path, resp.StatusCode)
	}
	return resp.StatusCode, json.Unmarshal(raw, out)
}

// FeeBps reads Circle's minimum fee for src -> dst at the given finality, rounded up to whole bps.
func (i Iris) FeeBps(ctx context.Context, src, dst, finality uint32) (uint64, error) {
	var rows []struct {
		FinalityThreshold uint32  `json:"finalityThreshold"`
		MinimumFee        float64 `json:"minimumFee"`
	}
	if _, err := i.get(ctx, fmt.Sprintf("/v2/burn/USDC/fees/%d/%d", src, dst), &rows); err != nil {
		return 0, err
	}
	for _, r := range rows {
		if r.FinalityThreshold == finality {
			if r.MinimumFee < 0 || r.MinimumFee >= 10_000 {
				return 0, fmt.Errorf("iris quoted an impossible fee %v bps", r.MinimumFee)
			}
			bps := uint64(r.MinimumFee)
			if float64(bps) < r.MinimumFee {
				bps++
			}
			return bps, nil
		}
	}
	return 0, fmt.Errorf("iris has no fee for %d -> %d at finality %d", src, dst, finality)
}

// Attestation is a signed burn, ready to mint.
type Attestation struct {
	Message     []byte
	Attestation []byte
	Recipient   common.Address
	DestDomain  uint32
	Amount      *big.Int
}

var hexRe = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)

// Await polls Iris until the burn in txHash on srcDomain is attested, then checks it mints to
// recipient on destDomain. A signed message for anything else is an error, never returned.
func (i Iris) Await(ctx context.Context, srcDomain uint32, txHash string, destDomain uint32, recipient common.Address, timeout time.Duration) (Attestation, error) {
	deadline := time.Now().Add(timeout)
	every := i.PollEvery
	if every == 0 {
		every = 3 * time.Second
	}
	for {
		var body struct {
			Messages []struct {
				Status         string  `json:"status"`
				Message        string  `json:"message"`
				Attestation    *string `json:"attestation"`
				DecodedMessage *struct {
					DestinationDomain json.Number `json:"destinationDomain"`
					Body              *struct {
						MintRecipient string `json:"mintRecipient"`
						Amount        string `json:"amount"`
					} `json:"decodedMessageBody"`
				} `json:"decodedMessage"`
			} `json:"messages"`
		}
		code, err := i.get(ctx, fmt.Sprintf("/v2/messages/%d?transactionHash=%s", srcDomain, txHash), &body)
		if err != nil {
			return Attestation{}, err
		}
		if code != http.StatusNotFound && len(body.Messages) > 0 {
			m := body.Messages[0]
			if m.Status == "complete" && m.Attestation != nil && hexRe.MatchString(*m.Attestation) && hexRe.MatchString(m.Message) && len(m.Message) > 2 {
				d := m.DecodedMessage
				if d == nil || d.Body == nil {
					return Attestation{}, fmt.Errorf("iris attested %s but its message did not decode; not relaying it", txHash)
				}
				dom, err := d.DestinationDomain.Int64()
				if err != nil || uint32(dom) != destDomain {
					return Attestation{}, fmt.Errorf("the attested burn is for domain %s, not %d; not relaying it", d.DestinationDomain, destDomain)
				}
				mr := common.FromHex(d.Body.MintRecipient)
				// Iris may return either a 20-byte EVM address or its zero-padded bytes32 form.
				validRecipient := len(mr) == 20 || (len(mr) == 32 && new(big.Int).SetBytes(mr[:12]).Sign() == 0)
				if !validRecipient || common.BytesToAddress(mr) != recipient {
					return Attestation{}, fmt.Errorf("the attested burn mints to %s, not %s; not relaying it", d.Body.MintRecipient, recipient.Hex())
				}
				amount, ok := new(big.Int).SetString(d.Body.Amount, 10)
				if !ok {
					return Attestation{}, fmt.Errorf("the attested amount %q does not parse", d.Body.Amount)
				}
				return Attestation{
					Message: common.FromHex(m.Message), Attestation: common.FromHex(*m.Attestation),
					Recipient: recipient, DestDomain: destDomain, Amount: amount,
				}, nil
			}
		}
		if time.Now().After(deadline) {
			return Attestation{}, fmt.Errorf("no attestation for %s within %s; the burn stands; fetch it later with `chainops payout-mint`", txHash, timeout)
		}
		select {
		case <-ctx.Done():
			return Attestation{}, ctx.Err()
		case <-time.After(every):
		}
	}
}
