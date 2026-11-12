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

**Run live on Arc Testnet (4 Nov 2026)**, each signed by the Circle treasury wallet
`0xb414…4CA` through `chainops -signer circle`, receipts read from our own RPC:
`createJob` ([0x8c7853b5…](https://testnet.arcscan.app/tx/0x8c7853b5e2d655adc7695e9445aa745bf1d695af90669c696bc909dad206655e)),
a USDC transfer ([0xc289df9a…](https://testnet.arcscan.app/tx/0xc289df9afd89bc6312bba5413ffba9fd4e09c87dcdc7c46c32d9b60e7fbd66da))
and `requestAdvance`, 0.50 USDC advanced
([0xa2a67fcd…](https://testnet.arcscan.app/tx/0xa2a67fcd1108d79bf5871463419f85eee338b498f5d3699e325182f7db470c9b)).

**Also run live the same day**, receipts read back from arcscan on 12 Nov 2026:

- **The sidecar's x402 signer.** The Circle treasury signed the EIP-3009 authorization for a
  0.04 USDC purchase (`0xb414…4CA` → `0x99B7…792D`, 40000 atomic).
- **The relayer wallet settling it.** The self-facilitator broadcast `transferWithAuthorization`
  from the Circle relayer wallet `0x792b…75b4`
  ([0x63731fac…](https://testnet.arcscan.app/tx/0x63731fac1ec20d0f815bd1e0e6039525c3504750c3b1f1b63837211125264201)).
- **An advance made by the running daemon.** The daemon ran on the Circle lane; the advance was
  proposed through the owner API, approved by a human, and recorded as `advance.executed`
  ([0x162dafe4…](https://testnet.arcscan.app/tx/0x162dafe462ce6d305b5435689cdf776993ea16a68ebe530b84649369d074bc25):
  Circle treasury → FloatPool, 0.50 USDC advanced).

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

**Run live on Arc Testnet (4 Nov 2026)** at `snapfall-blue.vercel.app`: a passkey wallet was
created on a real device (`0x1cc1…6705`), the page refused to fund a job whose customer is
another address, and on a job naming that wallet it funded the escrow with one sponsored user
operation (bundler → EntryPoint v0.7,
[0x1e1b48f8…](https://testnet.arcscan.app/tx/0x1e1b48f89f57cb24c5413de81ac4bc2b7b4d50889084f8ba1626200ce012fb26)):
the job reads `Funded` on chain and the wallet paid exactly the 1 USDC price, no gas.

**Run live (12 Nov 2026):** the customer accepted and settled a 19.5 USDC job from the same
passkey wallet, and the daemon recorded `settlement.executed` with `actor: customer` without a
second transaction
([0x9a06929d…](https://testnet.arcscan.app/tx/0x9a06929d4c91a02a6848cbf7585625f657fa6267df0f183c68349437a23de947);
[proof](TESTNET-ACCEPTANCE-PROOF-2026-11-12.md)). That acceptance went through the `/pay` wallet
panel; the portal's "Accept & settle from my wallet" button itself has not yet been run live.

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

**Run live (12 Nov 2026), Base Sepolia → Arc Testnet:** a burn on Base Sepolia
([0x7d2f7f25…](https://sepolia.basescan.org/tx/0x7d2f7f2526dfe5cc772343eebec75290d249ac4779d3af004760ddf2cf88f7be)),
then one Arc user operation that minted it and funded a 2 USDC job
([0x5059467f…](https://testnet.arcscan.app/tx/0x5059467f58fd9620205f9ad427764609814f07f44e100795483641ee50e13fa1)).
The same run found an earlier, unminted burn of the same amount, which was recovered by a
mint-only relay
([0xe545954d…](https://testnet.arcscan.app/tx/0xe545954dd78647d1dd61f734f2b0bd543b7a53f5ae5cecd2bba443f699d7516d)).
That duplicate is why the burn is now saved by its user-operation hash **before** it is
broadcast (`lib/cctpBurnGuard.ts`), so a lost receipt is looked up, not burned again. Details and
limits: [TESTNET-ACCEPTANCE-PROOF-2026-11-12.md](TESTNET-ACCEPTANCE-PROOF-2026-11-12.md).

**Not yet run live:** the operator payout off Arc (`chainops payout`).

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
