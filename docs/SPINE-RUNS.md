# Daily spine runs

`docs/WORK-SPLIT.md` §6: **"Wed 29 Jul onward: daily spine run, full DD loop on testnet,
pass/fail logged in repo. Red spine outranks all feature work for all three."**

`scripts/spine_run` is that command, and `docs/spine-runs/spine-runs.tsv` is that log.

The reason it exists is not tidiness. Six of Vasanth's twelve tasks have a done-when clause
that no unit test can close, because the clause names a live run:

| Task | Done when (WORK-SPLIT §2) | Closed by |
|---|---|---|
| V1 | "a real four-cent purchase completes on testnet **and the raw request/response pair is committed as a fixture**" | **NOT closed by any beat yet.** Beat 3 proves the x402 handshake, the EIP-3009 signature and the resource delivery are real, and stops at UNVERIFIED because `seller.ts:217` returns `settlement: NOT_BROADCAST`, so nothing reaches Arc and `SETTLED` is unreachable (H3 section 8). Both halves of the clause need the facilitator broadcast plus a funded wallet: see `docs/V3-CIRCLE-SETUP.md`. |
| V7 | "a full spine run renders every state transition live" | a human watching during a green run |
| V9 | "clicking Accept produces the on-chain settlement tx" | beat 6, from JobVault status on chain |
| V10 | "a live spine run animates correctly with zero manual triggers" | a human watching during a green run |
| V11 | "the ring updates from the real RateChanged event" | beat 7 reads the rate; a human sees the ring |
| V12 | "reset to full spine, twice in a row, no manual fixes" | two consecutive runs with `reset_demo` between |

Merged code is not proven code. This harness is what converts one into the other, and the log
is the only artifact that can say "green on 29, 30, 31 Jul" on submission day.

---

## Run it

The standing services are the operator's, per `docs/RUNBOOK.md`. `spine_run` deliberately does
**not** start them: a runner that starts everything hides which piece was down.

```bash
# terminal 1: the paid seller API (V2)
cd sidecar && npm run seller

# terminal 2: the payment sidecar (V4, H3)
cd sidecar && npm run service

# terminal 3: the indexer, started EARLY so catch-up finishes before the take (RUNBOOK)
cd daemon && go run ./cmd/indexer --deployment ../deployments/arc-testnet.json --db snapfall.db

# terminal 4: the dashboard, so V7/V10/V11 have a witness
cd dashboard && npm run dev

# terminal 5: the run itself
./scripts/spine_run --dry-run     # preflight and plan; touches nothing
./scripts/spine_run               # the real thing, one log line
```

`--dry-run` exits 0 only when every prerequisite is present, so it doubles as the morning
readiness check. Other flags: `--no-seed` (attach to the job already in `.demo/current.json`),
`--skip-wallet-check`, `--job-label NAME`, `--log PATH`, `--timeout SECONDS`.

### Running without waiting on the faucet

At the PRD price the run needs a deep pool, and that depth is the whole faucet dependency.
`FloatPool` caps one org's outstanding at 10% of TVL (`FloatPool.sol:220`,
`ORG_EXPOSURE_CAP_BPS`), so a 25.00 job at a 50% rate draws a 12.50 advance and needs
**125.00 USDC** of pool before it will clear. The Circle faucet pays 20 USDC per claim on a
~2 hour cooldown behind a reCAPTCHA (`docs/RUNBOOK.md`), so filling that gap from a near-empty
pool is upwards of twelve hours of human hands.

`--price` moves that floor, because `seed_demo` deposits `max(0, target - current)` and the
floor is ten times the advance:

```bash
./scripts/spine_run --scaled          # == --price 1.00 --pool-seed 0
```

Measured against the live pool on 1 Aug 2026, holding 20.0168 USDC:

| | cap floor | deposit `seed_demo` would submit |
|---|---|---|
| `--price 25.00` (PRD) | 125.00 USDC | **104.9832 USDC** — roughly six faucet claims |
| `--price 1.00` | 5.00 USDC | **none — the pool is already deep enough** |

Every beat still runs and is still read back from chain. Nothing is faked or skipped: the
escrow, the advance, the two purchases, the escalation, the settlement and the rate change all
happen. What shrinks is the **funding** -- the job price, the advance drawn against it, and the
pool depth that advance requires. The three x402 purchase amounts are unchanged, for the reason
in the next section.

