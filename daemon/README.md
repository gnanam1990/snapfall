# daemon (owner: B)

Local runtime: supervisor · event gateway · orchestrator · agent workers · action broker ·
memory service · egress proxy · policy engine · treasury signer boundary · chain indexer. (PRD §6.3)

**Language: Go — LOCKED 19 Jul 2026** per ADR-001 and PRD §6.2. The chain indexer shares types
with the daemon, so there is no third language in the stack.

## Run it

```bash
cd daemon

go test ./...                                   # 33 tests
go run ./cmd/snapfall --validate                # FR-ORG-006 manifest check, no daemon
go run ./cmd/snapfall --beats 5 --heartbeat-ms 200   # bounded run, exits cleanly
go run ./cmd/snapfall                           # runs until Ctrl-C
```

Flags: `--db` (default `snapfall.db`), `--manifests`, `--beats`, `--heartbeat-ms`, `--validate`, `-v`.

## Day-1 targets (PRD §14.3 B) — done

- [x] module scaffold + `store/schema.sql` applied (embedded via `go:embed`, so runtime and repo cannot drift)
- [x] supervisor boots one dummy worker
- [x] manifest loader validates `manifests/*.yaml` (FR-ORG-006)
- [x] typed bus + outbox table wired

Not yet: orchestrator/task DAG, action broker, sandbox, policy engine, treasury signer, memory
service, egress proxy, chain indexer. Those are the rest of workstream B.

## Layout

```
cmd/snapfall/          entry point: validate manifests -> open store -> start supervisor
internal/agents/       manifest loader + FR-ORG-006 validation; HeartbeatWorker (the dummy)
internal/store/        SQLite (WAL), event log, transactional outbox
internal/events/       typed bus + outbox publisher
internal/supervisor/   worker lifecycle, restart-with-backoff, health
store/schema.sql       canonical schema (PRD §8.1 entities, §8.5 taxonomy)
manifests/*.yaml       the four bounded roles (PRD §4.1)
```

## Trust boundary law

**Agents propose → typed actions validated → deterministic policy authorizes → isolated
treasury signs → contracts enforce.** LLM output never executes directly (FR-ACT-001).

Manifest validation is where this becomes enforceable rather than aspirational. These are
**fatal** — a manifest asserting any of them will not activate:

| Code | Rule |
| --- | --- |
| `agent-may-sign` | `can_sign_payments: true` — only the treasury signer signs (FR-PAY-001) |
| `agent-may-borrow` | `can_request_advance: true` — advances are human-authorized (FR-FLT-001, SEC-011) |
| `shell-in-allowlist` | `bash`/`sh`/`sudo`/… in `command_allowlist` — arbitrary execution (PRD §4.1) |
| `wildcard-egress` | `*` or `0.0.0.0/0` in `network_allowlist` — defeats deny-by-default (SEC-007) |
| `unknown-role` | a fifth role — PRD §2.5 caps the workforce at four |
| `duplicate-role` / `self-escalation` / `unknown-escalation` | structural incoherence |

Unknown YAML keys are also fatal: `can_sign_payment: true` (missing the `s`) would otherwise
parse as `false` and read as safe while the author believed they had granted signing.

Contradictions are reported as **warnings** and left to a human, per FR-ORG-006's "report."
One is live right now: `delivery.yaml` carries a 0.10 USDC budget with an empty
`network_allowlist`, so the budget is unreachable. Harmless, but either the budget or the
allowlist is wrong.

## Durability

`store.Append` writes the event row and its outbox row in **one transaction** — the
transactional outbox from PRD §6.2. A crash between "state changed" and "bus notified" is
therefore impossible, which is what NFR-001 ("no task event lost after SQLite commit")
requires. Bus delivery is at-least-once and preserves commit order across a failed handler,
so subscribers must be idempotent.

WAL is asserted at startup, not assumed — the daemon refuses to run in rollback-journal mode.

Verify restart recovery by hand:

```bash
cd daemon
rm -f /tmp/sf.db*
go run ./cmd/snapfall --db /tmp/sf.db --beats 2 --heartbeat-ms 100   # existing_events=0, total=4
go run ./cmd/snapfall --db /tmp/sf.db --beats 2 --heartbeat-ms 100   # existing_events=4, total=8
sqlite3 /tmp/sf.db "SELECT COUNT(*) FROM outbox WHERE published=0;"  # 0
```

## Supervision

Workers are **essential** (agents) or **infrastructure** (the outbox publisher, later the
indexer). Crashes restart with exponential backoff up to a budget; a cancelled context is a
clean stop, not a crash, so Ctrl-C does not burn restart budget. When every essential worker
reaches a terminal state the supervisor cancels its run context so infrastructure unwinds
instead of pinning the process open.
