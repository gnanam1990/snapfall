package explorer

import (
	"strings"
	"testing"
)

const (
	testHash    = "0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	testAddress = "0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
)

func TestExplorerBuildsCanonicalArcScanLinks(t *testing.T) {
	links, err := New(" https://testnet.arcscan.app/ ")
	if err != nil {
		t.Fatal(err)
	}
	txURL, err := links.TransactionURL(testHash)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://testnet.arcscan.app/tx/" + strings.ToLower(testHash); txURL != want {
		t.Fatalf("transaction URL = %q, want %q", txURL, want)
	}
	addressURL, err := links.AddressURL(testAddress)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://testnet.arcscan.app/address/" + strings.ToLower(testAddress); addressURL != want {
		t.Fatalf("address URL = %q, want %q", addressURL, want)
	}
}

func TestExplorerPreservesConfiguredPathPrefix(t *testing.T) {
	links, err := New("https://explorer.example/arc/")
	if err != nil {
		t.Fatal(err)
	}
	got, err := links.TransactionURL(testHash)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://explorer.example/arc/tx/" + strings.ToLower(testHash)
	if got != want {
		t.Fatalf("transaction URL = %q, want %q", got, want)
	}
}

func TestNewRejectsUnsafeOrAmbiguousBaseURL(t *testing.T) {
	for _, raw := range []string{
		"", "testnet.arcscan.app", "javascript:alert(1)", "ftp://testnet.arcscan.app",
		"https://user@example.com", "https://example.com?network=arc", "https://example.com/#arc",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := New(raw); err == nil {
				t.Fatalf("New(%q) succeeded", raw)
			}
		})
	}
}

func TestExplorerRejectsMalformedChainIdentifiers(t *testing.T) {
	links, err := New("https://testnet.arcscan.app")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "0x1234", testAddress, "0x" + strings.Repeat("z", 64)} {
		if _, err := links.TransactionURL(value); err == nil {
			t.Errorf("TransactionURL(%q) succeeded", value)
		}
	}
	for _, value := range []string{"", "0x1234", testHash, "0x" + strings.Repeat("z", 40)} {
		if _, err := links.AddressURL(value); err == nil {
			t.Errorf("AddressURL(%q) succeeded", value)
		}
	}
}
