package chain

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// balanceOf(address) is the ERC-20 selector 0x70a08231 and the owner as one left-padded word.
func TestCalldataBalanceOf(t *testing.T) {
	got := hex.EncodeToString(CalldataBalanceOf(common.HexToAddress("0x99B723eD097721036C08dd9DEe307286Df3A792D")))
	want := "70a08231" + "00000000000000000000000099b723ed097721036c08dd9dee307286df3a792d"
	if got != want {
		t.Fatalf("calldata %s, want %s", got, want)
	}
}

// VerifyChainID compares the endpoint's eth_chainId with the configured chain.
func TestVerifyChainID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x4cef52"}`))
	}))
	defer srv.Close()
	ok, _ := NewReadOnly(srv.URL, 5_042_002)
	if err := ok.VerifyChainID(context.Background()); err != nil {
		t.Fatalf("matching chain: %v", err)
	}
	wrong, _ := NewReadOnly(srv.URL, 5_042)
	if err := wrong.VerifyChainID(context.Background()); err == nil || !strings.Contains(err.Error(), "endpoint chain id 5042002 != configured 5042") {
		t.Fatalf("mismatch: %v", err)
	}
}
