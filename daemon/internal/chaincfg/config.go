// Package chaincfg loads the machine-readable deployment handoff (A1).
//
// The committed JSON names environment variables for deployment-specific addresses; it never
// contains private keys. Load resolves those names, validates the Arc identity and verifies that
// every H1 ABI exists before the indexer is allowed to contact an RPC endpoint.
package chaincfg

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gnanam1990/snapfall/daemon/internal/explorer"
)

// LookupEnv is injected so tests can resolve a deployment without mutating process state.
type LookupEnv func(string) (string, bool)

// Deployment is the resolved chain configuration consumed by the indexer.
type Deployment struct {
	SchemaVersion   int               `json:"schemaVersion"`
	Network         Network           `json:"network"`
	Contracts       Contracts         `json:"contracts"`
	FundedWallets   map[string]string `json:"fundedWallets"`
	WalletAddresses map[string]string `json:"-"`
}

// Network identifies one immutable chain deployment.
type Network struct {
	Name              string `json:"name"`
	ChainID           uint64 `json:"chainId"`
	CAIP2             string `json:"caip2"`
	RPCURL            string `json:"rpcUrl"`
	RPCURLEnv         string `json:"rpcUrlEnv"`
	ExplorerURL       string `json:"explorerUrl"`
	ConfirmationDepth uint64 `json:"confirmationDepth"`
	StartBlock        uint64 `json:"startBlock"`
	StartBlockEnv     string `json:"startBlockEnv"`
}

// Contracts is the complete A1 address set. USDC is included because Arc exposes separate
// native-gas and ERC-20 decimal surfaces; H1 accounting always uses the 6dp ERC-20 surface.
type Contracts struct {
	JobVault    Contract `json:"jobVault"`
	FloatPool   Contract `json:"floatPool"`
	AuditAnchor Contract `json:"auditAnchor"`
	USDC        Contract `json:"usdc"`
}

// Contract is one resolved contract and its ABI artifact.
type Contract struct {
	AddressEnv string `json:"addressEnv"`
	Address    string `json:"-"`
	ABI        string `json:"abi,omitempty"`
	ABIPath    string `json:"-"`
	Decimals   int    `json:"decimals,omitempty"`
}

// Load reads, resolves and validates a deployment. Unknown JSON keys are fatal so a typo can
// never silently point one process at a different chain or address set.
func Load(path string, lookup LookupEnv) (Deployment, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Deployment{}, fmt.Errorf("reading deployment %s: %w", path, err)
	}
	var d Deployment
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return Deployment{}, fmt.Errorf("parsing deployment %s: %w", path, err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return Deployment{}, fmt.Errorf("parsing deployment %s: trailing JSON content", path)
	}
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if err := d.resolve(filepath.Dir(path), lookup); err != nil {
		return Deployment{}, fmt.Errorf("deployment %s: %w", path, err)
	}
	return d, nil
}

func (d *Deployment) resolve(base string, lookup LookupEnv) error {
	if d.SchemaVersion != 1 {
		return fmt.Errorf("schemaVersion must be 1, got %d", d.SchemaVersion)
	}
	if d.Network.Name == "" || d.Network.ChainID == 0 {
		return fmt.Errorf("network name and chainId are required")
	}
	wantCAIP := fmt.Sprintf("eip155:%d", d.Network.ChainID)
	if d.Network.CAIP2 != wantCAIP {
		return fmt.Errorf("caip2 %q does not match chainId (want %q)", d.Network.CAIP2, wantCAIP)
	}
	if d.Network.RPCURLEnv != "" {
		if v, ok := lookup(d.Network.RPCURLEnv); ok && strings.TrimSpace(v) != "" {
			d.Network.RPCURL = strings.TrimSpace(v)
		}
	}
	if !strings.HasPrefix(d.Network.RPCURL, "http://") && !strings.HasPrefix(d.Network.RPCURL, "https://") {
		return fmt.Errorf("rpcUrl must be http(s), got %q", d.Network.RPCURL)
	}
	if _, err := explorer.New(d.Network.ExplorerURL); err != nil {
		return fmt.Errorf("explorerUrl: %w", err)
	}
	if d.Network.StartBlockEnv != "" {
		if v, ok := lookup(d.Network.StartBlockEnv); ok && strings.TrimSpace(v) != "" {
			n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			if err != nil {
				return fmt.Errorf("%s must be a base-10 block number: %w", d.Network.StartBlockEnv, err)
			}
			d.Network.StartBlock = n
		}
	}

	contracts := []struct {
		name string
		c    *Contract
	}{
		{"jobVault", &d.Contracts.JobVault},
		{"floatPool", &d.Contracts.FloatPool},
		{"auditAnchor", &d.Contracts.AuditAnchor},
		{"usdc", &d.Contracts.USDC},
	}
	for _, item := range contracts {
		if item.c.AddressEnv == "" {
			return fmt.Errorf("contracts.%s.addressEnv is required", item.name)
		}
		value, ok := lookup(item.c.AddressEnv)
		if !ok || strings.TrimSpace(value) == "" {
			return fmt.Errorf("required address environment variable %s is not set", item.c.AddressEnv)
		}
		address, err := normalizeAddress(value)
		if err != nil {
			return fmt.Errorf("%s: %w", item.c.AddressEnv, err)
		}
		item.c.Address = address
		if item.c.ABI != "" {
			item.c.ABIPath = filepath.Clean(filepath.Join(base, item.c.ABI))
			if stat, err := os.Stat(item.c.ABIPath); err != nil || stat.IsDir() {
				return fmt.Errorf("contracts.%s ABI %s is not a readable file", item.name, item.c.ABIPath)
			}
		}
	}
	if d.Contracts.USDC.Decimals != 6 {
		return fmt.Errorf("contracts.usdc.decimals must be 6 for H1 accounting, got %d", d.Contracts.USDC.Decimals)
	}

	d.WalletAddresses = make(map[string]string, len(d.FundedWallets))
	for role, envName := range d.FundedWallets {
		value, ok := lookup(envName)
		if !ok || strings.TrimSpace(value) == "" {
			return fmt.Errorf("funded wallet %s requires %s", role, envName)
		}
		address, err := normalizeAddress(value)
		if err != nil {
			return fmt.Errorf("funded wallet %s (%s): %w", role, envName, err)
		}
		d.WalletAddresses[role] = address
	}
	if len(d.WalletAddresses) == 0 {
		return fmt.Errorf("at least one funded wallet is required")
	}
	return nil
}

func normalizeAddress(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if len(v) != 42 || !strings.HasPrefix(v, "0x") {
		return "", fmt.Errorf("address %q must be 20-byte 0x hex", v)
	}
	for _, c := range v[2:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("address %q contains non-hex characters", v)
		}
	}
	return v, nil
}

// IndexerAddresses returns the contract filters in stable order.
func (d Deployment) IndexerAddresses() []string {
	return []string{d.Contracts.JobVault.Address, d.Contracts.FloatPool.Address, d.Contracts.AuditAnchor.Address}
}
