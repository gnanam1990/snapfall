# Snapfall H1 — Chain Indexer Event Contract (v1.0-rc)

**Status:** Design proposal for the Wed 22 Jul handshake session. **Freezes:** Fri 24 Jul 2026.
**After freeze:** changes require all-three sign-off and an `x-h1-version` bump.
**Producer:** Anandan's Arc indexer. **Consumer:** Gnanam's shared SQLite/event runtime.

H1 turns Arc contract logs into one stable, replayable local stream. Raw ABI names stay at the
chain edge; every downstream caller consumes the normalized names below.

## 1. Ordering, cursor and idempotency

- Canonical order is `(blockNumber, logIndex)`. Timestamps MUST NOT order or resume the stream.
- The durable resume cursor is `nextBlockNumber`, the next inclusive block to request. It advances
  only after a complete block range, in the same SQLite transaction as that range's raw logs,
  supported normalized events and financial projections. A crash therefore replays from the
  previous committed block boundary.
- A raw log is unique by `(chainId, transactionHash, logIndex)`. Replaying an inclusive block
  range therefore produces no duplicate normalized events or financial effects.
- Arc finality is deterministic on inclusion. The deployment config nevertheless carries a
  `confirmationDepth` field so a different environment cannot silently weaken this assumption.
- A log marked `removed=true` fails the batch closed; the cursor does not advance.

## 2. Common normalized envelope

```json
{
  "h1Version": "1.0",
  "kind": "JobFunded",
  "entityId": "0x…32-byte-job-id",
  "actor": "0x…optional-address",
  "chainId": 5042002,
  "blockNumber": 123,
  "logIndex": 4,
  "transactionHash": "0x…",
  "contractAddress": "0x…",
  "payload": {}
}
```

All addresses and hashes are lowercase `0x` hex. Every USDC amount is a base-10 string in
6-decimal atomic units: `"25000000"` means 25.00 USDC. JSON numbers are never used for token
amounts because Solidity `uint256` exceeds JavaScript's safe integer range.

For the six job lifecycle events, `entityId` is the bytes32 chain job ID stored locally as the
lowercase `0x` value in `jobs.vault_job_id`. For `RateUpdated`, `entityId` is instead the
organization address shown in the mapping below.

## 3. Frozen event mapping

| Raw contract event | Normalized `kind` | `entityId` | Payload |
|---|---|---|---|
| `JobVault.JobFunded(jobId, amount)` | `JobFunded` | `jobId` | `amountAtomic` |
| `FloatPool.AdvanceIssued(jobId, org, principal, fee, rateBps)` | `AdvanceIssued` | `jobId` | `org`, `principalAtomic`, `feeAtomic`, `rateBps` |
| `JobVault.ExpenseRecorded(jobId, amount, receiptHash)` | `ExpenseRecorded` | `jobId` | `amountAtomic`, `receiptHash` |
| `JobVault.DeliverySubmitted(jobId, deliveryHash)` | `DeliverySet` | `jobId` | `deliveryHash` |
| `JobVault.JobSettled(jobId, advanceRepaid, operatorNet)` | `JobSettled` | `jobId` | `advanceRepaidAtomic`, `operatorNetAtomic` |
| `FloatPool.AdvanceWrittenOff(jobId, bondSlashed, reserveUsed, socialized)` | `AdvanceWrittenOff` | `jobId` | `bondSlashedAtomic`, `reserveUsedAtomic`, `socializedAtomic` |
| `FloatPool.RateChanged(org, newRateBps)` | `RateUpdated` | `org` | `org`, `rateBps` |

The two deliberate renames are load-bearing: raw `DeliverySubmitted` becomes `DeliverySet`, and
raw `RateChanged` becomes `RateUpdated`. Downstream code must never depend on the Solidity names.

`AuditAnchor.JobAnchored` is retained in the raw `chain_logs` table but is outside the seven-event
H1 freeze. Adding an audit-domain normalized event is additive and requires an H1 version bump.

## 4. SQLite handoff

- `chain_logs` is the immutable raw receipt and replay guard.
- `chain_events` is the seven-event H1 stream consumed by the runtime and future SSE layer.
- `chain_job_financials` and `chain_org_rates` are deterministic projections rebuilt from H1.
- `chain_cursors` records the next inclusive Arc block to scan.
- `reconciliation_alerts` records local-ledger/chain mismatches for the dashboard flag.

For the MVP ledger comparison, local `jobs.quote_usdc` is the expected funded amount and must
equal the chain projection's `funded_amount_atomic` after conversion to 6-decimal atomic units.

No consumer writes these tables. The indexer owns them as one deep module; consumers read the
stable H1 stream or projections instead of re-decoding contract logs.

## 5. Golden fixture

`daemon/internal/indexer/testdata/h1-spine-logs.json` is the cross-team fixture. It contains all
seven events deliberately shuffled; a conforming implementation must emit them in chain order,
project 25.00 USDC funded, 12.50 principal, 0.25 fee, 12.75 repaid, 12.25 operator net, and a
55% rate, and produce the same result when the fixture is replayed.
