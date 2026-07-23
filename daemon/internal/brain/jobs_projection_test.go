package brain

import (
	"context"
	"os"
	"strings"
	"testing"
)

// The A4-producer half that needs no chain: Brain projects every job into the SQL
// `jobs` table Anandan's reconciler joins against. The FILE-BASED JobMemory stays
// authoritative — the projection is derived, write-only, and heals from memory.

// A tampered SQL row loses to memory: ReprojectJobs (the Recover path) rewrites the
// projection from the memory files, so if the two ever disagree, memory wins — the
// projection cannot drift into a second source of truth.
func TestProjection_MemoryWinsOverTamperedRow(t *testing.T) {
	b, st, _ := newTestBrain(t)
	b.SetScoper(StubScoper{})
	ctx := context.Background()
	if _, err := b.HandleOwnerRequest(ctx, "job_proj", "Acme Corp"); err != nil {
		t.Fatal(err)
	}

	var status, quote string
	row := func() {
		t.Helper()
		if err := st.DB().QueryRow(`SELECT status, quote_usdc FROM jobs WHERE id='job_proj'`).Scan(&status, &quote); err != nil {
			t.Fatalf("projected row: %v", err)
		}
	}
	row()
	if quote != "25.00" || status == "" {
		t.Fatalf("projection after creation: status=%q quote=%q", status, quote)
	}

	// Tamper with the projection directly; memory has not changed.
	if _, err := st.DB().Exec(`UPDATE jobs SET quote_usdc='999.00', status='forged' WHERE id='job_proj'`); err != nil {
		t.Fatal(err)
	}
	if err := b.ReprojectJobs(ctx); err != nil {
		t.Fatal(err)
	}
	row()
	if quote != "25.00" || status == "forged" {
		t.Fatalf("memory must win over a tampered row: status=%q quote=%q", status, quote)
	}
}

// Every memory write projects — including the helper paths that route through Update.
// A stage change lands in the SQL row without any call site knowing projection exists.
func TestProjection_TracksLifecycleWrites(t *testing.T) {
	b, st, _ := newTestBrain(t)
	b.SetScoper(StubScoper{})
	ctx := context.Background()
	if _, err := b.HandleOwnerRequest(ctx, "job_track", "Acme Corp"); err != nil {
		t.Fatal(err)
	}
	if err := b.memory.Update("job_track", func(jm *JobMemory) {
		jm.Stage, jm.VaultJobID = "delivery_ready", "0xabcd"
	}); err != nil {
		t.Fatal(err)
	}
	var status string
	var vault string
	if err := st.DB().QueryRow(`SELECT status, COALESCE(vault_job_id,'') FROM jobs WHERE id='job_track'`).Scan(&status, &vault); err != nil {
		t.Fatal(err)
	}
	if status != "delivery_ready" || vault != "0xabcd" {
		t.Fatalf("projection out of date: status=%q vault=%q", status, vault)
	}
}

// Structural pin: the projection is WRITE-ONLY and single-sited. Exactly one INSERT
// INTO jobs exists in this package, and nothing in it ever reads FROM the jobs table —
// so no code path can treat the SQL rows as authoritative job state.
func TestProjection_SingleWriteSiteAndNoReads(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	writes, reads := 0, 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		writes += strings.Count(string(src), "INSERT INTO jobs")
		reads += strings.Count(string(src), "FROM jobs")
	}
	if writes != 1 {
		t.Fatalf("jobs-projection write sites = %d, want exactly 1", writes)
	}
	if reads != 0 {
		t.Fatalf("brain reads FROM jobs %d times — the projection must never be read as truth", reads)
	}
}
