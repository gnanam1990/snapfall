# sidecar (owner: C — CRITICAL PATH)

TypeScript x402/Gateway payment sidecar + our paid demo API.

## Status (19 Jul 2026)

The **buyer → seller loop is green locally**, ahead of the Tue Jul 21 deadline:

```bash
cd sidecar && npm install && npm run demo:loop
```

Boots the paid demo API on :4099, generates an ephemeral key, and walks the demo purchase
sequence with assertions. No Circle account, no funded wallet, no network required.

```
1. company profile, 0.04 USDC, AUTO_APPROVE       -> 402, sign, retry, 200   (AT-02)
2. premium dataset, 4.00 USDC, needs approval     -> refused before signing  (AT-03)
3. benchmark summary, 0.06 USDC, the adaptation   -> 402, sign, retry, 200   (AT-04)
4. replayed authorization                          -> seller rejects, nonce burned
5. price raised after approval                     -> refused before signing  (AT-05)
6. total external spend                            -> 0.10 USDC              (§15.2)
```

**Screen-record this now** — it is the insurance + deck asset PRD §14.3 asks for.

### What is real vs. not

| | |
| --- | --- |
| Real | HTTP 402 challenge on both x402 transports; EIP-3009 `transferWithAuthorization` signed by a real key; signature verified by the seller via `verifyTypedData`; nonce replay rejected; policy gates enforced before signing |
| **Not real yet** | **Settlement is never broadcast.** The seller verifies the authorization but does not submit it on-chain. That needs a funded Arc testnet wallet + a facilitator. |

So the loop is cryptographically end-to-end and financially a dry run. Closing that gap is
the next task and it needs a human — see Blockers.

## Layout

```
src/x402.ts      wire format: challenge encode/decode, EIP-712 types (both transports)
src/seller.ts    paid demo API — the three §15.2 resources, 402 + verification
src/buyer.ts     purchase() — policy-gated signing path (FR-PAY-005)
src/demo-loop.ts end-to-end proof with assertions
```

### Architecture law, enforced in the type signature

`purchase()` takes an `ApprovedIntent`, not a URL and an amount. It throws `PolicyViolation`
before touching a key when:

- the decision is not `AUTO_APPROVE` / `HUMAN_APPROVED` (FR-PAY-005), or
- the seller's quoted price exceeds what the policy engine reserved (FR-APR-004 / AT-05).

An agent cannot reach the signer with a bare request. That is the PRD's "agents propose,
deterministic systems authorize" boundary expressed as a type.

## Blockers — need a human

1. **Circle CLI terms.** `circle skill install --tool claude-code` exits with
   "Terms acceptance is required before use." Read
   [agents.circle.com/terms-of-use](https://agents.circle.com/terms-of-use) and run it
   interactively. Accepting a vendor ToS is not something an agent should do for you.
2. **Circle dev account + Gateway testnet setup** (account creation is a human step).
3. **Funded Arc testnet wallet** — [faucet.circle.com](https://faucet.circle.com). Needed
   before settlement can be broadcast.
4. **USDC token address on Arc testnet** — `seller.ts` currently uses a placeholder
   (`ARC_USDC_ADDRESS` env). The EIP-712 domain's `verifyingContract` must be the real
   token or signatures will not verify against the real USDC.

## Reusing `packages/circle-tools` — read before you plan around it

PRD §14.3 / [R12] says to reuse `circlefin/agent-stack-starter-kits` `packages/circle-tools`
(Apache-2.0) as the sidecar base. I cloned and read it. **It does not fit as a drop-in base
for this project**, for three reasons:

1. **No Arc.** `src/chains.ts` defines `type Chain = 'BASE' | 'POLYGON'` — a closed TypeScript
   union, with hardcoded RPCs `mainnet.base.org` and `polygon-rpc.com`. Adding Arc means
   forking and editing the package, not configuring it.
2. **Mainnet-only RPCs**, while SEC-010 requires the demo run on testnet assets exclusively.
3. **It shells out to the `circle` CLI** (`src/cli.ts` `runCircle`/`runCircleJson`) and needs an
   authenticated Circle session — which is blocker #1 above.

It is still valuable as a **reference**, and that is how it has been used here: `src/x402.ts`
documents the 402 wire format confirmed against its `services.ts:readAccepts`, including the
non-obvious detail that Gateway-batched options are identified by `extra.name ===
'GatewayWalletBatched'` rather than the top-level `scheme` field.

**Decision needed at standup:** treat circle-tools as reference (current course, loop already
green), or fork it and add an Arc chain entry. Recommend the former — we are already past the
milestone it was meant to accelerate.

## Reference

- `circlefin/arc-nanopayments` — buyer/seller reference (Apache-2.0)
- `circlefin/agent-stack-starter-kits` — see caveat above
