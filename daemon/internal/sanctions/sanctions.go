// Package sanctions checks a payee address against the Chainalysis sanctions oracle before
// any money moves.
//
// The oracle is a public, read-only contract that Chainalysis keeps in step with sanctions
// designations (SanctionsList, isSanctioned(address) returns bool). Reading it needs no API
// key and no account, and anyone can repeat the same call to check a decision. It lives on
// Ethereum mainnet, not on Arc; an EVM address is the same key on every EVM chain, so the
// payee Arc is about to pay can be looked up there.
//
// This package screens ADDRESSES only. It does not screen a person or a company by name.
//
// It imports nothing from the daemon: purchasing defines the PayeeScreen seam it satisfies,
// and the two meet at the wiring layer.
package sanctions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// OracleAddress is the Chainalysis SanctionsList contract on Ethereum mainnet.
const OracleAddress = "0x40C57923924B5c5c5455c48D93317139ADDaC8fb"

// DefaultRPC is a public Ethereum mainnet endpoint that needs no key.
const DefaultRPC = "https://ethereum-rpc.publicnode.com"

// isSanctionedSelector is the first four bytes of keccak256("isSanctioned(address)").
const isSanctionedSelector = "df592f7d"

const maxResponseBytes = 64 << 10

var addressRE = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

// Oracle reads the sanctions oracle over JSON-RPC.
type Oracle struct {
	RPC      string
	Contract string
	HTTP     *http.Client
}

// NewOracle returns an Oracle on rpc (DefaultRPC when empty) with a bounded timeout.
func NewOracle(rpc string) *Oracle {
	if strings.TrimSpace(rpc) == "" {
		rpc = DefaultRPC
	}
	return &Oracle{RPC: rpc, Contract: OracleAddress, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

// Provider names the screen in refusals and logs.
func (o *Oracle) Provider() string { return "chainalysis-sanctions-oracle" }

// Sanctioned reports whether address is on the oracle's list. Any failure (a malformed
// address, an RPC error, an answer that is not a plain true/false) is an error, never a
// silent "not sanctioned": the caller must refuse to pay when it cannot tell.
func (o *Oracle) Sanctioned(ctx context.Context, address string) (bool, error) {
	if !addressRE.MatchString(address) {
		return false, fmt.Errorf("sanctions: %q is not a 20-byte hex address", address)
	}
	data := "0x" + isSanctionedSelector + strings.Repeat("0", 24) + strings.ToLower(address[2:])
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "eth_call",
		"params": []any{map[string]string{"to": o.Contract, "data": data}, "latest"},
	})
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.RPC, bytes.NewReader(body))
	if err != nil {
		// The parse error would quote the URL, and a keyed provider carries its key there.
		return false, errors.New("sanctions: the oracle RPC URL is not a valid URL")
	}
	req.Header.Set("Content-Type", "application/json")
	client := o.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		// *url.Error prints the full request URL, and a keyed RPC (".../v2/<key>") would put
		// its key into the refusal reason the owner sees. Keep only the underlying cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return false, fmt.Errorf("sanctions: oracle unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return false, fmt.Errorf("sanctions: reading oracle answer: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("sanctions: oracle RPC answered HTTP %d", resp.StatusCode)
	}
	var out struct {
		Result *string `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return false, fmt.Errorf("sanctions: oracle answer is not JSON-RPC: %w", err)
	}
	if out.Error != nil {
		return false, fmt.Errorf("sanctions: oracle RPC error %d: %s", out.Error.Code, out.Error.Message)
	}
	if out.Result == nil {
		return false, errors.New("sanctions: oracle RPC returned no result")
	}
	return decodeBool(*out.Result)
}

// decodeBool accepts exactly one ABI-encoded bool word: 32 bytes, value 0 or 1. Anything
// else (an empty "0x" from a missing contract, a revert payload, a wider word) is an error.
func decodeBool(word string) (bool, error) {
	w := strings.ToLower(strings.TrimPrefix(word, "0x"))
	if len(w) != 64 {
		return false, fmt.Errorf("sanctions: oracle returned %d hex chars, want one 32-byte bool", len(w))
	}
	switch w {
	case strings.Repeat("0", 64):
		return false, nil
	case strings.Repeat("0", 63) + "1":
		return true, nil
	default:
		return false, fmt.Errorf("sanctions: oracle returned a non-bool word %s", word)
	}
}
