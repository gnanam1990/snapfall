package brain

import (
	"context"
	"testing"
)

// The structural quote fix: binding a job to a vault id adopts the CHAIN's quote (via
// the oracle) so the local record and the chain agree by construction — no stub-quote
// divergence. Without an oracle the local quote is preserved (back-compat).
func TestBindVaultJob_AdoptsChainQuote(t *testing.T) {
	b, _, _ := newTestBrain(t)
	b.SetScoper(StubScoper{})
	ctx := context.Background()
	if _, err := b.HandleOwnerRequest(ctx, "job_q", "Acme Corp"); err != nil {
		t.Fatal(err)
	}
	// The stub quote is 25.00 before binding.
	if jm, _ := b.memory.Get("job_q"); jm.QuoteUSDC != "25.00" {
		t.Fatalf("pre-bind quote %q, want the stub 25.00", jm.QuoteUSDC)
	}

	vault := "0x" + repeat64("a")
	// Oracle reports the chain's customerPayment as 1.000000 (the real funded amount).
	b.SetQuoteOracle(func(_ context.Context, v string) (string, bool) {
		if v != vault {
			return "", false
		}
		return "1.000000", true
	})
	if err := b.BindVaultJob(ctx, "job_q", vault); err != nil {
		t.Fatal(err)
	}
	jm, _ := b.memory.Get("job_q")
	if jm.QuoteUSDC != "1.000000" {
		t.Fatalf("post-bind quote %q, want the chain-authoritative 1.000000", jm.QuoteUSDC)
	}
	if jm.VaultJobID != vault {
		t.Fatalf("vault id not bound: %q", jm.VaultJobID)
	}

	// No oracle → the local quote is preserved (a job with no oracle keeps its quote).
	if _, err := b.HandleOwnerRequest(ctx, "job_q2", "Beta Corp"); err != nil {
		t.Fatal(err)
	}
	b.SetQuoteOracle(nil)
	if err := b.BindVaultJob(ctx, "job_q2", "0x"+repeat64("b")); err != nil {
		t.Fatal(err)
	}
	if jm2, _ := b.memory.Get("job_q2"); jm2.QuoteUSDC != "25.00" {
		t.Fatalf("no-oracle bind changed the quote to %q, want the preserved 25.00", jm2.QuoteUSDC)
	}
}