**What does NOT scale, and why the harness enforces it.** The three x402 spends stay at 0.04,
4.00 and 0.06. They are graded against `policy.DemoPolicy()`, whose thresholds are absolute
rather than proportional (`daemon/internal/policy/demo.go`): `ApprovalAboveMicros` 0.10 is
exactly what makes the two small spends auto-approve and the bait escalate. Divide the bait by
the same factor as the price and it drops under 0.10, beat 4 auto-approves, and the run reports
PASS for a spine that never exercised the rejection path at all. Push it the other way, past
the 5.00 per-tx limit, and it is **denied** by an earlier rule instead of escalated
(`policy.go:17`, "a deniable intent is denied, never escalated") so the owner is never asked.
Both failures are silent.

So the preflight asserts the relationships instead of trusting them, and refuses before beat 1
with the beat named:

```
MISSING  beat 4's bait is 0.05, at or under the 0.10 approval threshold: it would
         AUTO-APPROVE and there would be no escalation to reject
```

`spine_run` mirrors three of those thresholds as shell variables, since bash cannot import the
Go package. `daemon/internal/policy/spine_figures_test.go` parses the script and fails when the
mirror drifts, and drives the real `Evaluate` at each of the three shipped amounts to confirm
they still land on AUTO_APPROVE / HUMAN_APPROVAL_REQUIRED / AUTO_APPROVE.

**A reduced run is not evidence for a done-when clause that names PRD figures.** It proves the
machinery; it does not prove the 25.00 story. The `scale` column in the log is there so the two
can never be confused, and the submission-day claim should rest on full-scale runs.

### What the preflight demands, and why

It refuses **before** beat 1 and names every gap. A run that dies at beat 6 on an unset key has
burned a testnet cycle, a faucet cooldown and twenty minutes.

| Variable | Why the run cannot start without it |
|---|---|
| `TREASURY_PRIVATE_KEY` | the operator signs `createJob` and `requestAdvance` |
| `SNAPFALL_CUSTOMER_PRIVATE_KEY` | only the designated customer may fund its own escrow (SC-JV-001), and it signs `acceptDelivery` |
| `SNAPFALL_LP_PRIVATE_KEY` | pool liquidity; **must not** equal the operator, or the 0.00 start is a lie |
| `SNAPFALL_TREASURY_ADDRESS`, `SNAPFALL_CUSTOMER_ADDRESS` | the deployment config's `fundedWallets` |
| `SNAPFALL_OWNER_TOKEN` | H2 bearer; this runner performs the owner's two clicks |
| `SIDECAR_AUTH_TOKEN` (32+ bytes) | below 32 the daemon wires **no** payment lane, so beats 3 and 5 could only ever be UNVERIFIED |
| `H2_APPROVAL_SECRET` | without it no `approvalToken` can be signed and every `pay` is 401 |

