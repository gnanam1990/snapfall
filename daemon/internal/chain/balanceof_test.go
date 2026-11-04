package chain

import (
	"encoding/hex"
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
