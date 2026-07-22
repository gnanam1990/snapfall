// Package explorer builds validated links into an EVM block explorer.
package explorer

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

// Explorer owns the URL contract used by H2 financial rows. Values are validated before they
// become clickable links, so malformed chain data cannot be turned into an arbitrary URL.
type Explorer struct {
	baseURL string
}

// New validates and normalizes an explorer base URL.
func New(rawBaseURL string) (*Explorer, error) {
	rawBaseURL = strings.TrimSpace(rawBaseURL)
	parsed, err := url.Parse(rawBaseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing explorer URL: %w", err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, fmt.Errorf("explorer URL scheme must be http or https")
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("explorer URL must include a host")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("explorer URL must not include user information")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("explorer URL must not include a query or fragment")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return &Explorer{baseURL: strings.TrimRight(parsed.String(), "/")}, nil
}

// TransactionURL returns the canonical explorer URL for one EVM transaction hash.
func (e *Explorer) TransactionURL(transactionHash string) (string, error) {
	value, err := normalizeHex(transactionHash, 32, "transaction hash")
	if err != nil {
		return "", err
	}
	return e.baseURL + "/tx/" + value, nil
}

// AddressURL returns the canonical explorer URL for one EVM account or contract address.
func (e *Explorer) AddressURL(address string) (string, error) {
	value, err := normalizeHex(address, 20, "address")
	if err != nil {
		return "", err
	}
	return e.baseURL + "/address/" + value, nil
}

func normalizeHex(value string, byteLength int, kind string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 2+byteLength*2 || !strings.HasPrefix(value, "0x") {
		return "", fmt.Errorf("%s must be %d-byte 0x hex", kind, byteLength)
	}
	if _, err := hex.DecodeString(value[2:]); err != nil {
		return "", fmt.Errorf("%s contains non-hex characters", kind)
	}
	return value, nil
}
