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

// The 12 Nov 2026 mainnet deployment (docs/addresses.md, "Arc mainnet") is committed, so the
// artifact loads with no address env at all, and it must resolve to exactly those contracts:
// a wrong committed address points the indexer, and eventually a signer, at someone else's code.
func TestLoad_MainnetArtifactCommitsTheDeployment(t *testing.T) {
	d, err := Load(mainnetArtifact(), mainnetWallets)
	if err != nil {
		t.Fatalf("arc-mainnet.json must load from its committed addresses: %v", err)
	}
	for name, got := range map[string]string{
		"jobVault":    d.Contracts.JobVault.Address,
		"floatPool":   d.Contracts.FloatPool.Address,
		"auditAnchor": d.Contracts.AuditAnchor.Address,
	} {
		want := map[string]string{
			"jobVault":    "0x67FeD7B5eaA966c4cc2738d1530f230B31B6f372",
			"floatPool":   "0xD2FaB9CD08a73B476e8DC81Afb7b57564dEef08f",
			"auditAnchor": "0xe8886A39158C7CC504F31789513b926704CCaB4D",
		}[name]
		if !strings.EqualFold(got, want) {
			t.Fatalf("%s %s, want %s", name, got, want)
		}
	}
	if d.Network.StartBlock != 30820652 {
		t.Fatalf("startBlock %d, want 30820652 (the AuditAnchor creation block)", d.Network.StartBlock)
	}
}

// An address in the environment still overrides the committed one (a redeploy is pointed at
// before its record is committed), and the artifact carries the mainnet network identity.
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
