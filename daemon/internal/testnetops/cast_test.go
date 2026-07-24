package testnetops

import (
	"strings"
	"testing"
)

func TestParseCastTransactionHashReadsReceiptField(t *testing.T) {
	const want = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	got, err := parseCastTransactionHash([]byte(`{"status":"0x1","transactionHash":"` + want + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("hash = %q, want %q", got, want)
	}
}

func TestParseCastTransactionHashRejectsWholeOrMalformedReceipt(t *testing.T) {
	for _, raw := range []string{
		`{"status":"0x1"}`,
		`{"transactionHash":"0xfeed"}`,
		`{"transactionHash":"0x` + strings.Repeat("g", 64) + `"}`,
		`not-json`,
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := parseCastTransactionHash([]byte(raw)); err == nil {
				t.Errorf("expected invalid receipt %q to fail", raw)
			}
		})
	}
}
