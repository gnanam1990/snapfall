package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// G2 gate: a crash between an event write and its side effect replays the effect
// EXACTLY once — never zero, never twice.

// effectCount reads how many times the side effect has run, from its own table.
func effectCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	err := s.DB().QueryRow(`SELECT COUNT(*) FROM effect_log`).Scan(&n)
	if err != nil {
		t.Fatalf("counting effects: %v", err)
	}
	return n
}

func setupEffectTable(t *testing.T, s *Store) {
	t.Helper()
	_, err := s.DB().Exec(`CREATE TABLE IF NOT EXISTS effect_log (outbox_id INTEGER NOT NULL)`)
	if err != nil {
		t.Fatalf("creating effect_log: %v", err)
	}
}

// The side effect: record the row in effect_log THROUGH the supplied transaction.
func recordEffect(rowID int64) func(tx *sql.Tx) error {
	return func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO effect_log (outbox_id) VALUES (?)`, rowID)
		return err
	}
}

// The G2 scenario, literally: event written, process dies before the side effect runs,
// store reopens (the "restart"), replay executes the effect exactly once, and a second
// replay does not run it again.
func TestG2_CrashBetweenWriteAndEffect_ReplaysExactlyOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "g2.db")

	// ── Before the crash: the event and its outbox row commit... ──
	s1, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	setupEffectTable(t, s1)
	if _, err := s1.Append(ctx, Event{Kind: "advance.issued", EntityID: "job_104"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// ...and the process dies HERE, before any effect executes.
	s1.Close()

	// ── Restart. ──
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	setupEffectTable(t, s2)

	if got := effectCount(t, s2); got != 0 {
		t.Fatalf("effect ran %d times before replay; the crash simulation is broken", got)
	}

	// Replay: drain unpublished rows through ExecuteEffect.
	rows, err := s2.Unpublished(ctx, 10)
	if err != nil {
		t.Fatalf("Unpublished: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 pending row after crash, got %d", len(rows))
	}
	ran, err := s2.ExecuteEffect(ctx, rows[0].ID, recordEffect(rows[0].ID))
	if err != nil {
		t.Fatalf("ExecuteEffect: %v", err)
	}
	if !ran {
		t.Fatal("replay must run the effect (it never ran before the crash)")
	}
	if got := effectCount(t, s2); got != 1 {
		t.Fatalf("effect ran %d times, want exactly 1 — never zero", got)
	}

	// ── A second replay pass must NOT run it again. ──
	rows, _ = s2.Unpublished(ctx, 10)
	if len(rows) != 0 {
		t.Fatalf("row still pending after successful effect: %d rows", len(rows))
	}
	// Even calling ExecuteEffect directly on the same row is a no-op now.
	ran, err = s2.ExecuteEffect(ctx, 1, recordEffect(1))
	if err != nil {
		t.Fatalf("re-ExecuteEffect: %v", err)
	}
	if ran {
		t.Fatal("a published row must not run its effect again")
	}
	if got := effectCount(t, s2); got != 1 {
		t.Fatalf("effect ran %d times after redundant replay, want exactly 1 — never twice", got)
	}
}

// The other crash window: the effect STARTED but the process died before commit.
// The transaction rolls back, so the half-done effect vanishes and replay runs it
// cleanly — the observable count is still exactly one.
func TestG2_CrashMidEffect_RollsBackThenReplaysOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "g2b.db")

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	setupEffectTable(t, s)

	if _, err := s.Append(ctx, Event{Kind: "job.funded", EntityID: "job_104"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	rows, _ := s.Unpublished(ctx, 10)

	// The effect writes its row and THEN dies (error aborts the tx = crash before commit).
	boom := errors.New("process died mid-effect")
	_, err = s.ExecuteEffect(ctx, rows[0].ID, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO effect_log (outbox_id) VALUES (?)`, rows[0].ID); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("expected the simulated crash, got %v", err)
	}

	// The half-done write must have rolled back with the claim.
	if got := effectCount(t, s); got != 0 {
		t.Fatalf("half-done effect visible after rollback: count %d, want 0", got)
	}
	rows, _ = s.Unpublished(ctx, 10)
	if len(rows) != 1 {
		t.Fatalf("row must still be pending after rollback, got %d rows", len(rows))
	}

	// Replay completes it — once.
	ran, err := s.ExecuteEffect(ctx, rows[0].ID, recordEffect(rows[0].ID))
	if err != nil || !ran {
		t.Fatalf("replay failed: ran=%v err=%v", ran, err)
	}
	if got := effectCount(t, s); got != 1 {
		t.Fatalf("effect count %d, want exactly 1", got)
	}
}

// Two executors racing for the same row: exactly one wins.
func TestG2_ConcurrentExecutorsRunEffectOnce(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "g2c.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	setupEffectTable(t, s)

	if _, err := s.Append(ctx, Event{Kind: "job.accepted"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	rows, _ := s.Unpublished(ctx, 10)

	ranCount := 0
	for i := 0; i < 2; i++ {
		ran, err := s.ExecuteEffect(ctx, rows[0].ID, recordEffect(rows[0].ID))
		if err != nil {
			t.Fatalf("executor %d: %v", i, err)
		}
		if ran {
			ranCount++
		}
	}
	if ranCount != 1 {
		t.Errorf("effect ran %d times across 2 executors, want exactly 1", ranCount)
	}
	if got := effectCount(t, s); got != 1 {
		t.Errorf("effect_log count %d, want 1", got)
	}
}