Optional: `ARC_TESTNET_RPC` (a private endpoint, per the RUNBOOK's 429 notes),
`SIDECAR_BASE_URL`, `SNAPFALL_SELLER_URL`, `SNAPFALL_DASHBOARD_URL`, `SNAPFALL_API_ADDR`,
`SIDECAR_STORE_PATH`, `SPINE_RUN_LOG`, `SPINE_RUN_TASK_TIMEOUT`.

It also requires the deployment config to exist and to say chain **5042002**, the sidecar to
answer `/health`, the seller to answer **402** on a paid route, and port 4010 to be **free**
(the run starts its own daemon, and a stray one would silently receive the owner's clicks). A
dashboard that is not answering is a note, not a refusal: it does not make the money wrong.

---

## The seven beats, and what each one proves

Each beat prints `PASS`, `UNVERIFIED`, or `FAIL`. The distinction is the whole point.

**1 FUND** (PRD §5.1 steps 1 and 2). Runs `scripts/seed_demo`: tops the pool to the
exposure-cap floor derived from the **live** rate and existing exposure, mints a fresh job id,
funds the escrow from the customer wallet, then re-reads the chain to confirm `Funded`, no open
advance, and TVL above the floor. The 12.50 advance needs TVL >= 125 or `requestAdvance` reverts
`CapExceeded` at 0:30 on camera, which is why the depth is computed and not assumed.

**2 SNAP** (step 3). `POST /api/v1/jobs/<job>/advance`, read the `intentHash` the owner was
shown out of the approvals inbox, `POST` an `approve` decision bound to that hash, then read
`FloatPool.openAdvanceOf` back with `chainops`. PASS requires `open=true` **and**
`principal == the seed's rate-derived figure`. A different principal is a `FAIL`, not a
rounding note.

**3 SPEND** (steps 6 and 7). The DD worker discovers its source by description (G10), policy
auto-approves 0.04, Funding pays through the H3 sidecar. Verified from the sidecar's **durable
store**, matching a `DELIVERED`/`SETTLED` record for exactly `40000` micros. The worker's own
report is not evidence: it reports success for `approved-pending-integration` too, which moves
no money.

**4 REJECT** (step 9, first half). Waits for the pending 4.00 escalation, prints its resource
and expiry, then answers `request_alternative` with a reason that mentions cost.
`internal/worker/worker.go` adapts only on code `owner-alternative_requested` **and** a reason
that reads as cost (`interpretReason`), so the wording is load-bearing. A plain `reject` is also
valid product behaviour, but it ends the branch with no cheaper buy and no beat 5.

**5 ALTERNATIVE** (step 9, second half). The worker re-queries discovery under a price cap and
buys the 0.06 benchmark; verified from the store again, at `60000` micros.

Between 5 and 6 the runner waits for the DD task's terminal stage and prints it. `delivery_ready`
is the **QA gate** (step 10): the QA worker passed the draft. Anything else is printed as-is, and
beat 6 will then refuse, which is correct.

**6 SETTLE** (steps 11 and 12). Mints the per-job customer credential (owner-gated, shown once),
`POST`s the customer Accept with it, then reads chain twice: JobVault status must be `Accepted`
and `openAdvanceOf` must read `open=false`. Both together are the fall: the waterfall repaid
12.50 principal plus the 0.25 fee to the pool before any operator transfer.

**7 RATE** (step 13). Re-reads the org's live advance rate keylessly
(`seed_demo --dry-run --org <treasury>`) and requires it to have **risen** from the rate the
seed recorded. Prints what the next job unlocks, computed in integer micros.

### Why the daemon starts twice

The one-shot exits when its DD task reaches a terminal stage, taking the H2 API with it. The
customer's Accept is a separate owner session, so the runner starts the same binary again in
serve mode over the same db and lets Brain replay the job from the event log. That restart is
not a workaround, it is a real exercise of the replay path G11 promises.

### PASS, UNVERIFIED, FAIL

- **PASS**: the effect was read back from something other than the request that caused it.
- **UNVERIFIED**: the beat ran and could not be confirmed. Every honest stop in this codebase
  lands here: `advance.pending_chain`, `settlement.pending_chain`,
  `purchase.pending_settlement`, `approved-pending-integration`. The runner greps the daemon log
  and prints the reason next to the verdict.
- **FAIL**: a hard error. A refused proposal, a wrong principal, a non-200 on a decision, a task
  that never reached a terminal stage, or an unexpected abort anywhere in the script.

The run's verdict is FAIL if any beat failed, else UNVERIFIED if any beat was unverified, else
PASS. **UNVERIFIED is not a pass.** V-tasks stay open on it.

### What is never asserted here

V7, V10, V11 and A9 are screen claims. The runner prints a checklist at the end and stops; it
will not invent a PASS for pixels it cannot see. Watch the dashboard during the run and note
what you saw in the log line by hand.

---

## The log

`docs/spine-runs/spine-runs.tsv`, one tab-separated line appended per run, header written once:

The shape of a line, with **invented** values. No spine run has been logged yet, so every row
below is illustrative; the real file is still empty and nothing here should be read as a record
of a run that happened.

```
# started_utc	git_sha	verdict	scale	first_bad_beat	vault_job_id	note
<utc>	<sha>	PASS	full	-	<job id>	all beats observed
<utc>	<sha>	UNVERIFIED	full	3-SPEND	<job id>	the purchase stopped at purchase.pending_settlement
<utc>	<sha>	PASS	price=1.00	-	<job id>	all beats observed
```

`scale` is `full` when the run's job price was the PRD 25.00, and `price=<amount>` otherwise. It
is derived from the price the seed actually used -- or, under `--no-seed`, from the attached
job -- rather than from which flags were typed, so `--price 25.00` records `full` and a run that
merely asked for a smaller price but never got one cannot record a reduced label it did not earn.

It is a column rather than a note because a reader scanning verdicts must not have to parse prose
to tell a 25.00 run from a 1.00 one: both are honest PASSes of every beat, but only the first is
evidence for a done-when clause that names the PRD amounts.

A log written before this column existed has six fields. `spine_run` detects that header and
**refuses to append** rather than shifting every column after the verdict; it prints the verdict
and tells you to add `full` to the old rows first.

`git_sha` carries a `+dirty` suffix when the worktree was not clean, because a run only proves
the code that ran. The beat blamed is the **first** non-PASS: that is the one to debug in the
morning. The note is flattened to one line so the file stays greppable.

A **preflight refusal writes nothing**. A missing key is an operator error, not a red spine, and
padding the record with non-runs would destroy the only thing the file is for. Everything from
beat 1 onward is logged, including an abort: the exit trap writes a FAIL line rather than
letting a dead run leave no trace.

Per-run artifacts (both daemon logs, the built binaries, every chain read, the curl log) land in
`.demo/spine-runs/<run id>/`, which is gitignored. That directory is the first thing to open when
a run goes red; the log line only tells you where to look.

---

## When it goes red

The rule first: **a red spine outranks all feature work for all three of us**, and per §6 of
WORK-SPLIT, red two days running is a pre-agreed swarm trigger. No debate needed.

Order of work: re-read the beat that failed, open `.demo/spine-runs/<run id>/daemon-a.log`,
then fix forward. Do not "fix" a red spine by loosening the check.

| Symptom | Likely cause and move |
|---|---|
| Beat 1 fails on `CapExceeded` | pool below the derived floor. `seed_demo` computes it; if the rate rose to 55% the next run needs 137.50, and prior outstanding exposure for the same org doubles the floor. Deposit and rerun. |
| Beat 1 fails funding the escrow | USDC is the gas token, so a wallet that both pays gas and holds a balance is short by what it spent (RUNBOOK, job-002). Fund amount **plus** headroom, never the exact figure. |
| Beat 2 UNVERIFIED, `advance.pending_chain` | no treasury chain lane: the key is missing or malformed. The daemon logged the reason at startup. |
| Beat 2 FAIL on principal mismatch | the rate moved between seed and snap, or a stale `.demo/current.json`. Reseed rather than reasoning about it. |
| Beat 3 or 5 UNVERIFIED, `purchase.pending_settlement` | no H3 lane: token under 32 bytes, missing `H2_APPROVAL_SECRET`, or the sidecar did not answer `/health` at daemon startup. The preflight catches all three; if it passed, restart the sidecar and rerun. |
| Beat 4 never sees the escalation | the worker never got that far. Check whether beat 3 abandoned its source; discovery returning nothing is a first-class honest outcome and is logged as `source-abandoned`. |
| Beat 5 UNVERIFIED with `source-abandoned` | discovery found nothing under the rejected price, or the decision reason did not read as cost. The reason text is a real input, not a comment. |
| Task never reaches a terminal stage | a stale `memory/` dir beside the db still holds the job: "job already exists", the supervisor retries five times and gives up (RUNBOOK). Reset: `rm -rf snapfall.db snapfall.db-wal snapfall.db-shm memory`, or just `./scripts/reset_demo`. |
| Beat 6 cannot mint the accept link | the job is not `delivery_ready`. Read the QA gate line printed just above beat 6. |
| Beat 6 UNVERIFIED, `settlement.pending_chain` | no customer chain lane; same diagnosis as beat 2 with the customer key. |
| Beat 7 says the rate did not tick | settlement did not complete, or the ring is reading a stale value. Beat 6's verdict tells you which. |
| Intermittent `429` from the RPC near head | expected (RUNBOOK): head-chasing, not catch-up. Start the indexer early, raise the poll interval, or point `ARC_TESTNET_RPC` at a private endpoint. |

### The V12 double-run gate

V12's clause is two clean runs back to back with no manual fixes:

```bash
./scripts/reset_demo && ./scripts/spine_run
./scripts/reset_demo && ./scripts/spine_run
```

`reset_demo` clears local state only. Arc has no delete, which is exactly why `seed_demo` mints
a fresh job id per run: `createJob` reverts `JobExists` on a reused id. Pool liquidity persists
on purpose, so the second run usually needs no deposit. Two PASS lines in the log, one after the
other, same day, is the gate.
