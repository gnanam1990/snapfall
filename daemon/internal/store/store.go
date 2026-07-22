// Package store owns local SQLite state (PRD §8.1, NFR-007).
//
// WAL journaling and a transactional outbox are the durability contract: NFR-001 requires
// that no task event is lost after a SQLite commit, so an event and its outbox row are
// written in the same transaction or not at all.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	schema "github.com/gnanam1990/snapfall/daemon/store"
	_ "modernc.org/sqlite" // pure-Go driver; no cgo, so cross-compiling the daemon stays trivial
)

// Store is the daemon's local state handle.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at path and applies the schema.
// Applying is idempotent — every DDL statement in schema.sql is CREATE TABLE IF NOT EXISTS.
func Open(ctx context.Context, path string) (*Store, error) {
	// _pragma args are applied per-connection, which matters because database/sql pools them:
	// setting WAL once on a single connection would not cover the rest.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connecting to %s: %w", path, err)
	}
	if _, err := db.ExecContext(ctx, schema.SQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("applying schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the handle for packages that need their own transactions.
func (s *Store) DB() *sql.DB { return s.db }

// JournalMode reports the active journal mode, so startup can assert WAL took effect
// rather than assume it.
func (s *Store) JournalMode(ctx context.Context) (string, error) {
	var mode string
	err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode)
	return mode, err
}

// Event is one entry in the tamper-evident local log (FR-AUD-001).
// Kind values come from the §8.5 taxonomy, e.g. "job.funded", "advance.issued".
type Event struct {
	Seq      int64
	TS       time.Time
	Kind     string
	EntityID string
	Actor    string
	Payload  any
}

// Append writes an event and its outbox row inside one transaction.
//
// This is the transactional outbox (PRD §6.2): the event and the intent to publish it commit
// together, so a crash between "state changed" and "bus notified" is impossible. The publisher
// drains the outbox separately and at-least-once.
//
// payload_hash is sha256 over the serialized payload — FR-AUD-001 tamper evidence, and the
// value SEC-008 lets us log in place of a sensitive payload.
func (s *Store) Append(ctx context.Context, ev Event) (int64, error) {
	payload, err := json.Marshal(ev.Payload)
	if err != nil {
		return 0, fmt.Errorf("marshalling %s payload: %w", ev.Kind, err)
	}
	sum := sha256.Sum256(payload)
	hash := "0x" + hex.EncodeToString(sum[:])

	ts := ev.TS
	if ts.IsZero() {
		ts = time.Now().UTC()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	res, err := tx.ExecContext(ctx,
		`INSERT INTO events (ts, kind, entity_id, actor, payload_json, payload_hash)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		ts.UnixMilli(), ev.Kind, ev.EntityID, ev.Actor, string(payload), hash)
	if err != nil {
		return 0, fmt.Errorf("appending event %s: %w", ev.Kind, err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO outbox (topic, payload_json, published, created_at) VALUES (?, ?, 0, ?)`,
		ev.Kind, string(payload), ts.UnixMilli()); err != nil {
		return 0, fmt.Errorf("enqueueing outbox row for %s: %w", ev.Kind, err)
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return seq, nil
}

// OutboxRow is an unpublished message awaiting delivery to the bus.
type OutboxRow struct {
	ID      int64
	Topic   string
	Payload []byte
}

// Unpublished returns up to limit rows awaiting delivery, oldest first.
func (s *Store) Unpublished(ctx context.Context, limit int) ([]OutboxRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, topic, payload_json FROM outbox WHERE published = 0 ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []OutboxRow
	for rows.Next() {
		var r OutboxRow
		var payload string
		if err := rows.Scan(&r.ID, &r.Topic, &payload); err != nil {
			return nil, err
		}
		r.Payload = []byte(payload)
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkPublished flips an outbox row after the bus accepted it.
func (s *Store) MarkPublished(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE outbox SET published = 1 WHERE id = ?`, id)
	return err
}

// ExecuteEffect runs a side effect for an outbox row with exactly-once semantics (G2).
//
// The handler receives the transaction in which the row's `published` flag is flipped, and
// MUST perform its state changes through that transaction. Then either both the effect and
// the flag commit, or neither does:
//
//   - crash BEFORE commit  -> effect rolled back, row still unpublished -> replay runs it once
//   - crash AFTER  commit  -> row published -> replay skips it
//
// There is no interleaving in which the effect runs zero times or twice. The published check
// happens inside the transaction, so two concurrent executors cannot both run the same row.
//
// Effects with consequences OUTSIDE this database (an HTTP call, a chain transaction) cannot
// get this guarantee from any local machinery — those must go through an idempotency key at
// the far end, and the handler's job is to record the attempt transactionally. Every purely
// local effect belongs in here.
func (s *Store) ExecuteEffect(ctx context.Context, rowID int64, fn func(tx *sql.Tx) error) (ran bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	// Claim-check inside the tx: a published row is already done.
	var published int
	err = tx.QueryRowContext(ctx, `SELECT published FROM outbox WHERE id = ?`, rowID).Scan(&published)
	if err != nil {
		return false, fmt.Errorf("loading outbox row %d: %w", rowID, err)
	}
	if published != 0 {
		return false, nil
	}

	if err := fn(tx); err != nil {
		return false, fmt.Errorf("effect for outbox row %d: %w", rowID, err)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE outbox SET published = 1 WHERE id = ?`, rowID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// EventCount reports how many events are in the log — used by restart recovery (AT-10)
// to confirm state survived a kill.
func (s *Store) EventCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&n)
	return n, err
}
