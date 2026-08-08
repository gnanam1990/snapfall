# First x402 settlement — 8 Aug 2026

The x402 agent-payment leg reached **SETTLED** on Arc testnet for the first time. Until now it
rested at `NOT_BROADCAST`: the handshake was real, but nothing was ever submitted, so no payment
had settled and the 2026-08-07 spine run's verdict was UNVERIFIED for exactly that beat. This
closes that gap.

## What settled

| | |
| --- | --- |
| Transaction | [`0x0d39b5738f7042ae82ae0a17f24474e67c27db0cd837b791c112f8d264b6dccc`](https://testnet.arcscan.app/tx/0x0d39b5738f7042ae82ae0a17f24474e67c27db0cd837b791c112f8d264b6dccc) |
| Status / block / gas | success (`1`) · block 55818900 · gas 111,821 |
| Method call | `USDC.transferWithAuthorization` (EIP-3009, `v,r,s` overload) on `0x3600…0000` |
| Payer (`from`) | `0x99B723eD097721036C08dd9DEe307286Df3A792D` (operator) |
| Payee (`to`) | `0xb30AD5579e9d2c193ACFaE243F5915Bbf0F1869E` (seller) |
| Value | `40000` atomic — **0.04 USDC** |
| Seller balance before → after | **`0` → `40000`** atomic, read from chain before and after |
| Receipt `facilitator` field | `{"verify":"self:eip3009-local-recover","settle":"self:transferWithAuthorization@0x3600…0000"}` |

**The payer and payee are two wallets the operator controls** — the seller is a fresh, receive-only
address minted for this. That does not weaken the proof of the *mechanism*: the point is that a
signed EIP-3009 authorization was broadcast and USDC moved on chain between two distinct addresses,
which is what `transferWithAuthorization` settling looks like regardless of who owns the wallets.
A zero-to-a-real-number balance change is the cleanest evidence the transfer executed.

## The method: simulate, then broadcast the same payload

The operator signed an `operator → seller` EIP-3009 authorization, then ran
`transferWithAuthorization` as an `eth_call` (a read — no transaction). Only after that simulation
came back clean was the **same signed payload** broadcast through the real self-facilitator
(`sidecar/src/self-facilitator.ts`). Simulating the exact payload first means a clean simulation
and a reverting send can only differ if something changed on chain between the two — and it did
not. Reproducible via `x402-simulate.ts` / `x402-broadcast.ts`.

## The bug the live run surfaced (the instructive part)

Settlement was blocked by a signature bug that **a green test suite could not have caught**, and
it was hidden precisely because settlement had never run.

We signed `transferWithAuthorization` with EIP-712 domain name **`"USD Coin"`** — Ethereum
*mainnet* USDC's name. Arc's precompile uses **`"USDC"`**. The mismatch is invisible during the
handshake: the buyer signs with the seller's advertised name and the seller verifies locally with
the same name, so 402 → sign → verify → 200 passes. But `transferWithAuthorization` recovers the
signer against the precompile's *own* domain at settle — so a `"USD Coin"` signature recovers to a
different address, and every broadcast we could have made would have reverted. On Circle Gateway
or a self-facilitator alike.

`NOT_BROADCAST` was reading as *"the Circle integration isn't wired yet."* It was also hiding a bug
that would have reverted the first real settlement on any facilitator. Confirmed by computing the
domain separator for each candidate name/version and matching it against the deployed
`DOMAIN_SEPARATOR` — only `"USDC"/"2"` matches (`sidecar/src/usdc-domain-chain-test.ts` re-derives
it from chain). **This is the second time this session a live run found what CI could not** — the
first was the missing `startWork`/`submitDelivery` caller in the 2026-08-07 run.

## What this proves, and what it does not

- **Proven, end to end on Arc:** the x402 handshake, the EIP-3009 signature over the correct USDC
  domain, and on-chain settlement.
- **Not proven:** the Circle Gateway integration. Settlement here was **self-facilitated** — the
  authorization broadcast directly, not through Gateway. The `CircleFacilitator` client is built
  and has never run against Circle's live service. The two are independent facts: a payment has
  settled; the Gateway path has not run. The receipt's `self:` facilitator markers make a
  self-facilitated settlement impossible to mistake for Circle evidence, and `capture-v1-fixture`
  (AT-18) refuses it as such.
