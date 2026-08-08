# Hard questions

Answers to the objections a finance-literate judge raises in the first sixty seconds. Each one is
grounded in a contract line or a chain read, and each says plainly where the answer is currently
weak — a product whose pitch is verifiability cannot bluff its own Q&A.

---

## 1. If the customer already escrowed 100% of the payment, what is the pool actually financing?

This is the first question, and it is the right one. Standard invoice factoring exists precisely
*because* the buyer has not paid. Here the buyer has paid. So what is left to finance?

### The twenty-second answer

> The escrow is the **customer's** money until delivery is accepted — it is refundable, and
> spending it would destroy the refund guarantee that makes the customer willing to prepay at all.
> So the escrow is committed but unspendable. The pool converts it into spendable working capital,
> and is paid for taking **performance risk** — the job fails, the customer is refunded, and the
> advance is never repaid — rather than payment risk.

### The mechanics, if pressed

Three facts, all in frozen contracts:

1. **The escrow is never the operator's to spend.** `JobVault.recordExpense` is accounting-only.
   The contract says it in its own words (`JobVault.sol:152-157`):

   > This is ACCOUNTING ONLY: it moves no USDC. In the demo, agent purchases are paid from the
   > advance sitting in the treasury (x402, off-vault), never from escrow — so escrow stays whole
   > for the waterfall.

   The agents spend the **advance**, which sits in the operator's treasury.

   *One honest edge:* the same comment records that SC-JV-003's wording is "records or releases",
   and that the releasing reading is unresolved (`docs/OPEN-SPEC-QUESTIONS.md` SPEC-02). The
   shipped contract implements the non-releasing reading, so the answer above is true of the code
   as deployed. If asked whether the spec permits a releasing variant: yes, and it is logged as an
   open question rather than settled quietly.

2. **The customer can still get their money back.** Until `acceptDelivery`, refund is live. On
   refund the escrow returns to the customer in full, and only *then* does the pool absorb the
   loss (`JobVault.sol:274-278` — "only after restitution does the pool absorb the loss").

3. **So the pool's principal is genuinely at risk.** `FloatPool.writeOff` is not a formality:

   ```solidity
   uint256 loss = a.principal;   // the fee was never earned; only principal is at risk
   ```

   The loss runs a three-stage waterfall: operator bond, then the first-loss reserve, then
   socialised across LP shares by reducing `totalAssets` — share count unchanged, each share worth
   less, the ERC-4626 way of taking a loss.

**The one-line version:** the customer's prepayment de-risks the *payment*, not the *work*. The
pool prices the work.

### Where this answer is weak, and you should say so before they find it

- **The operator bond is not implemented.** `FloatPool.writeOff` sets `bondSlashed = 0` and emits
  the event anyway; the comment marks it P1. So "the operator has skin in the game" is **not true
  today**. Do not claim it.
- **The first-loss reserve is currently 0.0042 USDC** (read from chain, 3 Aug 2026). With the bond
  at zero and the reserve at dust, a write-off today is borne almost entirely by the LPs. The
  three-stage waterfall is real code with two stages that are, in practice, empty.
- The honest framing is therefore: *the loss waterfall is built and exercised; it is not yet
  capitalised.* That is a straightforward roadmap answer, and far better than being caught.

---

## 2. What stops an operator self-dealing fake jobs to inflate their rate, then defaulting?

Nothing, today. Say so.

The rate rises with accepted jobs (`advanceRate` climbs from the 5000 bps base toward the 8500 cap)
and falls with write-offs, so the mechanism *notices* a default after the fact — the write-off path
ends in `emit RateChanged(org, advanceRate(org))`. But there is no cost to manufacturing history
beforehand, because:

- the operator bond that would make fake jobs expensive is the same unimplemented P1 stage above;
- nothing checks that a customer is independent of the operator.

Two structural limits do apply, and they bound the damage rather than prevent the attack:

- **`ORG_EXPOSURE_CAP_BPS` = 1000.** One org can never have more than 10% of pool TVL outstanding,
  so no single operator can drain the pool regardless of rate.
- **`UTILIZATION_CAP_BPS` = 8000.** Pool-wide lending is capped at 80% of TVL.

The intended fix is the performance bond, sized at 10% of principal. It is designed and not built.

---

## 3. Is this novel? Hasn't someone already done agent credit?

Do not claim a first. [Tessera](https://www.tesseracredit.com/docs) is live on Base doing
invoice-backed USDC credit for agents with limits derived from on-chain settlement history, and
RSoft Agentic Bank won Circle/Arc's own January 2026 hackathon with autonomous agent lending on Arc.
Some judges will have seen both.

What is defensible, and is narrow enough to be true:

- Tessera's buyer does **not** pre-fund; the lender advances against a promise and, by their own
  docs, "lenders take the principal loss (no insurance pool in v0)". Their escrow is listed as an
  unshipped Phase 1.
- Snapfall advances **only against funds already locked on chain**, and repays itself **atomically**
  at delivery — pool first, operator second, in one transaction, by contract control flow.

So: *escrow-first origination plus an atomic repay-the-lender-first waterfall.* As far as we can
find, that combination is unbuilt. "As far as we can find" is the correct strength of claim —
several of the closest candidates are private hackathon repos.

---

## 4. Prove the pool really is repaid first.

One transaction, re-verified against Arc testnet on 3 Aug 2026:

| | |
|---|---|
| tx | `0x108a8f908b368aca286b8011d3dab34fc26c635d32df2689555ffc806ef9de4b` |
| block | 53,613,272 (status success, gas 138,456) |
| log **12** | Transfer → FloatPool `0xde9f58a9…dc86e5` — **0.561000 USDC** |
| log **15** | Transfer → operator `0x99b723ed…3a792d` — **0.439000 USDC** |

The pool is paid at the lower log index, in the same transaction, so it cannot have been paid
second. This is not a convention: `JobVault.acceptDelivery` calls `floatPool.repayAdvance(...)`
before it transfers to the operator, and the contracts are frozen.

Expect a sharp viewer to notice **four** transfer events, not two. Logs 11 and 14 carry the same
amounts at 18 decimals — the native-token mirror, since USDC is Arc's gas token. Know this before
you are asked.

---

## 5. Can we see it run?

Be straight about the state of this. As of 8 Aug 2026 spine runs have been logged (`docs/spine-runs/`;
first automated settlement 2026-08-07), and a 0.04 USDC agent purchase has settled through x402,
self-facilitated (`docs/spine-runs/2026-08-08-first-x402-settlement.md`) — the authorization was
broadcast directly, not through Gateway. Separately, the Circle Gateway facilitator client is built
and has never run against Circle's live service; under that facilitator with no key the seller
records `NOT_BROADCAST`. Two independent facts: settlement happened, and it did not use Gateway.

What *has* happened, on chain, is the full money lifecycle for two jobs: fund → advance → expense →
delivery → accept → waterfall → rate rise. What has not happened is the agent-payment leg settling
real money.

The distinction to draw: **the money mechanism is proven; the agent-payment rail is not.** Both
statements are in the product's own audit surface, which is the point.
