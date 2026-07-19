# Open spec questions

Contradictions and gaps found while implementing against PRD v4.1 FINAL.
Raise at standup. **Anything marked ABI-AFFECTING must be resolved before the Fri Jul 24 freeze.**

| ID | Severity | Area | Status |
| --- | --- | --- | --- |
| [SPEC-01](#spec-01) | **Blocker** | FloatPool advance sizing | Open — blocks `requestAdvance` + AT-11 |
| [SPEC-02](#spec-02) | Medium | JobVault.recordExpense semantics | Implemented conservatively, needs ruling |
| [SPEC-03](#spec-03) | Low | createJob authorization | Implemented, needs ruling |
| [SPEC-04](#spec-04) | Medium — **ABI-AFFECTING** | JobVault↔FloatPool wiring | Open |
| [SPEC-05](#spec-05) | Low | Manifest schema: PRD §8.2 vs. the files on disk | Implemented against the files |

---

## SPEC-01

### The demo's headline number contradicts the advance formula

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

### `JobVault.floatPool` has no setter — **ABI-AFFECTING**

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
