// Package schema embeds the canonical SQLite schema so the daemon applies exactly
// the file that lives in the repo — no drift between docs and runtime.
package schema

import _ "embed"

// SQL is daemon/store/schema.sql, the local state schema (PRD §8.1 entities, §8.5 events).
//
//go:embed schema.sql
var SQL string
