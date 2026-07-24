# daemon (owner: B)

Local runtime: supervisor · event gateway · orchestrator · agent workers · action broker ·
memory service · egress proxy · policy engine · treasury signer boundary · chain indexer. (PRD §6.3)

**Language: Go — LOCKED 19 Jul 2026** per ADR-001 and PRD §6.2. The chain indexer shares types
with the daemon, so there is no third language in the stack.

## Run it

```bash
cd daemon

# 33 tests
go test ./...

# FR-ORG-006 manifest check, no daemon
go run ./cmd/snapfall --validate

# bounded run, exits cleanly
go run ./cmd/snapfall --beats 5 --heartbeat-ms 200

# runs until Ctrl-C
go run ./cmd/snapfall
```

> Comments go on their own line on purpose. Interactive zsh has `interactive_comments`
> off, so a trailing `# note` is passed to the command as arguments rather than ignored.

Flags: `--db` (default `snapfall.db`), `--manifests`, `--beats`, `--heartbeat-ms`, `--validate`, `-v`.

## Day-1 targets (PRD §14.3 B) — done

- [x] module scaffold + `store/schema.sql` applied (embedded via `go:embed`, so runtime and repo cannot drift)
- [x] supervisor boots one dummy worker
- [x] manifest loader validates `manifests/*.yaml` (FR-ORG-006)
- [x] typed bus + outbox table wired

The Anandan H1 chain indexer now has a standalone polling command, eight-event decoder,
transactional SQLite projection/cursor, and local-ledger reconciliation. It is not yet registered
as a worker in the main daemon; keeping the first integration behind `cmd/indexer` avoids a
cross-workstream change to the supervisor while H1 is under review.

Not yet: orchestrator/task DAG, action broker, sandbox, policy engine, real treasury signer,
memory service, egress proxy, and wiring the indexer into the main daemon.

## Chain indexer — read before writing it

Follow **docs.arc.io/arc/tutorials/monitor-contract-events** for the subscription/polling
pattern; do not invent one.

**Arc block timestamps are non-decreasing, not strictly increasing** — consecutive blocks may
carry the *same* timestamp. Two consequences for the indexer:

- **Never order events by timestamp alone.** Order by `(blockNumber, logIndex)`, which is
  total; a timestamp sort is not stable across same-timestamp blocks and will silently
  reshuffle events within a block.
