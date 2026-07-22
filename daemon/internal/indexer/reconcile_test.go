package indexer

import (
	"context"
	"testing"
)

func TestReconciliationRaisesAndResolvesStructuredAlert(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	idx := newTestIndexer(t, st, &fakeSource{head: 106, logs: fixtureLogs(t)})
	if _, err := idx.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := st.DB().ExecContext(ctx, `INSERT INTO organizations (id, owner, name, created_at) VALUES ('org', 'owner', 'Snapfall', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().ExecContext(ctx, `
		INSERT INTO jobs
		  (id, org_id, status, quote_usdc, advance_principal_usdc, advance_fee_usdc, advance_status, vault_job_id, created_at)
		VALUES
		  ('local-a', 'org', 'Accepted', '24.99', '12.50', '0.25', 'Repaid', ?, 1),
		  ('local-b', 'org', 'Refunded', NULL, NULL, NULL, 'WrittenOff', ?, 1)`, jobA, jobB); err != nil {
		t.Fatal(err)
	}
	reconciler, err := NewReconciler(st, testChain)
	if err != nil {
		t.Fatal(err)
	}
	result, err := reconciler.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !result.HasMismatch || len(result.Alerts) != 1 {
		t.Fatalf("reconciliation = %+v", result)
	}
	alert := result.Alerts[0]
	if alert.Field != "funded_amount" || alert.Local != "24990000" || alert.Chain != "25000000" {
		t.Fatalf("alert = %+v", alert)
	}

	if _, err := st.DB().ExecContext(ctx, `UPDATE jobs SET quote_usdc = '25.00' WHERE id = 'local-a'`); err != nil {
		t.Fatal(err)
	}
	result, err = reconciler.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.HasMismatch || len(result.Alerts) != 0 {
		t.Fatalf("resolved reconciliation = %+v", result)
	}
	var resolved int
	if err := st.DB().QueryRowContext(ctx, `SELECT resolved FROM reconciliation_alerts WHERE job_id = ? AND field = 'funded_amount'`, jobA).Scan(&resolved); err != nil {
		t.Fatal(err)
	}
	if resolved != 1 {
		t.Fatalf("alert was not resolved")
	}
}

func TestUSDCAtomicNeverRounds(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"25", "25000000"}, {"12.50", "12500000"}, {"0.000001", "1"}, {"0001.2", "1200000"},
	} {
		got, err := usdcAtomic(tc.input)
		if err != nil || got != tc.want {
			t.Fatalf("usdcAtomic(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
		}
	}
	for _, input := range []string{"", "-1", "1.", "1.0000001", "1e6", ".5"} {
		if got, err := usdcAtomic(input); err == nil {
			t.Fatalf("usdcAtomic(%q) unexpectedly accepted as %s", input, got)
		}
	}
}
