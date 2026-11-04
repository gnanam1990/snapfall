package chaincfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func noEnv(string) (string, bool) { return "", false }

// LoadNetwork serves setup tools that run before the operator's addresses exist: both
// committed artifacts load with nothing in the environment, where Load refuses.
func TestLoadNetwork_NeedsNoOperatorAddresses(t *testing.T) {
	testnet := filepath.Join("..", "..", "..", "deployments", "arc-testnet.json")
	for _, tc := range []struct {
		path    string
		chainID uint64
	}{{testnet, 5_042_002}, {mainnetArtifact(), 5_042}} {
		if _, err := Load(tc.path, noEnv); err == nil {
			t.Fatalf("%s: Load must still refuse without operator addresses", tc.path)
		}
		n, usdc, err := LoadNetwork(tc.path, noEnv)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		if n.ChainID != tc.chainID || usdc.Address != "0x3600000000000000000000000000000000000000" || usdc.Decimals != 6 {
			t.Fatalf("%s: chain %d usdc %+v", tc.path, n.ChainID, usdc)
		}
	}
}

// Same strictness on what it does read: the RPC override applies, and a bad USDC override
// or an unknown key still fails.
func TestLoadNetwork_KeepsTheStrictParts(t *testing.T) {
	testnet := filepath.Join("..", "..", "..", "deployments", "arc-testnet.json")
	n, _, err := LoadNetwork(testnet, func(k string) (string, bool) {
		if k == "ARC_TESTNET_RPC" {
			return "https://rpc.example/v2/override", true
		}
		return "", false
	})
	if err != nil || n.RPCURL != "https://rpc.example/v2/override" {
		t.Fatalf("rpc override: %q %v", n.RPCURL, err)
	}
	if _, _, err := LoadNetwork(testnet, func(k string) (string, bool) {
		if k == "ARC_USDC_ADDRESS" {
			return "0xnot-an-address", true
		}
		return "", false
	}); err == nil || !strings.Contains(err.Error(), "ARC_USDC_ADDRESS") {
		t.Fatalf("bad USDC override: %v", err)
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "d.json")
	if err := os.WriteFile(bad, []byte(`{"schemaVersion":1,"typo":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadNetwork(bad, noEnv); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown key: %v", err)
	}
}
