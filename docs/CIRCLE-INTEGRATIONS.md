# Circle integrations: who holds which key, and how USDC gets in and out

Snapfall uses five Circle products, each for one job. This page says what each one is for,
what it is **not** for, how to turn it on, and what has and has not been run live.

| Product | Used for | Custody | Code |
| --- | --- | --- | --- |
| Developer-Controlled Wallets | the operator's treasury signer (daemon + sidecar), and optionally the self-facilitator's gas relayer | developer (us) | `daemon/internal/circlewallet`, `sidecar/src/circle-wallet.ts` |
| Modular Wallets (passkey) + Gas Station | the customer funds and accepts from their own wallet | the customer | `dashboard/lib/passkeyWallet.ts`, `components/OwnWalletPanel.tsx` |
| CCTP V2 | customer pays with USDC from another chain; operator payout off Arc | n/a (protocol) | `dashboard/lib/cctp.ts`, `daemon/internal/cctp`, `chainops payout` |
| Facilitator Service (x402) | settling the workers' x402 purchases, Arc mainnet and testnet | n/a (settles the buyer's EIP-3009 authorization) | `sidecar/src/circle-facilitator-service.ts` (#113) |
| Gateway (x402) | the same, testnet only | Gateway balance | `sidecar/src/facilitator.ts` (never run live, see §4) |

The rule across all of them: **customer money is never in a wallet the operator controls.** The
developer-controlled wallet holds the operator's own treasury only.

## 1. Operator treasury: Developer-Controlled Wallet

See [CIRCLE-TREASURY-WALLET.md](CIRCLE-TREASURY-WALLET.md). In short: set
`CIRCLE_TREASURY_WALLET_ID` (+ `CIRCLE_API_KEY`, `CIRCLE_ENTITY_SECRET`) and both the daemon's
`requestAdvance` lane and the sidecar's x402 signer use the Circle wallet instead of
`TREASURY_PRIVATE_KEY`, with no fallback to the raw key. `CIRCLE_BROADCASTER_WALLET_ID` does the
same for the self-facilitator's relayer (`BROADCASTER_PRIVATE_KEY`), so the sidecar can run with
no raw key at all. `daemon/cmd/circle-setup` registers the entity secret without showing it,
creates both wallets, and checks the setup read-only (`secret`, `wallets`, `check`).

**Not yet run live:** a Circle signature, a Circle contract execution, or a daemon advance
through Circle. All three are tested against a fake Circle API that decrypts the ciphertext and
signs what it is sent; the network half needs the Circle API key, entity secret and wallets.

## 2. Customer: their own passkey wallet

**What changes.** In the demo, the daemon holds the customer key and signs `acceptDelivery`
for them (stated openly as demo-custodial). With a Circle client key configured, the portal
switches to **self-custody mode**: the customer signs with a passkey on their own device, which
owns a Circle modular smart account on Arc. Circle's paymaster sponsors the gas, so the customer
needs no extension, seed phrase or gas token.

**Two surfaces.**

- `/pay/<vaultJobId>`, with no token: the customer creates or signs in to their passkey wallet
  and funds the escrow (`approve` for exactly the price, then `fund`, as one sponsored user
  operation). The chain is the gate: `fund` only succeeds from the job's customer address. This
  is also where a new customer gets their wallet address to give the operator **before** the
  job is created, because the job must name that address as its customer.
- `/portal/<jobId>?token=…`, the existing magic link: the Accept button is replaced by
  "Accept & settle from my wallet". The wallet signs `acceptDelivery`, then the page sends the
  usual credential-gated Accept so the daemon's record matches.

**The daemon side.** Before submitting a settlement, the daemon now asks the chain whether the
job is already `Accepted`. If the customer's own wallet settled it, the daemon records
`settlement.executed` with `actor: customer` (and the indexed transaction once the indexer has
it) instead of submitting a second `acceptDelivery` that would revert. Any `JobSettled` is
invoiced by the existing settlement observer whoever signed it.

**Self-custody mode is all-or-nothing per deployment.** With `NEXT_PUBLIC_CIRCLE_CLIENT_KEY`
set, the custodial Accept button is not shown at all. Two buttons that settle through different
keys would be a choice the customer cannot make well.

**Checks that cannot be skipped.** A user operation can be included while its call reverted, so
success is the user-operation receipt's `success` flag, never "it was mined". The gate in
`lib/customerWallet.ts` only offers the action the contract would accept from this address in
this state, so the sponsor does not pay for a call that is certain to revert.

**Turn it on (human steps).**

1. Circle console: create a **Client Key** restricted to the portal's domain, and set the
   passkey domain (Wallets → Modular Wallets → Passkey) to that same domain.
2. Configure a Gas Station / paymaster policy for Arc Testnet (and Base Sepolia, for §3).
3. Set `NEXT_PUBLIC_CIRCLE_CLIENT_KEY` (and optionally `NEXT_PUBLIC_CIRCLE_CLIENT_URL`) for the
   dashboard build. The client key is public by design; it authorizes nothing.

**Not yet run live:** a real passkey registration and a sponsored user operation on Arc Testnet.
The SDK's API is taken from its own type definitions (`@circle-fin/modular-wallets-core`
1.0.16, pinned with viem 2.55.11 to match it) and typechecks; the calldata and gating are unit
tested; the network half needs the client key.

## 3. CCTP V2: USDC in from another chain, and out to one

**Customer pays from Base Sepolia (testnet) / Base (mainnet).** On `/pay`, if the customer's Arc
wallet holds less than the price, the page offers to bring USDC over with the same passkey:

1. On the source chain: `approve` TokenMessengerV2, `depositForBurn` to the customer's Arc
   address, Fast finality. The burn is sized so that even at the full fee it permits, what is
   minted covers the price (`burnFor`, tested across fees and prices); the fee is read from
   Circle's table, rounded **up** (it is fractional: 1.3 bps live for Base Sepolia → Arc).
2. Wait for Circle's attestation (proxied through `/api/cctp/message`; fixed host, listed
   domains only).
3. On Arc, one user operation: `receiveMessage`, `approve`, `fund`. Mint and payment land
   together.

The burn is saved in the browser before anything else, so closing the tab mid-transfer leads
back to "finish moving your USDC", not to lost funds; and if someone else relayed the mint in
the meantime (`destinationCaller` is open), the nonce check skips it instead of reverting. An
attestation that mints to any other address or domain is refused, not relayed. What lands is
taken as the amount minus the executed fee, or minus the burn's cap when no executed fee is
reported; if Circle reports neither, the page mints alone, reads the balance, and only then
decides whether to pay (`finishPlan`, `lib/cctp.ts`). A saved burn is only resumed if it names a
listed source domain and a 32-byte hash.

**Operator payout off Arc.**

```
chainops [-signer circle] payout <domain> <recipient> <usdc>
chainops payout-mint <domain> <arc-burn-tx> <recipient>
```

Burns on Arc with the operator's own lane (raw key or the Circle treasury wallet), waits for the
attestation, and prints the message and attestation. With `-dest-rpc`, `-dest-chain-id` and
`-dest-key-env` it also submits `receiveMessage` on the destination; that key only relays and
never holds the USDC. Leaving Arc uses Standard finality (Arc finalizes on inclusion); the fee is
read every time (currently 0) rather than assumed.

V2 only. Testnet domains: 0 Ethereum Sepolia, 3 Arbitrum Sepolia, 6 Base Sepolia, 26 Arc.

**Not yet run live:** a real burn and mint. Circle's fee and message endpoints were exercised
live through the proxy (fee table read; unknown burn answers "not seen yet").

## 4. Seller settlement: which facilitator, and what a live check of Gateway found

The seller has three ways to settle a worker's x402 payment (`facilitator-select.ts`):

| Path | Selected by | Networks | Evidence returned |
| --- | --- | --- | --- |
| Circle Facilitator Service | `SNAPFALL_CIRCLE_FACILITATOR_SERVICE=1` | Arc mainnet + testnet | a transaction hash |
| Self-relay | `SNAPFALL_SELF_FACILITATOR=1` | Arc mainnet + testnet | a transaction hash, from our own receipt |
| Circle Gateway | default | Arc testnet only | a transfer UUID (see below) |

**Facilitator Service is the mainnet path.** It settles the same EIP-3009 authorization over the
USDC domain that the buyer already signs, including through the Circle treasury signer (§1), so
nothing on the buyer side changes. It returns a 0x-64 hash, which is the only thing the client
counts as settled. Its `maxTimeoutSeconds` is how long the service may wait, not the
authorization's validity, so it does not ask for Gateway's 7-day minimum; whether it settles the
buyer's 1-hour authorizations has not yet been shown by a live settle. It authenticates with `CIRCLE_API_KEY`, the same variable the
developer-controlled wallets use; settling with a key binds the seller's payTo to that Circle
account.

**Self-relay** submits `transferWithAuthorization` itself, from a Circle wallet
(`CIRCLE_BROADCASTER_WALLET_ID`, §1) or `BROADCASTER_PRIVATE_KEY`. It is the path that needs no
Circle facilitator at all.

### What a live check of Gateway found

The facilitator client (`sidecar/src/facilitator.ts`) was written from documentation and never
run. Checking it against the live testnet API on 2026-10-28 found:

1. **Wrong path, now fixed.** It called `/gateway/v1/x402/{verify,settle}`, which answers 404.
   The live endpoints are `/v1/x402/{verify,settle}` (they answer 400 asking for
   `paymentPayload`/`paymentRequirements`). The fixture contract and docs are updated.
2. **The authorization must be a Gateway one.** `GET /v1/x402/supported` lists one kind for
   Arc Testnet: `x402Version 2`, scheme `exact`, `extra.name: GatewayWalletBatched`, version
   `1`, `verifyingContract` = the GatewayWallet `0x0077777d7EBA4688BDeF3E311b846F25870A19B9`.
   The buyer currently signs against the **USDC** contract's domain, which Gateway will not
   accept, and the seller does not advertise the Gateway kind.
3. **Minimum validity is 7 days** (`minValiditySeconds: 604800`). The buyer deliberately caps
   an authorization at 1 hour because it is a bearer instrument. Under Gateway the exposure is
   bounded by the buyer's Gateway **deposit**, not by the authorization's lifetime, so the cap
   would move from "time" to "deposited balance". That is a security decision, not a refactor.
4. **Settle returns a transfer UUID, not a transaction hash.** Settlement is batched; the
   on-chain movement happens later. The facilitator and AT-18's fixture capture both treat only
   a 0x-64 hash as evidence, so a successful Gateway settle would currently be reported as
   unsettled.
5. **A Gateway deposit is a prerequisite.** The buyer needs USDC deposited into the
   GatewayWallet (a deposit, never a plain transfer, which loses the funds).

Items 2–5 would change the H3 contract with the daemon, the AT-18 evidence rule and the buyer's
lifetime cap together. Facilitator Service avoids all four, so they only matter if Gateway itself
is still wanted; whether to keep, fix or drop the Gateway client is a decision to make with
Vasanth, and nothing on the mainnet path waits on it.
