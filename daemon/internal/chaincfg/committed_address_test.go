package chaincfg

import (
	"path/filepath"
	"strings"
	"testing"
)

// The deploy-artifact contract: committed addresses load with NO env vars set, an env
// var still overrides its committed value, and a contract with neither stays fatal —
// the fail-closed posture is unchanged, only the source of truth gained a committed
// form. Loaded against the REAL deployments/arc-testnet.json so the artifact itself is
// under test, not a fixture shaped like it.
func TestLoad_CommittedDeployArtifactResolvesWithoutEnv(t *testing.T) {
	// Funded WALLETS stay env-only — they are runtime role config, not deploy
	// artifacts. Contract addresses are the artifact under test: zero env for them.
	wallets := func(k string) (string, bool) {
		switch k {
		case "SNAPFALL_TREASURY_ADDRESS":
			return "0x99B723eD097721036C08dd9DEe307286Df3A792D", true
		case "SNAPFALL_CUSTOMER_ADDRESS":
			return "0x00000000000000000000000000000000000000CC", true
		}
		return "", false
	}
	real := filepath.Join("..", "..", "..", "deployments", "arc-testnet.json")
	d, err := Load(real, wallets)
	if err != nil {
		t.Fatalf("the committed deploy artifact must load with no contract env: %v", err)
	}
	if d.Contracts.JobVault.Address != strings.ToLower("0xF3830D7C3B8ca873bB0b277c0e179999e3d52681") &&
		d.Contracts.JobVault.Address != "0xF3830D7C3B8ca873bB0b277c0e179999e3d52681" {
		t.Fatalf("jobVault address: %s", d.Contracts.JobVault.Address)
	}
	if d.Network.StartBlock != 53268443 {
		t.Fatalf("startBlock %d, want the deployment block 53268443", d.Network.StartBlock)
	}

	// Env still wins over the committed value.
	override := "0x00000000000000000000000000000000000000AA"
	d2, err := Load(real, func(k string) (string, bool) {
		if k == "SNAPFALL_JOB_VAULT_ADDRESS" {
			return override, true
		}
		return wallets(k)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(d2.Contracts.JobVault.Address, override) {
		t.Fatalf("env must override the committed address: %s", d2.Contracts.JobVault.Address)
	}
}
