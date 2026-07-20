# Open spec questions

Contradictions and gaps found while implementing against PRD v4.1 FINAL.
Raise at standup. **Anything marked ABI-AFFECTING must be resolved before the Fri Jul 24 freeze.**

| ID | Severity | Area | Status |
| --- | --- | --- | --- |
| [SPEC-01](#spec-01) | **Blocker** | FloatPool advance sizing | ✅ **RESOLVED 19 Jul** — budget term removed; `advance = rate × customerPayment` |
| [SPEC-02](#spec-02) | Medium | JobVault.recordExpense semantics | Implemented conservatively, needs ruling |
| [SPEC-03](#spec-03) | Low | createJob authorization | Implemented, needs ruling |
| [SPEC-04](#spec-04) | Medium — **ABI-AFFECTING** | JobVault↔FloatPool wiring | ✅ **RESOLVED 19 Jul** — one-shot `wireFloatPool` / `wireJobVault` |
| [SPEC-05](#spec-05) | Low | Manifest schema: PRD §8.2 vs. the files on disk | Implemented against the files |
| [SPEC-06](#spec-06) | **Blocker** | Demo pool seed breaks the exposure cap | Open — the advance reverts at the 0:40 beat |
| [SPEC-07](#spec-07) | Low | Refund restitution vs. recorded expenses | Implemented as full restitution |

---

## SPEC-01

> ### ✅ RESOLVED — 19 Jul 2026
> **Ruling: option (b).** `maxOperatingBudget` is removed from the advance formula entirely.
> `advance = advanceRate(org) × customerPayment`. The operating budget remains solely the
> SC-JV-003 spend bound. SC-FP-002 and FR-FLT-002 updated in the PRD; `requestAdvance`
> implemented; the 6.00 budget no longer caps the 12.50 advance
> (`test_requestAdvance_ignoresMaxOperatingBudget`).
>
> Solvency no longer relies on the budget term — it holds by construction, since the worst
> case is 85% × 1.02 = 86.7% of an already-escrowed payment. Asserted by
> `testFuzz_advance_neverExceedsEscrow` and `testFuzz_settlement_escrowAlwaysCoversPool`.

### Original finding: the demo's headline number contradicts the advance formula

**Severity: blocker.** This is the 0:40–1:00 beat of the video — the "snap."

The formula appears twice, identically:

- **SC-FP-002** (§7.2): `advance = min(maxOperatingBudget, advanceRate(org) × customerPayment)`
- **FR-FLT-002** (§5.13): same, "computed on-chain (SC-FP-002)"

The demo seed (§15.2) sets:

| Field | Value |
| --- | --- |
| Customer payment (escrow) | 25.00 USDC |
| **Max operating budget** | **6.00 USDC** |
| Advance rate (job 1) | 50% |
| **Advance** | **12.50 USDC** |

Apply the formula: `min(6.00, 0.50 × 25.00)` = `min(6.00, 12.50)` = **6.00 USDC**, not 12.50.

The seed data and the formula disagree, and four other places in the PRD side with the seed:

- §3.4 (primary use case): "at the org's 50% rate, 12.50 USDC lands in the treasury sub-second"
- §15.1 (demo script, 0:40): "12.50 USDC (50% rate) lands sub-second; treasury 0 → 12.50"
- §15.2: waterfall "Pool receives 12.75; operator receives 12.25"
- **AT-12** and **AT-11**: "Funded 25 USDC job at 50% → requestAdvance transfers **exactly 12.50** to treasury"

**AT-11 is an acceptance test that fails against our own seed data.** Every downstream number
inherits the error: pool repayment 12.75, operator 12.25, operator net 24.65, gross margin 24.65.

For comparison, if the advance really were 6.00: fee 0.12, pool receives 6.12, operator receives
18.88, operator net 24.78. Internally consistent — but it is not the demo in the script, and
"6.00 USDC lands" is a materially weaker beat than "12.50."

### Root cause

`maxOperatingBudget` is doing two unrelated jobs. It is a **spending** cap (SC-JV-003 bounds
`recordExpense` by it — correct) and the formula also uses it as a **borrowing** cap. Those are
different quantities. The demo spends 0.10 USDC total against a 6.00 budget while wanting to
borrow 12.50; any sane parameterization has borrowing capacity above spending capacity.

### Options

| | Change | Cost | ABI |
| --- | --- | --- | --- |
| **(a)** | Raise demo-seed `maxOperatingBudget` to ≥ 12.50 (say 15.00) | One number in a seed file | none |
| **(b)** | Drop the `min()`; cap the advance by `customerPayment` only | Edit SC-FP-002 + FR-FLT-002, version bump | none |
| **(c)** | Add a distinct `maxAdvance` field to `Job`, separate from `maxOperatingBudget` | New struct field + `createJob` arg | **breaks ABI — must land before Jul 24** |

**Recommendation: (a) now, (c) later.** (a) is a one-line seed change that keeps the formula,
the contract, and every number in the video intact. (c) is the conceptually correct fix and
belongs on the post-hackathon roadmap — but if the team wants it in the MVP it has to land
**before Friday**, because it changes `createJob`'s signature.

Note (a) does slightly weaken the "bounded agent spend" story, since the operating budget rises
to 15.00 while actual spend stays 0.10. If that matters for the narrative, (b) preserves a tight
6.00 spending cap and still advances 12.50.

**Until this is ruled on, `requestAdvance` stays unimplemented** — it cannot be written without
knowing which number is authoritative, and its tests would encode the wrong one.

---

## SPEC-02

### `recordExpense`: does it move money?

SC-JV-003 reads: "Only the operator/authorized treasury **records or releases** approved onchain
expenses, bounded by maxOperatingBudget." Two readings:

1. **Accounting only** — increments `onchainExpenses`, transfers nothing.
2. **Releasing** — also transfers USDC out of escrow to the operator.

**Implemented as (1).** Rationale: the function is named `recordExpense`, its event is
`ExpenseRecorded`, and in the demo agent purchases are paid via x402 from the *advance sitting in
the treasury* — never from escrow. Under reading (2) escrow would drop to 24.90 before settlement
and §15.2's "operator receives 12.25" arithmetic breaks.

Reading (2) would also need a rule for what happens to released expenses in the waterfall, which
the PRD does not give. Locked in by `test_recordExpense_movesNoEscrow`; flag if wrong.

---

## SPEC-03

### Who may call `createJob`?

Unspecified. SC-JV-001 covers *funding* (customer only) but nothing says who registers the job.

**Implemented:** `admin` (demo seeding) or the designated `operator` (self-service, multi-org
ready). The customer is designated at creation and is the only address that can fund, so a
hostile creator cannot redirect anyone's money — the blast radius is job-ID squatting.

---

## SPEC-04

> ### ✅ RESOLVED — 19 Jul 2026
> **Ruling: implemented as specified.** `JobVault.wireFloatPool(address)` and
> `FloatPool.wireJobVault(address)` — admin-only, one-shot, revert `AlreadyWired`, emit
> `Wired(address)`. `repayAdvance` / `writeOff` carry an `onlyJobVault` modifier that also
> rejects an unwired pool with `NotWired` (SC-FP-010). `Deploy.s.sol` wires both directions
> and logs every address. Covered by `test/Wiring.t.sol` (11 tests).

### Original finding: `JobVault.floatPool` has no setter — **ABI-AFFECTING**

`JobVault` declares `IFloatPool public floatPool` with the comment "set once by admin," but no
function sets it. `script/Deploy.s.sol`'s stated deploy order ends with "wire addresses," which
is currently impossible. `acceptDelivery` cannot execute the SC-JV-009 waterfall without it.

Symmetrically, `FloatPool.jobVault` has no setter either, and SC-FP-010 ("repay/writeOff callable
only by the registered JobVault") depends on it being set.

Needs `setFloatPool(IFloatPool)` / `setJobVault(IJobVaultView)`, admin-only, one-shot. These are
*additions*, which the freeze note in `JobVault.sol` permits ("additions ok") — but they add ABI
surface, so they should land before Jul 24 rather than after. Not implemented: outside the Jul 19
task list, and it should be a deliberate call rather than a drive-by.

---

## SPEC-05

### The manifest schema in PRD §8.2 is not the schema on disk

PRD §8.2 "Example agent manifest" shows a nested schema:

```yaml
id: market-researcher
role: Market Researcher
manager: business-manager
permissions:
  filesystem: { read: [...], write: [...] }
  commands: [...]
  network: { allow: [...] }
finance:
  job_limit_usdc: 3.00
  transaction_limit_usdc: 0.10
  approval_above_usdc: 0.10
  categories: [...]
escalation:
  on_policy_denial: business-manager
  on_sensitive_egress: human-owner
```

`daemon/manifests/*.yaml` use a flatter, different one — no `id`, no `manager`, a single
`budget_usdc` instead of three finance limits, flat `filesystem_scope` /
`command_allowlist` / `network_allowlist`, and a single `escalates_to`.

**Implemented against the files on disk**, since those are what the daemon must load today
and they cover FR-ORG-003's required fields.

Two things the §8.2 schema has that the files do not, both of which the policy engine will
want and neither of which is currently expressible:

- **Separate transaction vs. job limits.** FR-PAY-003 requires enforcing "job budget, agent/task
  limit, per-transaction limit" as distinct checks. The files carry one `budget_usdc`, so the
  0.10 USDC auto-approval threshold in the demo (§15.2) has nowhere to live in the manifest.
- **Category allowlists.** FR-PAY-003 and Appendix A.1 both key limits by category
  (`business-data`, `model-inference`); the files have no categories field.

Neither blocks Day 1, but B should extend the manifest schema before the policy engine lands,
or the thresholds will end up hardcoded. This is local API surface, not contract ABI, so it is
not bound by the Jul 24 freeze — but FR-ORG-005 (manifest import/export) is easier to honour if
the schema settles early.

---

## SPEC-06

### The demo pool seed cannot support the demo advance — **blocker**

Found while implementing the SC-FP-006 caps.

PRD §15.2 sets **"Pool seed (demo LPs) | 100.00 testnet USDC"**. SC-FP-006 caps per-org
exposure at **10% of TVL**. The demo's advance is **12.50 USDC**:

```
12.50 / 100.00 = 12.5%  >  10%  ->  requestAdvance reverts CapExceeded
```

**The advance reverts at the 0:40 "snap" beat** — the single most important moment in the
video. This is the same class of error as SPEC-01: a seed number that contradicts a contract
rule, not a contract bug.

For a 12.50 advance to stay inside the cap, TVL must be **≥ 125.00 USDC**.

| | Change | Cost |
| --- | --- | --- |
| **(a)** | Raise the demo pool seed to ≥ 125.00 (suggest **150.00** for headroom) | One number in §15.2 + the seed script |
| **(b)** | Raise `ORG_EXPOSURE_CAP_BPS` above 1250 | Weakens SC-FP-006 and the rate-gaming answer in §15.5 |

**Recommendation: (a), seeded at 150.00.** It costs one number, keeps the cap story intact for
judge Q&A ("per-org exposure cap" is part of the anti-gaming answer), and leaves room for the
second-job P2 beat at the improved 55% rate (13.75, which needs TVL ≥ 137.50).

The contracts already enforce the cap as specified. Both sides of the boundary are pinned by
tests: `test_exposureCap_rejectsDemoSeedOf100` and `test_exposureCap_admitsExactlyTenPercent`.
The test suite seeds **150.00**. **§15.2 has NOT been edited** — the demo seed is the
architect's number to change.

---

## SPEC-07

### Does a refund deduct recorded expenses?

SC-JV-006 constrains refund/cancel by "state, deadline, spent amount". The spent-amount half
is ambiguous once SPEC-02 ruled that `recordExpense` is accounting-only.

**Implemented: restitution is the FULL `customerPayment`.** `onchainExpenses` never left
escrow (SPEC-02), so deducting it would strand exactly that much USDC in the contract with no
party able to claim it — there is no specified recipient for a retained expense on a refund.

The alternative reading — pay `customerPayment - onchainExpenses` to the customer and release
`onchainExpenses` to the operator as compensation for work performed — is defensible but
invents an operator payout the PRD never describes. Not implemented on that basis.

In the demo this is moot: `recordExpense` is never called on the vault, since agent purchases
are paid via x402 from the advance sitting in the treasury. Pinned by `test_refund_conservesValue`.
