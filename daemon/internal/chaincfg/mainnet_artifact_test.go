package chaincfg

import (
	"path/filepath"
	"strings"
	"testing"
)

// deployments/arc-mainnet.json under test, not a fixture shaped like it.
func mainnetArtifact() string {
	return filepath.Join("..", "..", "..", "deployments", "arc-mainnet.json")
}

func mainnetWallets(k string) (string, bool) {
	switch k {
	case "SNAPFALL_TREASURY_ADDRESS":
		return "0x99B723eD097721036C08dd9DEe307286Df3A792D", true
	case "SNAPFALL_CUSTOMER_ADDRESS":
		return "0x00000000000000000000000000000000000000CC", true
	}
	return "", false
}

// Nothing is deployed to mainnet yet, so the artifact commits no contract addresses. Loading
// it with none supplied must FAIL — a mainnet config that silently resolved to a zero address
// would point the indexer, and eventually a signer, at nothing.
func TestLoad_MainnetArtifactFailsClosedWithoutAddresses(t *testing.T) {
	if _, err := Load(mainnetArtifact(), mainnetWallets); err == nil {
		t.Fatal("arc-mainnet.json must not load while its contract addresses are unknown")
	}
}

// Once a deployment exists and its addresses are in the environment, the same artifact loads
// and carries the mainnet network identity.
func TestLoad_MainnetArtifactResolvesFromEnv(t *testing.T) {
	const (
		jobVault    = "0x00000000000000000000000000000000000000A1"
		floatPool   = "0x00000000000000000000000000000000000000A2"
		auditAnchor = "0x00000000000000000000000000000000000000A3"
	)

	d, err := Load(mainnetArtifact(), func(k string) (string, bool) {
		switch k {
		case "SNAPFALL_JOB_VAULT_ADDRESS":
			return jobVault, true
		case "SNAPFALL_FLOAT_POOL_ADDRESS":
			return floatPool, true
		case "SNAPFALL_AUDIT_ANCHOR_ADDRESS":
			return auditAnchor, true
		}
		return mainnetWallets(k)
	})
	if err != nil {
		t.Fatalf("arc-mainnet.json must load once its addresses are supplied: %v", err)
	}

	if d.Network.ChainID != 5042 {
		t.Fatalf("chainId %d, want 5042 (Arc mainnet, live 16 Sep 2026)", d.Network.ChainID)
	}
	if d.Network.Name != "arc-mainnet" {
		t.Fatalf("network name %q", d.Network.Name)
	}
	// rpc.mainnet.arc.network does not resolve; only the .io host does. A typo here is a
	// deployment pointed at nothing, which is the failure this pins.
	if d.Network.RPCURL != "https://rpc.mainnet.arc.io" {
		t.Fatalf("rpcUrl %q, want https://rpc.mainnet.arc.io", d.Network.RPCURL)
	}
	if !strings.EqualFold(d.Contracts.JobVault.Address, jobVault) {
		t.Fatalf("jobVault %s", d.Contracts.JobVault.Address)
	}
	if !strings.EqualFold(d.Contracts.FloatPool.Address, floatPool) {
		t.Fatalf("floatPool %s", d.Contracts.FloatPool.Address)
	}

	// USDC sits at the same precompile on both networks and is committed, so it needs no env.
	// Verified against mainnet 19 Sep 2026: symbol USDC, decimals 6.
	if !strings.EqualFold(d.Contracts.USDC.Address, "0x3600000000000000000000000000000000000000") {
		t.Fatalf("usdc %s", d.Contracts.USDC.Address)
	}
}

// The two networks must not be confusable. A run pointed at the wrong artifact should be
// obvious from the chain id alone, and the CAIP-2 string has to agree with it.
func TestLoad_MainnetAndTestnetAreDistinct(t *testing.T) {
	testnet, err := Load(filepath.Join("..", "..", "..", "deployments", "arc-testnet.json"), mainnetWallets)
	if err != nil {
		t.Fatalf("testnet artifact: %v", err)
	}
	if testnet.Network.ChainID == 5042 {
		t.Fatal("the testnet artifact must not carry the mainnet chain id")
	}
	if testnet.Network.CAIP2 == "eip155:5042" {
		t.Fatal("the testnet artifact must not carry the mainnet caip2 id")
	}
}
