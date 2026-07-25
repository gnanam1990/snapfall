# V6 follow-ups

**Written 25 Jul 2026, the day V6 merged (`559875a`, PR #46).** V6 filled the F2 seam: an
approved purchase now runs the real policy engine, the real escalation, the real owner
decision, the real expiry clock, the real freeze gate, holds real budget, and then actually
pays through the H3 sidecar. Several things it deliberately did not build were recorded only
in the PR body and in `5999f16`'s commit message. This file is where they live now, so the
next person, or the same person on 6 Aug, can act on them without archaeology.

Every claim below was established from the code and carries a `file:line`. Nothing here is
taken on the word of a commit message; where a shipped code comment turned out to be stale,
that is recorded as a finding of its own.

**How severity is used.** Most of these do not block the 8 Aug demo. Saying so is more
useful than inflating them: the scarce resource between now and the deadline is attention,
and an item marked "blocks the demo" that does not is a theft of it. "Camera risk" means the
spine still runs but something on screen contradicts the narration.

## Register

| ID | Item | Blocks 8 Aug demo | Camera risk | Severity | Owner stream |
| --- | --- | --- | --- | --- | --- |
| [V6-F1](#v6-f1-recordexpense-is-unwired) | `recordExpense` is unwired: no daemon purchase ever reaches the chain | No | **Yes**, the job page's budget bar | Medium | Vasanth (V6/V7 lanes), with a Gnanam and Anandan dependency |
| [V6-F2](#v6-f2-paymentexecuted-carries-no-amount-and-no-receipthash) | `payment.executed` carries no amount and no receiptHash, so the money graph reconciles by amount | No | Low | Low | Gnanam **and** Vasanth (spans two owners) |
| [V6-F3](#v6-f3-per-job-chain-budget-versus-per-org-daemon-budget) | Per-job chain budget versus per-org daemon budget, made loud but not resolved | No | No, at default seed values | Low | Gnanam (policy engine) |
| [V6-F4](#v6-f4-docsopen-spec-questionsmd-does-not-exist) | `docs/OPEN-SPEC-QUESTIONS.md` is cited six times and does not exist | No | No | Low for the demo, **medium for the submission package** | Vasanth (docs), ruling content is Gnanam's |
| [V6-F5](#v6-f5-policy_version-is-an-integer-column-holding-text) | `policy_version INTEGER` holds the string `"pol_7"`, stored as TEXT | No | No | Low, latent | Gnanam (schema) |
| [V6-F6](#v6-f6-manifestbudgetmicros-is-a-false-guarantee) | `Manifest.BudgetMicros` is parsed and validated but never enforced, while a shipped manifest says it is | No | No | Medium as an integrity claim | Gnanam (policy engine) or Vasanth (the comment) |

Ownership is assigned by the `docs/WORK-SPLIT.md` streams (Vasanth: Money Rails and Product
Face, V1 to V12; Gnanam: Brain runtime, policy, approval, contracts, G1 to G14; Anandan:
chain layer and integration, A1 to A15), not by git authorship, which on this machine is not
a signal.

---

## V6-F1 recordExpense is unwired

### What is true today

`chain.CalldataRecordExpense` is defined at `daemon/internal/chain/bindings.go:103` and has
**exactly one caller in the repository**: the manual operator CLI at
`daemon/cmd/chainops/main.go:153`, reachable only by typing

```
chainops -key-env TREASURY_PRIVATE_KEY record-expense <jobid32> <usdc> <receipt32>
```

(usage line, `daemon/cmd/chainops/main.go:42`). Nothing in the daemon calls it. `grep` over
`scripts/`, `docs/`, `dashboard/` and `README.md` finds no other reference, so it is not even
a documented step in `docs/RUNBOOK.md`.

The signing capability exists but the lane does not. `daemon/cmd/snapfall/main.go:864` wires
exactly two submitters, `fund.SetChain(advanceLane, settleLane)`, whose parameter names are
`treasuryAdvance, customerSettle` (`daemon/internal/funding/funding.go:116`). The advance lane
is `lane{treasury, fpAddr}` and the settle lane is `lane{customer, jvAddr}`
(`daemon/cmd/snapfall/main.go:851,858`); a `lane` binds one client to one contract address
(`daemon/cmd/snapfall/main.go:139-150`). `recordExpense` needs the **treasury** key against
the **JobVault** address, which is a third combination that is never constructed. The
treasury signer is the operator (`daemon/cmd/snapfall/main.go:852`) and
`JobVault.recordExpense` is operator-only (`contracts/src/JobVault.sol:161`), so the
authority is there; only the wiring is missing.

The daemon already produces both arguments a call would need. A completed purchase yields
`out.PaidMicros` (the sidecar's `amountPaid`, never the daemon's expectation,
`daemon/internal/purchasing/purchasing.go:576-577`) and `out.ReceiptHash`, which is the H3
`AuthNonce` because H3's receipt has no receiptHash of its own
(`daemon/internal/funding/funding.go:377-378`, `daemon/internal/h3/h3.go:389-395`). Both are
already written durably into `purchase.delivered`
(`daemon/internal/purchasing/purchasing.go:597-605`, keys `amount_micros` and `receipt_hash`).
`AuthNonce` returns `0x` plus 64 hex characters (`daemon/internal/h3/h3.go:367-371`), which is
exactly what `chain.JobID32` accepts, so it is already a valid `bytes32` argument.

### The blocker: is there a producer for `brain.JobMemory.VaultJobID`?

The answer is more specific than "no", and the shipped comments about it are now stale.

There are **two** producers, and neither is an on-chain `createJob`:

1. `Brain.BindVaultJob` (`daemon/internal/brain/advance.go:105-137`) validates the id as
   `0x`-prefixed `bytes32` hex, adopts the chain-authoritative quote when a quote oracle is
   wired, and writes `jm.VaultJobID`. It has exactly one production call site,
   `daemon/cmd/snapfall/main.go:439`, and it fires only when the operator passes
   `--owner-vault` (`daemon/cmd/snapfall/main.go:265`). So on the one-shot owner path the id
   exists only if a human pastes it in from `seed_demo`'s state file
   (`.demo/current.json`, written by `daemon/cmd/seed-demo/main.go:287`). The comment at
   `daemon/cmd/snapfall/main.go:437-438` says exactly this and calls the automation open.
2. `Brain.OpenMilestone` sets `jm.VaultJobID = cycle.VaultJobID`
   (`daemon/internal/brain/milestone.go:130`), where that value is
   `"0x" + sha256("snapfall:vault-milestone:v1\x00" + instruction + "\x00" + number)`
   (`daemon/internal/brain/milestone.go:156-163`). It is a **locally derived** identity. The
   only two `createJob` call sites in the repo, `daemon/cmd/chainops/main.go:116` and
   `daemon/cmd/seed-demo/main.go:256`, mint their ids independently, so for a milestone job
   the vault id points at a JobVault job that does not exist.

`jobs.vault_job_id` is also written now, whenever memory has an id
(`daemon/internal/brain/project.go:58-69`).

That makes three shipped comments stale, and they are the comments a reader would trust:
`daemon/internal/billing/billing.go:20-21` ("Nothing writes jobs.vault_job_id, so
Request.VaultJobID has no producer for real jobs"), `daemon/internal/brain/billing.go:102`
("nothing writes jobs' VaultJobID"), and `daemon/internal/brain/advance.go:55` ("nothing
writes vault ids"). The accurate statement is: **no producer agrees with the chain by
construction.** One is operator-typed, the other is synthetic.

### What breaks or misleads

Three things stay at zero, and one of them is on camera.

1. **On chain.** `j.onchainExpenses` is written only by `recordExpense`
   (`contracts/src/JobVault.sol:168`), so it stays 0 for every job, forever.
2. **In the projection.** `chain_job_financials.expense_total_atomic` keeps its schema
   default `'0'` (`daemon/store/schema.sql:97`) because the only branch that updates it is
   the `ExpenseRecorded` case (`daemon/internal/indexer/project.go:45-55`), and no
   `ExpenseRecorded` log is ever emitted.
3. **In the dashboard.** `dashboard/lib/jobChain.ts:184` reads the expenses word from the
   single `jobs` eth_call, so `onchainExpensesUsdc` is `"0"`, `budgetRemainingUsdc` is the
   whole budget, and `budgetUsedBps` is 0 (`dashboard/lib/jobChain.ts:224-225,237-240`).

**The budget bar therefore renders, for a job that exists on chain and whose workforce has
genuinely spent money: `0.00` spent, the full budget remaining, and a fill of width 0%**
(`dashboard/app/jobs/[jobId]/page.tsx:266-281`, width is
`Math.min(100, (job.budgetUsedBps ?? 0) / 100)`). Directly beneath it sits the caption
"Expenses recorded on chain against the budget bound (SC-JV-003)"
(`dashboard/app/jobs/[jobId]/page.tsx:283-286`), which is a true statement about a thing that
never happens. Note that the bar does **not** show the honest "not known" dash: a job absent
from chain returns nulls and renders an em-dash placeholder
(`dashboard/lib/jobChain.ts:160-178`), but a job that *is* on chain returns a real `0`, which
formats as `0.00`. The UI asserts zero rather than admitting ignorance, which is the one thing
this codebase otherwise refuses to do.

**Billing classifies every daemon purchase as `pending-settlement`, permanently.** In
`Agent.reconcile`, `onChain` is built only from `ExpenseRecorded` lines
(`daemon/internal/billing/billing.go:341-345`), so `onChain[p.ReceiptHash]` is never true and
every provenance entry falls through to
`OutcomePendingSettlement` (`daemon/internal/billing/billing.go:354`). `gapsFor` then adds the
expenses gap, "purchases approved but not yet settled on chain"
(`daemon/internal/billing/billing.go:401-404`), and any gap forces
`StatusPartial`, rendered as "partial, awaiting chain records"
(`daemon/internal/billing/billing.go:54,220-223`). Consequences worth stating plainly:

- No invoice this daemon can produce is ever `complete`. G12's done-when, "invoice totals
  reconcile to chain to the cent on a real spine run" (`docs/WORK-SPLIT.md:63`), cannot be met
  while this is open, whatever else works.
- `Totals.ExpenseTotalAtomic` stays the empty string, because `sawExpense` never goes true
  (`daemon/internal/billing/billing.go:270-291`). The cross-check does not raise a false alarm
  for this, because `""` versus the projection's `"0"` is explicitly treated as agreement
  (`daemon/internal/billing/billing.go:315-317`). That special case is correct and should stay.
- The loud path is unreachable. `AlertExpenseOutsidePolicy`, the alarm for an on-chain expense
  no daemon record can vouch for (`daemon/internal/billing/billing.go:366-372`), iterates over
  `ExpenseRecorded` lines and so has never fired against real data. The most safety-critical
  branch in the billing agent is the least exercised one.

### The fix

**Full fix**, roughly a half day of wiring plus a test:

1. Construct a third lane, treasury key against the JobVault address, in the
   `if deployment != ""` block (`daemon/cmd/snapfall/main.go:838-897`), and hand it to the
   Purchaser or to Funding as an expense submitter. Keep the existing posture: a configured
   deployment with a missing key wires no lane and the flow stops at a labelled
   `*.pending_chain` record rather than pretending
   (`daemon/cmd/snapfall/main.go:832-837`).
2. Submit after `Purchaser.settle` records `purchase.delivered`, using
   `out.ReceiptHash` and `out.PaidMicros` from the same outcome
   (`daemon/internal/purchasing/purchasing.go:597-605`). Never derive the amount from the
   intent: the sidecar's `amountPaid` is the authority, which V6 already holds to
   (`daemon/internal/purchasing/purchasing.go:576`).
3. Respect the state machine. `recordExpense` reverts `InvalidStatus` unless the job is
   `InProgress` (`contracts/src/JobVault.sol:162`), and only `startWork` sets that state
   (`contracts/src/JobVault.sol:141-149`), which is itself another operator-only call with no
   daemon caller (`daemon/cmd/chainops/main.go:138` is the sole one). `submitDelivery` moves
   the job to `Delivered` (`contracts/src/JobVault.sol:184`), after which the expense can
   never be recorded. So the write has a window: after the advance and before delivery. A
   revert must land as a pending record, not a silent failure.
4. Give the milestone path a vault id that exists on chain. Either call `createJob` with
   `milestoneIdentity`'s derived id, or have the milestone flow adopt the id from an actual
   `createJob` receipt. This is the piece that is not Vasanth's alone.

**Manual workaround for 6 Aug, if the full fix is not affordable.** Every input already
exists, so the expense can be recorded by hand between the spend beat and delivery:

1. Take the vault job id from `.demo/current.json`.
2. Take `receipt_hash` and `amount_micros` from the `purchase.delivered` event in the store.
3. Convert micros to the human decimal `chainops` expects: `usdcMicros` parses `"0.04"`, not
   `40000` (`daemon/cmd/chainops/main.go:222-241`). This conversion is exactly the kind of
   step that goes wrong live, so do it before the take, not during it.
4. Run `chainops record-expense <vaultJobId> <usdc> <receipt_hash>` while the job is
   `InProgress`.

The budget bar then renders real numbers, and Billing's `matched` outcome fires for the first
time. If even that is not affordable, the honest alternative is to not put the Operating
budget card on camera, and to say in the narration that on-chain expense recording is wired to
the CLI and not yet to the daemon.

**Also fix the stale comments** listed above, whatever else happens. They are load-bearing for
the next reader.

### Owner and demo impact

Owner: **Vasanth** for the lane and the submit call, since it belongs beside the V6 chain lanes
he already wires and feeds the V7 page he owns. **Gnanam** for `SPEC-02` (below) if the
releasing reading of `recordExpense` is adopted, because that changes the contract. **Anandan**
for `createJob` inside the milestone loop (A11, `docs/WORK-SPLIT.md:90`), because the
milestone-cycle orchestration is his.

Does not block the demo: fund, snap, spend, deliver and fall all run without it. It is a
**camera risk** and a **G12 done-when blocker**.

**Adjacent open ruling: SPEC-02.** `contracts/src/JobVault.sol:152-157` states that
`recordExpense` is "ACCOUNTING ONLY: it moves no USDC", and then that "SC-JV-003's wording is
'records or releases'; the releasing reading is unresolved, see docs/OPEN-SPEC-QUESTIONS.md
SPEC-02." Whoever wires this must know which reading is law before they wire it: under the
recording reading the escrow stays whole for the waterfall and PRD 15.2's "operator receives
12.25" arithmetic holds; under the releasing reading it does not, and
`contracts/test/JobVault.t.sol:264` says exactly that. See [V6-F4](#v6-f4-docsopen-spec-questionsmd-does-not-exist):
the venue named for that resolution does not exist.

---

## V6-F2 payment.executed carries no amount and no receiptHash

### What is true today

The payload is two keys:

```go
if err := l.appendEvent(ctx, req.JobID, "payment.executed", map[string]any{
        "request_id": req.ID, "intent_hash": req.IntentHash,
}); err != nil {
```

(`daemon/internal/approval/lifecycle.go:720-722`). No amount, no receipt hash. The chain side
does ship one: H1 maps `JobVault.ExpenseRecorded(jobId, amount, receiptHash)` to payload keys
`amountAtomic, receiptHash` (`docs/handshakes/H1-indexer-events.md:54`), the decoder emits
both (`daemon/internal/indexer/decode.go:80`), and Billing lifts `receiptHash` onto the line as
its join key (`daemon/internal/billing/billing.go:259-261`).

So the two vocabularies that describe one purchase share no identifier, and the money graph
reconciles them by **amount**. `spendSource` decides which side reported a spend purely from
the kind, `ExpenseRecorded` being the chain's and everything else the daemon's
(`dashboard/components/MoneyGraph.tsx:131-133`), and the tally is keyed on the amount string
(`dashboard/components/MoneyGraph.tsx:277-285`). An event counts only if it raises its own
source's tally above the other's, so the true number of purchases at a given amount is the
larger of the two claims. The rule is documented in place at
`dashboard/components/MoneyGraph.tsx:178-204`, including why: "A shared purchase id would make
this trivial, and there is none".

Two further facts about the production path, both verified:

- In production the daemon's `payment.executed` is dropped entirely, not merely counted
  imprecisely. An amountless spend returns early
  (`dashboard/components/MoneyGraph.tsx:250`), and the comment there is correct about why. The
  daemon-side spend total therefore rides on `approval.requested` with state approved
  (`dashboard/components/MoneyGraph.tsx:104-105`), whose amount is read from the intent
  (`dashboard/lib/activity.ts:161`, `AmountMicros`). That is the **requested** figure, not the
  sidecar's `amountPaid`. The paid figure exists, in `purchase.delivered`
  (`daemon/internal/purchasing/purchasing.go:602`), and never reaches the browser.
- The local demo fixture is more capable than production, which is how this stays invisible:
  the mock SSE route synthesizes `payment.executed` **with** an `amountUsdc`
  (`dashboard/app/api/events/stream/route.ts:54-58`), while the file's own header calls itself
  the mock behind the real H2 shape (`dashboard/app/api/events/stream/route.ts:1-12`). Anyone
  testing the graph against the fixture sees a code path production never takes.

`purchase.delivered` is not on the H2 stream at all. The daemon kinds H2 enumerates are
`policy.evaluated`, `approval.requested`, the three approval decisions, `approval.expired`,
`payment.executing|executed|failed`, `purchase.pending_settlement`, `task.withheld`, the freeze
pair and `brain.msg.*` (`docs/handshakes/H2-owner-api.md:42`). The three V6 additions
(`purchase.delivered`, `purchase.unresolved`, `purchase.payee_divergence`,
`daemon/internal/purchasing/purchasing.go:65-70`) are absent from that list and have no case in
`dashboard/lib/activity.ts`.

### The residual imprecision this leaves

The amount tally is symmetric and handles the ordinary cases correctly, including two genuine
same-amount purchases (daemon reaches 2 while chain is at 1, so the second counts). What it
cannot do is distinguish two **different** purchases that happen to cost the same:

> A daemon purchase of 0.04 and an unrelated chain expense of 0.04 inside one cycle are
> treated as one purchase. The daemon event counts (d=1 > c=0), the chain event does not
> (c=1 is not > d=1), and the graph shows 0.04 where 0.08 moved.

It errs toward not inventing money, which is the right direction, and it is stated honestly in
the source (`dashboard/components/MoneyGraph.tsx:201-203`). But the demo script deliberately
contains two purchases: 0.04 and 0.06 (`docs/WORK-SPLIT.md:25`, the paid demo API's three
endpoints). If a rehearsal ever produces two spends at the same figure, or if a manual
`chainops record-expense` echoes a purchase the daemon also reported, the total silently
under-reports. The tally also resets only on `fund` or an explicit `reset`
(`dashboard/components/MoneyGraph.tsx:264,330`), so "one cycle" is the whole window between
funding events.

### The fix

The contract change is small and additive: **`payment.executed` gains `amount_micros` and
`receipt_hash`** at `daemon/internal/approval/lifecycle.go:720-722`, and H2's kind table gains
those keys. The Executor already returns nothing to the lifecycle, so the values have to be
threaded from the purchaser's outcome rather than read from the intent, which is the whole
point: the paid figure must not be replaced by the requested one.

There is a cheaper alternative that changes no existing payload: **surface
`purchase.delivered` on the H2 stream**, since it already carries `amount_micros`,
`receipt_hash`, `payment_id`, `auth_nonce`, merchant, pay-to and state
(`daemon/internal/purchasing/purchasing.go:597-605`). That requires adding the kind to
`docs/handshakes/H2-owner-api.md:42`, adding a case in `dashboard/lib/activity.ts`, and
switching `spendSource` and the tally in `dashboard/components/MoneyGraph.tsx` to key on
`receipt_hash` when both sides have one, falling back to the amount tally when they do not.

Either way the money graph then reconciles by identity: an `ExpenseRecorded` whose
`receiptHash` matches a `purchase.delivered` already seen is an echo, and one that does not is
a genuinely separate expense. That is the same join Billing already performs
(`daemon/internal/billing/billing.go:336-355`), so the dashboard would stop inventing a second
reconciliation strategy for the same question.

Note the ordering: this only becomes observable once [V6-F1](#v6-f1-recordexpense-is-unwired)
lands, because until then no `ExpenseRecorded` ever arrives and the tally has nothing to
reconcile against.

### Owner and demo impact

**This one spans two owners and must be raised at standup rather than picked up quietly.** The
event payload and the H2 kind table are Gnanam's (G7 approval lifecycle,
`docs/WORK-SPLIT.md:56`, and H2 is Gnanam to Vasanth, `docs/WORK-SPLIT.md:14`). The stream
normalization, `activity.ts` and `MoneyGraph.tsx` are Vasanth's (V5, V10). Neither half is
useful alone, and "both sides assuming the other handled it is the exact shape of every gap
found this week" (`daemon/internal/billing/billing.go:36`).

Does not block the demo, and severity is genuinely low: the current behaviour under-reports
rather than over-reports, which is the safe failure. Fix it after V6-F1, or not at all before
8 Aug.

---

## V6-F3 Per-job chain budget versus per-org daemon budget

### What is true today

Two independent figures govern one job's spending and nothing reconciles them:

- **On chain**, per job: `maxOperatingBudget`, set as `createJob`'s `budget` argument
  (`daemon/internal/chain/bindings.go:95-100`), enforced by `recordExpense`'s
  `if (spent > j.maxOperatingBudget) revert OverBudget()`
  (`contracts/src/JobVault.sol:165-166`). The demo seed supplies it from
  `-budget`, default `"6"` USDC (`daemon/cmd/seed-demo/main.go:73`, submitted at
  `daemon/cmd/seed-demo/main.go:256-258`).
- **In the daemon**, per org: `policy.DemoPolicy().JobBudgetMicros = 6_000_000`
  (`daemon/internal/policy/demo.go:26`), enforced by policy rule 1
  (`daemon/internal/policy/policy.go:269-274`) and made live for the first time by V6's
  reservation ledger (`daemon/internal/budget/budget.go:1-4`).

V6 made the divergence loud and resolved nothing, deliberately. `chainBudget.check` reads
`jobEconomics`, whose third return value used to be discarded, compares it to the policy
figure, and on disagreement logs a named `BUDGET DIVERGENCE` warning and appends
`budget.chain_divergence` (`daemon/cmd/snapfall/main.go:187-252`,
`daemon/internal/budget/budget.go:107-110,905-930`). It runs at boot over every milestone with
a chain identity (`daemon/cmd/snapfall/main.go:950-958`) and again the moment the owner binds
one (`daemon/cmd/snapfall/main.go:443-446`), once per vault job per process
(`daemon/cmd/snapfall/main.go:172-176`). The posture is stated where it is implemented: "raise
the alert, pick no winner" (`daemon/cmd/snapfall/main.go:161-162`), matching Billing's
projection-divergence stance.

Three honest qualifications on how much the alarm can actually see:

1. **At default seed values the two figures are equal** (6.00 both sides), so the alarm stays
   silent and there is nothing to see. It fires only if someone passes a different `-budget`.
2. A zero `maxBudget` is treated as silence, not divergence, because a job not yet on chain has
   no figure to disagree with (`daemon/cmd/snapfall/main.go:216-218`). Combined with
   [V6-F1](#v6-f1-recordexpense-is-unwired)'s finding that milestone vault ids are synthetic,
   the boot sweep over milestones reads a nonexistent job, gets zero, and returns quietly. The
   alarm's main loop is, today, structurally unable to fire.
3. Every read failure is a warning and never fatal
   (`daemon/cmd/snapfall/main.go:182-186`), and a `maxBudget` that does not fit int64 refuses
   to compare rather than comparing a truncated figure
   (`daemon/cmd/snapfall/main.go:219-226`). Both are correct; both mean a missing alarm is
   possible.

### What breaks or misleads

A purchase the policy engine allows can still revert `OverBudget` on chain. Today that cannot
happen, because nothing calls `recordExpense` at all, so the chain bound is unreachable and the
divergence is purely notional. **The day V6-F1 is wired, this becomes a live failure mode**, and
it lands at the spend beat.

The direction matters. If the chain budget is *lower* than the policy budget, the policy engine
auto-approves a purchase and the expense record then reverts, so money has moved with no
on-chain trace, which is precisely the state Billing calls `outside-policy` from the other
direction. If it is *higher*, the daemon is merely stricter than the contract, which is
harmless.

### What resolving it would require

Not a wiring change. From the implementation note at `daemon/cmd/snapfall/main.go:152-157`,
verified against the code:

1. **Change `Lifecycle.Policy`'s signature.** It is `func() (policy.PolicyConfig, string)`
   (`daemon/internal/approval/lifecycle.go:186`), with no job parameter, and it is read twice
   per payment, at intake and again at execution, so a version bump between them invalidates
   (SEC-006, same line). A per-job budget means `func(jobID string) (policy.PolicyConfig, string)`
   or a separate per-job override hook. There are **18 assignment sites** for that field:
   `daemon/cmd/snapfall/main.go:789`, `daemon/cmd/approval-demo/main.go:49`,
   `daemon/cmd/freeze-demo/main.go:80`, and fifteen test sites across `advancing`, `approval`,
   `brain`, `funding`, `telegram`, `ownerapi`, `purchasing` and `integration`.
2. **Decide what an unknown job budget means**, and this is the hard half. The engine's stated
   law is that a zero limit on a deny rule means NOT CONFIGURED and denies, because "unset
   means unlimited is how a money bug ships" (`daemon/internal/policy/policy.go:97-99`,
   enforced at `daemon/internal/policy/policy.go:260-262` and pinned by case 21). Applied
   literally, a job whose chain budget cannot be read can never buy anything, which would make
   an RPC failure a total spending outage. Applied loosely, the fallback to the org default is
   exactly the silent divergence this item is about. That is a case table with its own fixture
   rows, which is why V6 did not open it at 1.5 days of budget.
3. **Give the policy engine a per-job source for the figure.** The only reader of
   `jobEconomics` in the daemon is the alarm itself (`daemon/cmd/snapfall/main.go:164-165`),
   and `jobs.operating_budget_usdc` exists in the schema
   (`daemon/store/schema.sql:19`) but is never written: Brain's projection writes only id, org,
   status, quote and vault id (`daemon/internal/brain/project.go:62-69`), and that projection
   is fenced against gaining a second write site
   (`daemon/internal/budget/budget.go:9-13`). So the figure has to come from a chain read at
   intake, with all the latency and failure-mode questions that implies, or from the deployment
   config.

A cheaper interim that is not a resolution but removes the demo hazard: assert equality at
boot and refuse to serve, or have `seed_demo` read the policy figure and default `-budget` to
it so the two cannot drift by typo. Both are small; neither answers question 2.

### Owner and demo impact

Owner: **Gnanam**, G6 policy engine (`docs/WORK-SPLIT.md:55`). The seed-side default is
Vasanth's (V12).

Does not block the 8 Aug demo at default values, and the alarm that exists is the correct
mitigation for the value it can actually protect. Revisit only if V6-F1 lands first.

---

## V6-F4 docs/OPEN-SPEC-QUESTIONS.md does not exist

### What is true today

**Verified: the file is absent from the working tree** (`ls docs/` returns `PRD.md`,
`RUNBOOK.md`, `SRS-v4-annex.md`, `USYC-SWEEP.md`, `WORK-SPLIT.md`, `handshakes`). It was
deleted on 22 Jul in `8c04728`, "docs: adopt v8.0 Formal PRD Edition as docs/PRD.md; retire
v4.1 working docs", whose message says it "Removes the superseded v4.1 working docs
docs/ABI-FREEZE-LOG.md and docs/OPEN-SPEC-QUESTIONS.md". It was 249 lines. It is recoverable
in full: `git show 8c04728^:docs/OPEN-SPEC-QUESTIONS.md`.

**Six live citations name the path.** Quoted exactly:

| Citation | Text |
| --- | --- |
| `contracts/src/JobVault.sol:157` | `see docs/OPEN-SPEC-QUESTIONS.md SPEC-02.` |
| `contracts/test/FloatPool.t.sol:213` | `That contradiction is unresolved (see docs/OPEN-SPEC-QUESTIONS.md, SPEC-01);` |
| `contracts/test/Advance.t.sol:34` | `and docs/OPEN-SPEC-QUESTIONS.md SPEC-06: a 12.50 advance needs TVL >= 125.00 to stay` |
| `daemon/internal/agents/manifest.go:45` | `here; see docs/OPEN-SPEC-QUESTIONS.md SPEC-05.` |
| `docs/SRS-v4-annex.md:523` | `(Ruling 19 Jul 2026, supersedes the prior min(maxOperatingBudget, ...) formulation ... see docs/OPEN-SPEC-QUESTIONS.md SPEC-01.)` |
| `README.md:85` | ``[`docs/OPEN-SPEC-QUESTIONS.md`](docs/OPEN-SPEC-QUESTIONS.md) - **SPEC-01 is a blocker**`` |

`SPEC-07` is named without the path, at `contracts/src/JobVault.sol:244`: "recordExpense never
moves escrow (SPEC-02) ... See SPEC-07." A further eight places cite SPEC ids with no venue at
all (`contracts/src/FloatPool.sol:174`, `contracts/src/JobVault.sol:199`,
`contracts/test/Advance.t.sol:13,103,111,367`, `contracts/test/JobVault.t.sol:264`,
`contracts/test/FloatPool.t.sol:12`). So a reader who wants to know the status of SPEC-01,
SPEC-02, SPEC-05, SPEC-06 or SPEC-07 has fourteen pointers and no document.

### What breaks or misleads

1. **README.md is actively wrong, on the most visible page in the repository.** It says
   "SPEC-01 is a blocker: the demo's 12.50 USDC advance contradicts SC-FP-002's formula given a
   6.00 max operating budget. Resolve at standup; it blocks `requestAdvance` and fails AT-11 as
   written" (`README.md:85-88`). SPEC-01 was resolved three days before the file was deleted:
   `contracts/src/FloatPool.sol:174-176` records "SPEC-01 RULING (19 Jul 2026):
   `advance = advanceRate(org) x customerPayment`. The `min(maxOperatingBudget, ...)` term is
   GONE", `docs/SRS-v4-annex.md:523` carries the same ruling into SC-FP-002, and
   `contracts/test/Advance.t.sol:111` asserts it. The README therefore tells a reader,
   including a judge, that the demo's central beat is blocked by a contradiction that was
   settled on 19 Jul, and links them to a 404 for the details.
2. **`SPEC-02` is unresolved and adjacent to [V6-F1](#v6-f1-recordexpense-is-unwired).** The
   open question is whether `recordExpense` should release escrow or only record it. The
   contract implements the recording reading and says the releasing reading is unresolved
   (`contracts/src/JobVault.sol:152-157`), the refund path depends on that choice
   ("Restitution is the FULL customerPayment. `onchainExpenses` does not reduce it because
   recordExpense never moves escrow (SPEC-02)", `contracts/src/JobVault.sol:242-244`), and a
   contract test warns that under the other reading "the waterfall's 'operator receives 12.25'
   arithmetic breaks. See SPEC-02" (`contracts/test/JobVault.t.sol:264`). **Whoever wires
   V6-F1 is the first person who will actually care**, because they are choosing what a
   recorded expense does to the money. If the releasing reading were ever adopted, V6-F1's
   submit call stops being pure accounting and becomes a transfer, which changes its failure
   semantics entirely. Record the decision before writing the call, not after.
3. `SPEC-05` (`daemon/internal/agents/manifest.go:45`) is the manifest-schema divergence: the
   flat files on disk versus PRD 8.2's nested illustrative example. That question is still
   genuinely open and is related to [V6-F6](#v6-f6-manifestbudgetmicros-is-a-false-guarantee).
4. `SPEC-06` (`contracts/test/Advance.t.sol:34`) is the pool-seed floor, which V12 resolved in
   practice by computing the depth rather than hardcoding it
   (`scripts/README.md`, the "Why the pool depth is computed" paragraph). The resolution exists
   in a script's README and nowhere in the spec register, because there is no register.

### The fix

**Restore the file rather than rewrite the citations.** Six code comments and a README link
name it by path; editing seven files to point somewhere else is more churn and more risk than
recreating one. Recover the 249-line original with
`git show 8c04728^:docs/OPEN-SPEC-QUESTIONS.md > docs/OPEN-SPEC-QUESTIONS.md`, then add a
status header per item so it reads as a resolution register and not as a live blocker list:

- SPEC-01: **RESOLVED 19 Jul 2026.** Cite `contracts/src/FloatPool.sol:174` and
  `docs/SRS-v4-annex.md:523`.
- SPEC-02: **OPEN.** Note the adjacency to V6-F1 and that the recording reading is what is
  implemented and tested today.
- SPEC-05: **OPEN.** Files on disk are authoritative for now.
- SPEC-06: **RESOLVED in practice by V12.** Cite `scripts/README.md` and the 150 USDC default.
- SPEC-07: **OPEN**, and name where it is referenced from, since it has no path citation.

Then fix `README.md:85-88`: it must stop calling SPEC-01 a blocker.

If restoring is refused on the grounds that v4.1 working docs were deliberately retired, the
alternative is a new `docs/SPEC-RULINGS.md` plus edits to all six citing files. That is the
worse trade, but either beats the status quo, which is fourteen pointers into nothing.

### Owner and demo impact

Owner: **Vasanth** for the file and the README, since the deletion came through his docs
commit. The ruling content for SPEC-02, SPEC-05 and SPEC-07 is **Gnanam's** to state, being
contract and manifest semantics.

Does not block the demo: no code reads this file. Severity is low for the run and **medium for
the submission package**, because the README is the first thing a reader opens and it currently
misrepresents the project's own state. That is the one kind of inaccuracy this codebase cannot
afford, given that its entire pitch is stopping honestly rather than pretending. Cheap to fix,
and worth doing before 6 Aug.

---

## V6-F5 policy_version is an INTEGER column holding TEXT

### What is true today

The column is declared `policy_version INTEGER NOT NULL` in `payment_intents`
(`daemon/store/schema.sql:35`) and `policy_version INTEGER NOT NULL DEFAULT 1` in
`organizations` (`daemon/store/schema.sql:6`).

The value written is a string. `Lifecycle.claimNonce` inserts `in.PolicyVersion`
(`daemon/internal/approval/lifecycle.go:820-836`), whose type is
`PolicyVersion string` (`daemon/internal/policy/policy.go:94`), and every wiring supplies the
literal `"pol_7"`: production at `daemon/cmd/snapfall/main.go:789`, plus
`daemon/cmd/approval-demo/main.go:49`, `daemon/cmd/freeze-demo/main.go:80` and fifteen test
sites. The same literal is the frozen cross-language value in H3's golden vector
(`docs/handshakes/H3-sidecar-api.md:242`, `sidecar/src/h3-golden-vector-test.ts:40`), so the
string form is not an accident and is not going to change.

SQLite's INTEGER affinity converts a text value only if it is well-formed numeric; otherwise it
stores the text as TEXT. **Verified empirically** with Node 22's `node:sqlite` against an
in-memory database using the exact column declaration:

| inserted | `typeof(policy_version)` | `CAST(policy_version AS INTEGER)` | `policy_version > 5` |
| --- | --- | --- | --- |
| `'pol_7'` | `text` | `0` | `1` |
| `'7'` | `integer` | `7` | `1` |

So the breakage is already present and already silent, in two distinct ways:

- `CAST(policy_version AS INTEGER)` yields **0** for every row ever written, so any arithmetic
  or numeric ordering on the column is wrong rather than erroneous.
- A bare comparison such as `policy_version > 5` returns **true** for the text row, because
  SQLite's type ordering puts every TEXT value above every numeric one. A "policy version is at
  least N" guard would pass unconditionally, which is worse than failing: it is a check that
  looks like it works.

`NOT NULL` is satisfied, so nothing complains at insert time. The condition is described
correctly in the budget package's rationale for not building money truth on this table:
"its policy_version INTEGER column receives the string 'pol_7', so numeric comparison on that
table is already broken" (`daemon/internal/budget/budget.go:19-21`). This entry exists so the
fact is not only findable inside a package doc explaining a different decision.

### What breaks or misleads

Nothing today. `grep` over the repository finds **no query that reads `policy_version` at
all**: the only occurrences are the two declarations, the one INSERT, and the budget package's
comment. So this is a latent trap, not a live bug. The trap is specific: the next person who
writes a numeric predicate against this column, reasonably trusting the declared type, gets a
silently wrong answer on a table that records what policy authorized each payment.

### The fix

Declare it for what it holds: `policy_version TEXT NOT NULL` in both tables
(`daemon/store/schema.sql:6,35`). `"pol_7"` is a label, not a number, and the frozen H3 vector
settles that it stays a label.

One caveat that has to be stated with the fix, because it is the reason the budget package
refused to touch this table at all: `store.Open` applies `schema.sql` wholesale and every
statement is `CREATE TABLE IF NOT EXISTS`, with no ALTER path
(`daemon/internal/budget/budget.go:14-18`). Editing the declaration therefore does nothing to
an existing `.db` file. It takes effect only on a fresh database. That is acceptable here
because `scripts/reset_demo` deletes the daemon SQLite store between takes
(`scripts/README.md`), so every demo run already starts from a fresh schema. Do not, however,
expect a developer's long-lived local `.db` to pick it up.

Second-order option worth considering while in there: `organizations.policy_version` defaults to
`1` and is written by nobody (Brain's projection inserts only id, owner, name, created_at,
`daemon/internal/brain/project.go:53-55`), so it is a column with a fabricated-looking default
and no producer. Either write it or drop it.

### Owner and demo impact

Owner: **Gnanam**, who owns the event store and schema (G2, `docs/WORK-SPLIT.md:49`).

Does not block the demo and has no camera surface. Fix it when the schema is next touched for
another reason; it does not deserve its own change before 8 Aug.

---

## V6-F6 Manifest.BudgetMicros is a false guarantee

### What is true today

**Half one: it is parsed and validated.** `Manifest.BudgetUSDC` is the YAML field and
`BudgetMicros int64` its parsed 6dp form (`daemon/internal/agents/manifest.go:53,61-62`).
`validate` parses it and makes a parse failure **fatal**, blocking activation:

```go
micros, err := parseUSDC(m.BudgetUSDC)
if err != nil {
        add(true, "bad-budget", "budget_usdc %q: %v", m.BudgetUSDC, err)
} else {
        m.BudgetMicros = micros
}
```

(`daemon/internal/agents/manifest.go:195-200`). `parseUSDC` rejects negatives, non-decimals,
anything finer than USDC's 6 decimals, and implausibly large values rather than truncating
(`daemon/internal/agents/manifest.go:251-294`). The daemon refuses to boot on a fatal finding
(`daemon/cmd/snapfall/main.go:320-330`). So the field is taken seriously right up to the point
of use.

**Half two: it is never enforced.** `BudgetMicros` is read in exactly two places in the
repository: one non-fatal contradiction warning, that a budget with an empty
`network_allowlist` is unusable (`daemon/internal/agents/manifest.go:218-220`), and a test
(`daemon/internal/agents/manifest_test.go:272`). It reaches no policy evaluation. Main logs
`budget_usdc` for each agent at startup (`daemon/cmd/snapfall/main.go:332-339`) and then
discards the manifests except for a count in the boot event
(`daemon/cmd/snapfall/main.go:402`).

The policy engine has no per-agent rule and no per-agent limit. `PolicyConfig` has
`JobBudgetMicros`, `PerTxLimitMicros`, `DailyCapMicros`, `ApprovalAboveMicros`, the merchant
allowlist and the category lists (`daemon/internal/policy/policy.go:100-108`), and the rule set
is `intent-validation`, `job-budget`, `per-tx-limit`, `daily-cap`, `merchant-allowlist`,
`blocked-category`, `approval-threshold` (`daemon/internal/policy/policy.go:38-44`). Nothing
per agent. `PaymentIntent` does carry `AgentID` (`daemon/internal/policy/policy.go:85`), and no
rule reads it. The reservation ledger folds spend per **job** and per **org** only
(`Ledger.Spend`, `daemon/internal/budget/budget.go:758-780`, using `l.jobOrg[jobID]`), so even
the cumulative state a per-agent rule would need does not exist.

**And a shipped manifest asserts the opposite.** `daemon/manifests/research.yaml:8`:

```yaml
budget_usdc: "1.00"         # per-job default; policy engine enforces (FR-PAY-003)
```

The requirement it cites does back the reading: FR-PAY-003 is "Policy engine enforces job
budget, **agent/task limit**, per-transaction limit, merchant/domain allowlist, category
allowlist, velocity limits, expiry, duplicate-nonce prevention"
(`docs/SRS-v4-annex.md:325`). The agent/task limit half of that P0 requirement is unimplemented,
and the manifest comment states it as done.

### What breaks or misleads

An operator would reasonably read `research.yaml` and conclude that lowering
`budget_usdc` to `"0.10"` caps what the Research agent can spend. It does not. The 4.00
premium purchase, the rejection-beat bait, would still be evaluated purely against the org
policy: `PerTxLimitMicros`, `JobBudgetMicros` and `ApprovalAboveMicros` from
`policy.DemoPolicy()`, with the manifest figure playing no part. Whether the purchase is
allowed or escalated is identical for a manifest that says `"0.10"` and one that says `"100.00"`.

This is worse than an unimplemented feature, because the manifest is the artefact the
architecture points at as the place an agent's authority is declared, and the same file
truthfully enforces its neighbours: `can_sign_payments: false` and `can_request_advance: false`
are hard fatal rejections (`daemon/internal/agents/manifest.go:166-172`), and a shell in
`command_allowlist` or a wildcard host is fatal too
(`daemon/internal/agents/manifest.go:204-214`). Every other permission line in that file is
load-bearing, which is exactly what makes the one decorative line credible. The package doc
says validation exists because "a manifest that would grant an agent authority the architecture
forbids is rejected outright" (`daemon/internal/agents/manifest.go:3-6`); a budget line that
grants nothing and restricts nothing sits oddly against that.

### The fix

Two options, and the cheap one is legitimate.

**Cheap and honest, minutes:** change the comment to say what is true. For example
`# per-job default; NOT enforced yet, the policy engine has no per-agent rule (FR-PAY-003, see
docs/V6-FOLLOWUPS.md V6-F6)`. This codebase's whole discipline is to stop honestly rather than
pretend, and a comment that overclaims is the documentation form of a fabricated success. Do
this one regardless of whether the other is ever done.

**Real, roughly a day:** implement the agent/task limit half of FR-PAY-003.

1. Add `AgentBudgetMicros map[string]int64` (or a per-agent config) to `PolicyConfig`, and a
   rule between `job-budget` and `per-tx-limit`. Respect the engine's existing law: a zero
   limit means NOT CONFIGURED and denies (`daemon/internal/policy/policy.go:97-99`), so decide
   explicitly what an agent absent from the map means, and pin that decision as a fixture case
   the way case 21 pins the job-budget instance.
2. Add a per-agent dimension to `SpendState` and to `Ledger.Spend`. The ledger already carries
   the data it would need on each hold, but its fold predicate is job and org only
   (`daemon/internal/budget/budget.go:758-780`), and no ledger payload carries an `agent_id`
   today (verified: no such key in `daemon/internal/budget/budget.go`). So this is a new fold
   dimension plus a new payload key, not a filter over what exists.
3. Feed the manifests into the wiring. Today `agents.Load`'s result never reaches
   `approval.New` or the policy closure (`daemon/cmd/snapfall/main.go:320-340` versus
   `daemon/cmd/snapfall/main.go:786-789`), which is why the figure is inert.

Note that step 1 collides with [V6-F3](#v6-f3-per-job-chain-budget-versus-per-org-daemon-budget):
both want `Lifecycle.Policy` to become parameterised, and both must answer the same
"what does unconfigured mean" question. If either is ever done, do them together and write one
case table.

### Owner and demo impact

Owner: **Gnanam** for the policy-engine work (G6) and for the manifest schema question, which
is `SPEC-05`. **Vasanth** can land the comment fix inside a docs pass.

Does not block the demo. Severity is medium **as an integrity claim**, not as a runtime risk:
nothing behaves incorrectly, but a reasonable operator would be misled about where an agent's
spending limit lives. The comment fix should land before 8 Aug; the implementation should not
be attempted before it.

---

## Verification notes

Written under the constraint that the **Go toolchain cannot run on this machine** (Windows
Application Control blocks it), so no `go build`, `go vet` or `go test` was attempted. Every
Go and Solidity claim above was established by reading the source at the cited lines and by
exhaustive `grep` for callers and readers, which is sufficient for the questions asked here
(all six are "who calls this" and "who reads this" questions, which static search answers
better than a test run would).

What was actually run:

- `git branch --show-current`, `git log --oneline -25`, `git log --oneline --all --grep=V6`,
  `git show 5999f16 --stat`, `git log --oneline --diff-filter=D --all -- docs/OPEN-SPEC-QUESTIONS.md`,
  `git show 8c04728 --stat`, and `git log --format=%an` over `contracts/src/JobVault.sol` and
  `docs/SRS-v4-annex.md`.
- `ls docs/` plus a `test -f docs/OPEN-SPEC-QUESTIONS.md` existence check: **MISSING**,
  confirming V6-F4's central claim.
- Caller and reader searches for `CalldataRecordExpense`, `CalldataCreateJob`,
  `CalldataStartWork`, `BindVaultJob`, `VaultJobID`, `expense_total_atomic`, `policy_version`,
  `pol_7`, `BudgetMicros`, `receipt_hash`/`ReceiptHash`/`receiptHash`, `payment.executed`,
  `purchase.delivered`, `chain_divergence`, `.Policy = func()`, `OPEN-SPEC-QUESTIONS`, and
  `"policy engine enforces"`.
- The SQLite affinity experiment for V6-F5, run in Node 22.23.1 via `node:sqlite` against
  `CREATE TABLE t (policy_version INTEGER NOT NULL)`, results tabulated in that section.

Not run: the dashboard gates (`npx tsc --noEmit`, `npm test`, `npm run build`). This change is
one new Markdown file under `docs/`, which none of them read, and other agents are editing the
same tree concurrently, so a failure would report their work rather than this. If a gate is
wanted for the record, run it after the concurrent branches settle.
