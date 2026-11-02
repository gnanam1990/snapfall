package cctp

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

var recipient = common.HexToAddress("0x00000000000000000000000000000000000000A1")

// The burn calldata decodes back to exactly what was asked: the amount, the domain, the
// recipient as a left-padded bytes32, the token, an OPEN destination caller, the fee cap and
// the finality.
func TestCalldataDepositForBurn_RoundTrips(t *testing.T) {
	usdc := common.HexToAddress("0x3600000000000000000000000000000000000000")
	data, err := CalldataDepositForBurn(big.NewInt(2_500_000), 6, recipient, usdc, big.NewInt(0), StandardFinality)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(data[:4]); got != "8e0250ee" {
		t.Fatalf("selector %s", got)
	}
	vals, err := depositForBurnArgs.Unpack(data[4:])
	if err != nil {
		t.Fatal(err)
	}
	if vals[0].(*big.Int).Int64() != 2_500_000 || vals[1].(uint32) != 6 || vals[3].(common.Address) != usdc ||
		vals[5].(*big.Int).Sign() != 0 || vals[6].(uint32) != StandardFinality {
		t.Fatalf("decoded %v", vals)
	}
	if w := vals[2].([32]byte); common.BytesToAddress(w[12:]) != recipient || w[0] != 0 {
		t.Fatalf("mintRecipient %x", w)
	}
	if vals[4].([32]byte) != ([32]byte{}) {
		t.Fatal("destinationCaller must be open (zero)")
	}

	rm, err := CalldataReceiveMessage([]byte{1, 2, 3}, []byte{4, 5})
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(rm[:4]) != "57ecfd28" {
		t.Fatal("receiveMessage selector")
	}
	back, err := receiveMessageArgs.Unpack(rm[4:])
	if err != nil || string(back[0].([]byte)) != "\x01\x02\x03" || string(back[1].([]byte)) != "\x04\x05" {
		t.Fatalf("receiveMessage args %v %v", back, err)
	}
}

func TestMaxFee_CoversTheFullFee(t *testing.T) {
	for _, c := range []struct {
		amount int64
		bps    uint64
		want   int64
	}{{1_000_000, 0, 0}, {1_000_000, 1, 100}, {1_000_001, 1, 101}, {1, 1, 1}, {999, 13, 2}} {
		if got := MaxFee(big.NewInt(c.amount), c.bps).Int64(); got != c.want {
			t.Fatalf("MaxFee(%d, %d) = %d, want %d", c.amount, c.bps, got, c.want)
		}
	}
}

func TestNetworks_OnlyArc(t *testing.T) {
	if _, err := ForArc(1); err == nil {
		t.Fatal("a non-Arc chain was accepted")
	}
	tn, _ := ForArc(5_042_002)
	mn, _ := ForArc(5_042)
	if tn.TokenMessenger == mn.TokenMessenger || !strings.Contains(tn.IrisBase, "sandbox") || strings.Contains(mn.IrisBase, "sandbox") {
		t.Fatal("testnet and mainnet must not share contracts or attestation service")
	}
}

type fakeIris struct {
	mu        sync.Mutex
	fees      string
	responses []string // served in order for /v2/messages, last one repeats
	calls     int
}

func (f *fakeIris) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/v2/burn/USDC/fees/") {
		_, _ = w.Write([]byte(f.fees))
		return
	}
	i := f.calls
	if i >= len(f.responses) {
		i = len(f.responses) - 1
	}
	f.calls++
	if f.responses[i] == "404" {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write([]byte(f.responses[i]))
}

func message(status, domain, mintRecipient string) string {
	return fmt.Sprintf(`{"messages":[{"status":%q,"message":"0xabcd","attestation":"0xef01",
		"decodedMessage":{"destinationDomain":%q,"decodedMessageBody":{"mintRecipient":%q,"amount":"2500000"}}}]}`,
		status, domain, mintRecipient)
}

func padded(a common.Address) string { w := AddressWord(a); return "0x" + hex.EncodeToString(w[:]) }

func newIris(t *testing.T, f *fakeIris) Iris {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	return Iris{Base: srv.URL, PollEvery: time.Millisecond}
}

func TestFeeBps_RoundsFractionalUp(t *testing.T) {
	iris := newIris(t, &fakeIris{fees: `[{"finalityThreshold":1000,"minimumFee":1.3},{"finalityThreshold":2000,"minimumFee":0}]`})
	if bps, err := iris.FeeBps(context.Background(), 6, 26, FastFinality); err != nil || bps != 2 {
		t.Fatalf("fast fee = %d %v, want 2 (1.3 rounded up)", bps, err)
	}
	if bps, err := iris.FeeBps(context.Background(), 6, 26, StandardFinality); err != nil || bps != 0 {
		t.Fatalf("standard fee = %d %v", bps, err)
	}
	if _, err := iris.FeeBps(context.Background(), 6, 26, 1500); err == nil {
		t.Fatal("an unquoted finality must be an error, not a zero fee")
	}
}

// Iris answers 404 until it indexes the burn, then pending, then complete. Await waits through
// both and returns the signed message.
func TestAwait_WaitsThroughNotFoundAndPending(t *testing.T) {
	f := &fakeIris{responses: []string{"404", message("pending_confirmations", "6", padded(recipient)), message("complete", "6", padded(recipient))}}
	a, err := newIris(t, f).Await(context.Background(), 26, "0xburn", 6, recipient, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if a.Amount.Int64() != 2_500_000 || hex.EncodeToString(a.Message) != "abcd" || hex.EncodeToString(a.Attestation) != "ef01" {
		t.Fatalf("attestation %+v", a)
	}
	if f.calls != 3 {
		t.Fatalf("polls = %d, want 3", f.calls)
	}
}

// A signed message for a different recipient or domain is never handed back for relaying.
func TestAwait_RefusesAMessageForSomeoneElse(t *testing.T) {
	other := common.HexToAddress("0x00000000000000000000000000000000000000B2")
	for name, body := range map[string]string{
		"recipient": message("complete", "6", padded(other)),
		"domain":    message("complete", "0", padded(recipient)),
	} {
		_, err := newIris(t, &fakeIris{responses: []string{body}}).Await(context.Background(), 26, "0xburn", 6, recipient, time.Second)
		if err == nil || !strings.Contains(err.Error(), "not relaying") {
			t.Fatalf("%s: %v, want a refusal", name, err)
		}
	}
}

func TestAwait_TimesOutWithTheBurnStanding(t *testing.T) {
	_, err := newIris(t, &fakeIris{responses: []string{"404"}}).Await(context.Background(), 26, "0xburn", 6, recipient, 5*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "the burn stands") {
		t.Fatalf("timeout: %v", err)
	}
}
