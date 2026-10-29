# Zero8 (v0.8.0) hardfork — 3 Sep 2026, 15:00 UTC

Arc testnet upgrades to arc-node v0.8.0 today. This note records what was checked before the
fork, what the fork can and cannot touch in Snapfall, and how to tell afterwards.

Source: [`circlefin/arc-node/BREAKING_CHANGES.md`](https://github.com/circlefin/arc-node/blob/main/BREAKING_CHANGES.md).
Read 3 Sep 2026 ~08:20 UTC.

## Files here

| File | What it is |
|---|---|
| `zero8-pre.txt` | Chain fingerprint taken **before** the fork, at repo `103f0be` |
| `zero8-post.txt` | The same fingerprint taken after — produce it and diff |

```bash
export ARC_ARCHIVE_RPC=...              # optional; an archive endpoint, see §3
./scripts/hardfork-check > docs/hardfork/zero8-post.txt
diff docs/hardfork/zero8-pre.txt docs/hardfork/zero8-post.txt
```

`gasPrice` and the `taken` timestamp are expected to differ. **Everything else differing is a
finding**, because no Snapfall transaction is being submitted in between.

## 1. What v0.8.0 changes, and whether it reaches us

Six breaking changes. Five are node-operator only — `--rpc.admin` for peer mutation, mandatory
denylist enforcement, stricter `ARC_*` env validation, a required `--chain` on snapshot
download, and a pruning interval moving from 5000 to 128 blocks. **Snapfall runs no node**, so
none of them apply.

One is client-facing:

> **JSON-RPC error messages on insufficient balance.** `eth_call` and `eth_estimateGas` now
> surface revm 38's `OutOfFunds`; EOA transfers report `"gas required exceeds allowance"` where
> they previously said `"Missing or invalid parameters"`. *Action required: update JSON-RPC
> error parsers.*

**We have no such parser.** The only `strings.Contains` against an error anywhere in the daemon
is a SQLite `"UNIQUE constraint"` match in `daemon/internal/approval/lifecycle.go`. RPC errors
are wrapped and returned, never matched on, so no control flow depends on their text. The
sidecar matches no error strings at all. This change is cosmetic for us.

Two earlier changes already bind and were re-checked:

- **v0.7.2 rejects pre-EIP-155 transactions.** `daemon/internal/chain/chain.go` signs with
  `types.NewEIP155Signer(chainID)`. Compliant.
- **v0.7.2 caps JSON-RPC gas at 30,000,000.** Our largest transaction is a 1.47M-gas contract
  deployment. Two orders of magnitude of headroom.

## 2. The real exposure is gas accounting, not the itemized list

Zero8 is described as updating gas accounting and state-clearing semantics at the execution
layer. Neither appears as a numbered breaking change, because neither changes an API — but both
can move what `eth_estimateGas` returns, and our settlement path *clears storage* (an advance
closing zeroes its slots), which is exactly where refund accounting lives.

The submit path estimates per transaction and adds 20% headroom, so a moderate shift absorbs
silently. No gas limit is hardcoded anywhere; `SubmitWithGas` exists only for the deliberate
revert demo and has no production caller. Assessed as low risk, but it is the thing to watch:
**if anything fails after the fork, compare `gasUsed` in `zero8-post.txt` against the pre file
before looking anywhere else.**

## 3. Pre-existing finding: the public RPC no longer serves transactions by hash

Found while building the baseline, **before** the fork — this is not fork damage:

```
eth_getTransactionByHash    -> null
eth_getTransactionReceipt   -> null
eth_getBlockByNumber        -> served
eth_getLogs (by block)      -> served
```

Every transaction hash in `docs/addresses.md` returns `null` on
`rpc.testnet.arc.network` — the deploy transactions, the job-004 lifecycle, all three
`RateChanged` settlements. The blocks themselves are still served, so the chain is intact; the
tx-hash index is pruned.

**Why this matters beyond today.** `docs/addresses.md` names the RPC as *"the primary
verification path"* precisely because ArcScan has a documented outage on transaction-hash
lookups. Both paths are now dead for hash lookups at once, so as written, the settlement proof
cannot be verified by a reader.

The evidence itself is intact and still reachable two ways:

1. **By block, on the public RPC.** `eth_getLogs` over the settlement block returns the full
   waterfall. Verified 3 Sep: `logIndex 12` transfers 561000 to FloatPool, `logIndex 15`
   transfers 439000 to the operator — pool repaid before the operator is paid, the same
   ordering `docs/addresses.md` §4 claims. `scripts/hardfork-check` now asserts this on every
   run rather than counting logs.
2. **By hash, on an archive endpoint.** A third-party archive RPC still returns the receipt
   with `status 0x1`. Set `ARC_ARCHIVE_RPC` to use it; the script reports it when present.

Neither path is in `docs/addresses.md` yet. That doc needs its verification commands
reworked before submission — the numbers are right, the way it tells a reader to check them
is not.

## 4. State that is crossing the fork open

The pool is **not** quiescent. From `zero8-pre.txt`:

```
FloatPool.totalOutstanding   = 600000   (0.60 USDC)
FloatPool.orgOutstanding(op) = 600000
FloatPool.advanceRate(op)    = 7000     (70%)
FloatPool.acceptedJobs(op)   = 4
FloatPool.reserve            = 8240
```

An advance is live across the upgrade. Note this is ahead of what `docs/addresses.md` records
(rate 6500, three accepted jobs, reserve 5640): a fourth job settled after that page was last
written on 8 Aug, and a fifth advance is open now.

The invariant to check after the fork is conservation, and the pre-file has every term of it.

## 5. Verified green before the fork, at `103f0be`

| Layer | Result |
|---|---|
| `forge build` | clean |
| `forge test` | 119 passed, 0 failed |
| `go build ./...` | clean |
| `go test ./...` | exit 0, 33 packages ok |
| `sidecar: tsc --noEmit` | clean |
| `sidecar: h3-vectors` | 4 assertions pass |
| `sidecar: post-sign` | 5 assertions pass |
| `sidecar: seller-hostile` | 13 assertions pass |
| `sidecar: facilitator` | 18 assertions pass |
| `sidecar: facilitator-wiring` | 7 assertions pass |

All three layers are green on the pre-fork network. Anything red afterwards is the fork,
not accumulated drift — which is the whole reason to have run them today.

## 6. After 15:00 UTC

1. `./scripts/hardfork-check > docs/hardfork/zero8-post.txt`, then diff against the pre file.
2. Confirm `waterfall: pool before operator = YES` still holds.
3. Confirm the FloatPool constants and the one-shot wiring are unchanged.
4. Only then submit anything: run `./scripts/testnet-ops` for wallet balances, and a scaled
   `./scripts/spine_run` for a live end-to-end.
5. Record what broke here. If nothing broke, record that too — a clean diff is the result.
