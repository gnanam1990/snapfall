package testnetops

import "testing"

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
		`not-json`,
	} {
		if _, err := parseCastTransactionHash([]byte(raw)); err == nil {
			t.Fatalf("expected invalid receipt %q to fail", raw)
		}
	}
}
