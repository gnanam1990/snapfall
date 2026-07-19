package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// NFR-007 names WAL explicitly. Rollback-journal mode would weaken the durability
// guarantee the whole event log rests on, so startup asserts rather than assumes.
func TestOpen_UsesWAL(t *testing.T) {
	s := openTemp(t)
	mode, err := s.JournalMode(context.Background())
	if err != nil {
		t.Fatalf("JournalMode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

// The schema must apply cleanly to an existing database — the daemon runs it on every boot.
func TestOpen_SchemaIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")

	s1, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := s1.Append(ctx, Event{Kind: "job.funded", EntityID: "job_104"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	s1.Close()

	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopening must not fail: %v", err)
	}
	defer s2.Close()

	// AT-10 in miniature: state survives a restart.
	n, err := s2.EventCount(ctx)
	if err != nil {
		t.Fatalf("EventCount: %v", err)
	}
	if n != 1 {
		t.Errorf("event count after restart = %d, want 1", n)
	}
}

// The transactional outbox (PRD §6.2): an event and its intent to publish commit together.
// If these could diverge, a crash between them would lose a bus notification — exactly what
// NFR-001 forbids.
func TestAppend_WritesEventAndOutboxAtomically(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	seq, err := s.Append(ctx, Event{
		Kind:     "advance.issued",
		EntityID: "job_104",
		Actor:    "treasury",
		Payload:  map[string]any{"principal": "12.50", "fee": "0.25"},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if seq != 1 {
		t.Errorf("first event seq = %d, want 1", seq)
	}

	rows, err := s.Unpublished(ctx, 10)
	if err != nil {
		t.Fatalf("Unpublished: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("outbox rows = %d, want 1", len(rows))
	}
	if rows[0].Topic != "advance.issued" {
		t.Errorf("topic = %q, want advance.issued", rows[0].Topic)
	}

	var payload map[string]any
	if err := json.Unmarshal(rows[0].Payload, &payload); err != nil {
		t.Fatalf("outbox payload is not valid JSON: %v", err)
	}
	if payload["principal"] != "12.50" {
		t.Errorf("payload lost fields in transit: %v", payload)
	}
}

// FR-AUD-001 tamper evidence / SEC-008: every event carries a hash of its payload,
// which is what may be logged in place of a sensitive payload.
func TestAppend_HashesPayload(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	if _, err := s.Append(ctx, Event{Kind: "payment.signed", Payload: map[string]any{"amount": "0.04"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	var hash string
	if err := s.DB().QueryRowContext(ctx, `SELECT payload_hash FROM events WHERE seq = 1`).Scan(&hash); err != nil {
		t.Fatalf("reading hash: %v", err)
	}
	// sha256 hex is 64 chars, plus the 0x prefix.
	if len(hash) != 66 || hash[:2] != "0x" {
		t.Errorf("payload_hash = %q, want 0x-prefixed sha256", hash)
	}

	// Identical payloads hash identically; different ones do not.
	if _, err := s.Append(ctx, Event{Kind: "payment.signed", Payload: map[string]any{"amount": "0.04"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := s.Append(ctx, Event{Kind: "payment.signed", Payload: map[string]any{"amount": "4.00"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	var same, different string
	s.DB().QueryRowContext(ctx, `SELECT payload_hash FROM events WHERE seq = 2`).Scan(&same)
	s.DB().QueryRowContext(ctx, `SELECT payload_hash FROM events WHERE seq = 3`).Scan(&different)
	if same != hash {
		t.Error("identical payloads must hash identically")
	}
	if different == hash {
		t.Error("a changed amount must change the hash")
	}
}

// FR-AUD-001 requires a monotonic sequence — the audit log's ordering guarantee.
func TestAppend_SequenceIsMonotonic(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	var prev int64
	for i := 0; i < 25; i++ {
		seq, err := s.Append(ctx, Event{Kind: "task.created", EntityID: "t"})
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		if seq <= prev {
			t.Fatalf("seq %d did not increase from %d", seq, prev)
		}
		prev = seq
	}
}

func TestMarkPublished_RemovesRowFromBacklog(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	if _, err := s.Append(ctx, Event{Kind: "job.accepted", EntityID: "job_104"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	rows, _ := s.Unpublished(ctx, 10)
	if len(rows) != 1 {
		t.Fatalf("expected 1 unpublished row, got %d", len(rows))
	}
	if err := s.MarkPublished(ctx, rows[0].ID); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}
	rows, _ = s.Unpublished(ctx, 10)
	if len(rows) != 0 {
		t.Errorf("expected an empty backlog, got %d rows", len(rows))
	}
}

// Ordering matters: the outbox is drained oldest-first so subscribers see events
// in the order they were committed.
func TestUnpublished_ReturnsOldestFirst(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)

	for _, kind := range []string{"job.funded", "advance.issued", "job.accepted"} {
		if _, err := s.Append(ctx, Event{Kind: kind}); err != nil {
			t.Fatalf("Append %s: %v", kind, err)
		}
	}
	rows, err := s.Unpublished(ctx, 10)
	if err != nil {
		t.Fatalf("Unpublished: %v", err)
	}
	want := []string{"job.funded", "advance.issued", "job.accepted"}
	for i, w := range want {
		if rows[i].Topic != w {
			t.Errorf("row %d = %q, want %q", i, rows[i].Topic, w)
		}
	}
}