- **Never use `timestamp > lastSeen` as a cursor.** A strict comparison drops every event in a
  block sharing the previous block's timestamp. Events remain ordered by `(blockNumber,
  logIndex)`; the durable polling cursor advances only after a complete block range commits.

The same rule already governs the contracts (see the deadline/window logic there) — the
indexer must agree with them, or replay after a restart will not reproduce the same ordering.

Run one H1 catch-up after exporting the deployment addresses listed in
`../deployments/README.md`. For every post-genesis deployment, set its deployment block first;
this avoids scanning unrelated Arc history:

```bash
cd daemon
export SNAPFALL_DEPLOYMENT_BLOCK=<deployment-block>
go run ./cmd/indexer --once --deployment ../deployments/arc-testnet.json --db snapfall.db
```

Without `--once`, the command polls continuously. It verifies `eth_chainId` before reading logs,
requests block ranges bounded by `--chunk-size`, and atomically commits each range's raw logs,
supported normalized H1 events, financial projections and next-block cursor. Replaying an
inclusive range is safe by `(chainId, transactionHash, logIndex)`. The command requires an
explicit `--deployment` path so its behavior does not depend on the process working directory.

## Build-Monitor standing pipelines (A11)

Brain's `OpenMilestone` interface scopes each milestone under a stable standing-instruction
ID. The pair `(standingInstructionID, milestoneNumber)` deterministically produces a fresh
local job ID and a fresh bytes32 JobVault job ID; reopening the same pair is refused. Opening
does not confirm work, authorize an advance, or release escrow—the existing owner, funding,
and customer gates remain unchanged.

The registered `build-monitor` worker reads `<repository>/.snapfall/milestone.json`, checks
the listed repository-relative artifacts without executing repository code, and reports the
measured percentage and current Git revision to Brain before emitting its evidence report:

```json
{
  "checks": [
    { "name": "contract", "path": "dist/contract.json" },
    { "name": "integration", "path": "reports/integration.txt" },
    { "name": "release", "path": "reports/release.txt" }
  ]
}
```

After a milestone settlement, Brain verifies the advance and accepted JobVault state through
the chain oracle, reads the organization's resulting FloatPool rate, and records
`pipeline.milestone.completed`. If no chain oracle is wired, it records an explicitly pending
observation rather than inventing a result. The AT-17 integration test drives two milestones
through distinct advances and settlements and pins progress-before-release plus the second
rate tick.

The A12 owner API exposes the reviewed catalog at `GET /api/v1/workforce/manifests` and
durable activation state at `GET /api/v1/workforce/activations`.
`POST /api/v1/workforce/build-monitor/hire` accepts `repository`, `quoteUsdc`, and `by`;
it opens the first standing-pipeline milestone and records the owner confirmation, which
dispatches Build-Monitor immediately. Repeating the same activation resumes its deterministic
cycle instead of creating a duplicate. Responses expose Brain's real lifecycle stage rather
than claiming that the one-shot repository measurement watches indefinitely. These routes
remain under the existing owner bearer or loopback-only boundary.

## Telegram approval mirror (A13)

Telegram is an optional notification mirror over the existing H2 approval inbox. It never
approves or rejects directly: each inline action opens `/approvals` with the request and
decision preselected, where the dashboard renders the current request and posts the displayed
`intentHash` through H2. Repeated decisions therefore retain G7's existing idempotency and
stale-view protections.

Configure the bot at runtime only:

```bash
export SNAPFALL_TELEGRAM_BOT_TOKEN=<BotFather token>
export SNAPFALL_TELEGRAM_CHAT_ID=<owner chat id>
export SNAPFALL_DASHBOARD_URL=https://dashboard.example
go run ./cmd/snapfall
```

`SNAPFALL_DASHBOARD_URL` defaults to `http://127.0.0.1:3000`; set a reachable HTTPS URL when
opening notifications on another device. Token and chat ID must be configured together or the
daemon refuses to start. With neither set, Telegram remains disabled. `.env` files are ignored,
and the bot token is never included in message bodies or error logs.

## Layout

```
cmd/snapfall/          entry point: validate manifests -> open store -> start supervisor
cmd/indexer/           A2/A3 Arc poller + A4 reconciliation runner
internal/agents/       manifest loader + FR-ORG-006 validation; HeartbeatWorker (the dummy)
internal/chaincfg/      A1 deployment/config loader; resolves addresses from env
internal/explorer/      A5 validated transaction/address explorer links for H2 rows
internal/indexer/       H1 RPC adapter, decoder, projection, cursor, reconciliation
internal/worker/        bounded workers, including A11 Build-Monitor repo measurement
internal/integration/   cross-module acceptance paths, including A11 / AT-17
internal/store/        SQLite (WAL), event log, transactional outbox
internal/events/       typed bus + outbox publisher
internal/supervisor/   worker lifecycle, restart-with-backoff, health
internal/telegram/     optional A13 approval mirror; dashboard deep links only
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
rm -f /tmp/sf.db /tmp/sf.db-wal /tmp/sf.db-shm

# first run: existing_events=0, events_total=4
go run ./cmd/snapfall --db /tmp/sf.db --beats 2 --heartbeat-ms 100

# restart: existing_events=4, events_total=8
go run ./cmd/snapfall --db /tmp/sf.db --beats 2 --heartbeat-ms 100

# backlog must be 0 — every event was published before shutdown
sqlite3 /tmp/sf.db "SELECT COUNT(*) FROM outbox WHERE published=0;"
```

## Supervision

Workers are **essential** (agents) or **infrastructure** (the outbox publisher, later the
indexer). Crashes restart with exponential backoff up to a budget; a cancelled context is a
clean stop, not a crash, so Ctrl-C does not burn restart budget. When every essential worker
reaches a terminal state the supervisor cancels its run context so infrastructure unwinds
instead of pinning the process open.
