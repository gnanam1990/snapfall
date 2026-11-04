package main

import (
	"context"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/gnanam1990/snapfall/daemon/internal/chain"
	"github.com/gnanam1990/snapfall/daemon/internal/circlewallet"
)

// A THROWAWAY key, never a real wallet.
const treasuryTestKey = "0x59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d"

// With no Circle wallet configured, the treasury is the raw key exactly as before, and it is
// its own reader.
func TestOpenTreasury_RawKeyWhenNoCircleWallet(t *testing.T) {
	t.Setenv(circlewallet.EnvWalletID, "")
	t.Setenv("TREASURY_PRIVATE_KEY", treasuryTestKey)
	s, reader, kind, err := openTreasury(context.Background(), quietLog(), "http://127.0.0.1:1", 5_042_002)
	if err != nil {
		t.Fatal(err)
	}
	if kind != "raw key" {
		t.Fatalf("kind = %q, want the raw-key lane", kind)
	}
	if c, ok := s.(*chain.Client); !ok || c != reader {
		t.Fatal("the raw-key signer should also be the reader")
	}
}

// A stale org address is flagged; a matching or unset one is not.
func TestTreasuryAddressMismatch(t *testing.T) {
	signer := common.HexToAddress("0x00000000000000000000000000000000000000A1")
	for v, want := range map[string]bool{
		"": false, "0x00000000000000000000000000000000000000a1": false,
		"0x00000000000000000000000000000000000000B2": true, "not-an-address": true,
	} {
		t.Setenv("SNAPFALL_TREASURY_ADDRESS", v)
		if _, got := treasuryAddressMismatch(signer); got != want {
			t.Fatalf("SNAPFALL_TREASURY_ADDRESS=%q: mismatch=%v, want %v", v, got, want)
		}
	}
}

// A configured Circle lane that cannot be built wires NOTHING. It never quietly signs with
// the raw key instead, even when a valid one is sitting in the environment.
func TestOpenTreasury_BrokenCircleLaneNeverFallsBackToTheRawKey(t *testing.T) {
	t.Setenv("TREASURY_PRIVATE_KEY", treasuryTestKey)
	t.Setenv(circlewallet.EnvWalletID, "8a1f6c2e-3b4d-4e5f-9a0b-1c2d3e4f5a6b")
	t.Setenv(circlewallet.EnvAPIKey, "INERT:not-a-circle-key:inert-fake-api-secret")
	t.Setenv(circlewallet.EnvEntitySecret, "not-hex")
	s, reader, _, err := openTreasury(context.Background(), quietLog(), "http://127.0.0.1:1", 5_042_002)
	if err == nil || s != nil || reader != nil {
		t.Fatalf("a broken Circle lane produced a signer (%T) err=%v", s, err)
	}
	if !strings.Contains(err.Error(), circlewallet.EnvEntitySecret) {
		t.Fatalf("the refusal should name the variable: %v", err)
	}
	if strings.Contains(err.Error(), "inert-fake-api-secret") {
		t.Fatalf("the refusal echoed the API key: %v", err)
	}
}
