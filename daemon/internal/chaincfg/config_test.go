package chaincfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(values map[string]string) LookupEnv {
	return func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	}
}

func writeDeployment(t *testing.T, extra string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"JobVault.json", "FloatPool.json", "AuditAnchor.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("[]"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	body := `{
  "schemaVersion": 1,
  "network": {"name":"arc-testnet","chainId":5042002,"caip2":"eip155:5042002","rpcUrl":"https://rpc.testnet.arc.network","rpcUrlEnv":"RPC","explorerUrl":"https://testnet.arcscan.app","confirmationDepth":0,"startBlock":0,"startBlockEnv":"START"},
  "contracts": {
    "jobVault":{"addressEnv":"VAULT","abi":"JobVault.json"},
    "floatPool":{"addressEnv":"POOL","abi":"FloatPool.json"},
    "auditAnchor":{"addressEnv":"ANCHOR","abi":"AuditAnchor.json"},
    "usdc":{"addressEnv":"USDC","decimals":6}
  },
  "fundedWallets":{"operatorTreasury":"TREASURY"}` + extra + `
}`
	path := filepath.Join(dir, "deployment.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validEnv() map[string]string {
	return map[string]string{
		"VAULT": "0x1111111111111111111111111111111111111111", "POOL": "0x2222222222222222222222222222222222222222",
		"ANCHOR": "0x3333333333333333333333333333333333333333", "USDC": "0x4444444444444444444444444444444444444444",
		"TREASURY": "0x5555555555555555555555555555555555555555", "RPC": "https://rpc.example", "START": "12345",
	}
}

func TestLoadResolvesDeploymentWithoutSecrets(t *testing.T) {
	d, err := Load(writeDeployment(t, ""), env(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if d.Network.RPCURL != "https://rpc.example" || d.Network.StartBlock != 12345 {
		t.Fatalf("network overrides not resolved: %+v", d.Network)
	}
	if d.Contracts.JobVault.Address != "0x1111111111111111111111111111111111111111" {
		t.Fatalf("vault address = %q", d.Contracts.JobVault.Address)
	}
	if d.WalletAddresses["operatorTreasury"] != "0x5555555555555555555555555555555555555555" {
		t.Fatalf("wallet not resolved: %+v", d.WalletAddresses)
	}
	if len(d.IndexerAddresses()) != 3 {
		t.Fatalf("indexer addresses = %v", d.IndexerAddresses())
	}
}

func TestLoadFailsClosedOnMissingAddress(t *testing.T) {
	values := validEnv()
	delete(values, "POOL")
	_, err := Load(writeDeployment(t, ""), env(values))
	if err == nil || !strings.Contains(err.Error(), "POOL") {
		t.Fatalf("expected missing POOL error, got %v", err)
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	_, err := Load(writeDeployment(t, ",\n  \"surprise\": true"), env(validEnv()))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestLoadRejectsChainIdentityMismatch(t *testing.T) {
	path := writeDeployment(t, "")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), "eip155:5042002", "eip155:1", 1))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Load(path, env(validEnv()))
	if err == nil || !strings.Contains(err.Error(), "does not match chainId") {
		t.Fatalf("expected CAIP mismatch, got %v", err)
	}
}

func TestRepositoryArcConfigIsResolvable(t *testing.T) {
	values := validEnv()
	values["SNAPFALL_JOB_VAULT_ADDRESS"] = values["VAULT"]
	values["SNAPFALL_FLOAT_POOL_ADDRESS"] = values["POOL"]
	values["SNAPFALL_AUDIT_ANCHOR_ADDRESS"] = values["ANCHOR"]
	values["ARC_USDC_ADDRESS"] = values["USDC"]
	values["SNAPFALL_TREASURY_ADDRESS"] = values["TREASURY"]
	values["SNAPFALL_CUSTOMER_ADDRESS"] = "0x6666666666666666666666666666666666666666"
	values["SNAPFALL_DEPLOYMENT_BLOCK"] = "99"
	if _, err := Load(filepath.Join("..", "..", "..", "deployments", "arc-testnet.json"), env(values)); err != nil {
		t.Fatal(err)
	}
}
