# V3: Circle stack setup (the one human-blocked task)

**Owner:** Vasanth. **Status:** the only V-task not merged. Everything else in V1 to V12 is done.
**Read this instead of the PRD.** Every fact below was derived from the repo, and every file
path and line reference is real. Where the repo has never run a command, this runbook says so
rather than inventing flags.

## What V3 is, verbatim

> **V3. Circle stack setup.** Gateway testnet deposit, Agent Wallet with spend-policy configured
> (outer guard), Circle CLI installed and hitting our wallet. *Done when:* CLI lists the agent
> wallet and a policy rule demonstrably blocks an over-limit test payment at the wallet layer.
> *(0.5d)*
>
> `docs/WORK-SPLIT.md` §2, Phase 1

Two proofs, therefore two artifacts to capture:

1. **P1** `circle` CLI output listing our agent wallet.
2. **P2** a test payment above the policy limit, refused **at the wallet layer**, with the
   refusal text captured. The refusal must come from Circle, not from our code. Our own policy
   engine already refuses over-limit payments (that is the inner guard, AT-03 and AT-05, already
   green). P2 only counts if our software is not the thing saying no.

## Why this task is blocked on a human, precisely

Four acts cannot be delegated to an agent, and an agent should not attempt them:

| Act | Why only you |
| --- | --- |
| Accept the Circle Agent Stack terms of use | Accepting a vendor ToS is a legal act. `circle skill install --tool claude-code` already exits with "Terms acceptance is required before use" (recorded in `sidecar/README.md`, Blockers 1). Read [agents.circle.com/terms-of-use](https://agents.circle.com/terms-of-use) yourself. |
| Create the Circle developer account | Account creation, email verification, credentials. |
| Claim testnet USDC from the faucet | [faucet.circle.com](https://faucet.circle.com) is reCAPTCHA-gated. 20 USDC per claim, one claim per roughly 2 hours (`docs/RUNBOOK.md`). The gate exists on purpose and must not be automated. |
| Paste API keys / wallet secrets into a shell or a `.env` | Secrets stay in your hands. `.env` and `.env.*` are gitignored; `.env.example` is the only committed one. |

Everything after those four is one command each, and the commands are listed below.

## Before you start

| Fact | Value | Where it lives |
| --- | --- | --- |
| Chain | Arc testnet, chain id **5042002** | `deployments/arc-testnet.json` `network.chainId` |
| RPC | `https://rpc.testnet.arc.network` | same file, `network.rpcUrl` (override: `ARC_TESTNET_RPC`) |
| Explorer | `https://testnet.arcscan.app` | same file, `network.explorerUrl` |
| **USDC on Arc testnet** | **`0x3600000000000000000000000000000000000000`** | same file, `contracts.usdc.address`. This resolves `sidecar/README.md` Blockers 4: the address is known and committed, the placeholder in `seller.ts` is only a default. |
| Gas token | USDC is Arc's native gas token | `docs/RUNBOOK.md`, "Wallet health and funding" |

Read the rate-limit and faucet-cadence sections of `docs/RUNBOOK.md` before recording day. The
full demo figures need roughly 9 faucet claims across about 18 hours, so claim early.

## Step 1 (human): Circle developer account and terms

1. Create the Circle developer account and reach the Agent Stack console.
2. Read and accept [agents.circle.com/terms-of-use](https://agents.circle.com/terms-of-use).
3. Create an API key for **testnet**. Keep it in `sidecar/.env`, never in a command line and
   never in a commit.

Honest note: **no code in this repo reads `CIRCLE_API_KEY` today.** It exists in
`sidecar/.env.example` as a placeholder for this task. Grep confirms zero consumers. If V3
introduces a real Circle API call, that is a new consumer to add deliberately, not an existing
integration you are switching on.

## Step 2 (one command): install the Circle CLI

```bash
bun add -g @circle-fin/cli
circle --version
```

Then, optional but the reason the CLI is in the adoption map, install the Agent Skills into your
IDE (`docs/SRS-v4-annex.md`, kickoff checklist):

```bash
circle skill install --tool claude-code
```

This is the command that currently fails on terms acceptance. After Step 1 it should proceed.

## Step 3 (human + one command): Agent Wallet, then prove the CLI sees it

Create the Agent Wallet in the console (human), then list it from the CLI:

```bash
circle wallet list
```

**Do not trust that subcommand blindly.** No command in this repository has ever talked to the
Circle CLI, so the exact verb and flags are not verified here. Run `circle --help`, then
`circle <group> --help`, and use what the installed version actually offers. Reference:
[developers.circle.com/agent-stack/agent-wallets](https://developers.circle.com/agent-stack/agent-wallets)
(`[R4]` in `docs/SRS-v4-annex.md`).

**P1 is the output of that list command showing our wallet.** Paste it into the V3 PR body.

## Step 4 (human): Gateway testnet deposit

Fund the Agent Wallet, then make the Gateway deposit that Nanopayments settle against
([developers.circle.com/gateway/nanopayments](https://developers.circle.com/gateway/nanopayments),
`[R5]`).

Sizing: V1 needs **one 0.04 USDC purchase**. The demo's total external spend is 0.10 USDC
(`docs/PRD.md` §15.2, asserted in `npm run demo:loop` check 6). A single 20 USDC faucet claim
covers the x402 side many times over. The 150 USDC pool seed is V12's separate concern, not
this one.

Check wallet balances with the read-only path from the repository root:

```bash
export SNAPFALL_TREASURY_ADDRESS=0x...
export SNAPFALL_CUSTOMER_ADDRESS=0x...
./scripts/testnet-ops
```

That prints current versus minimum balances and exits with the faucet URL when a wallet is low.
It never sends anything without `--fund`. See `docs/RUNBOOK.md` for the guarded top-up path
(`--fund` requires a named Foundry keystore account, `SNAPFALL_FUNDER_ACCOUNT`, and refuses raw
private keys).

## Step 5 (human, in the console): the spend policy, which is the outer guard

This is the load-bearing half of V3. `docs/handshakes/H3-sidecar-api.md` §8 states plainly that
the buyer layer **cannot** constrain which facilitator settles a signed EIP-3009 authorization,
because the authorization is a bearer instrument once handed over. The Agent Wallet spend policy
is what actually binds settlement to the approved payee and to Circle's rails. Our pre-sign
payee equality check is the inner half and is already merged.

Configure at minimum:

| Rule | Value | Why this value |
| --- | --- | --- |
| Per-transaction limit | **0.10 USDC** | Above the 0.06 benchmark resource, below the 4.00 premium dataset. Makes the AT-03 rejection beat fail at the wallet layer too, not only in our policy engine. |
| Payee allowlist | the `SELLER_ADDRESS` you configure in Step 6 | This is the rule that makes "Circle-facilitator-only" mean something (H3 §8, point 1). |
| Asset | USDC `0x3600000000000000000000000000000000000000` | The only asset we transact. |
| Chain | Arc testnet 5042002 | SEC-010: testnet assets exclusively. |

## Step 6 (one command each): the env vars, and exactly which file reads each

Put these in `sidecar/.env` (gitignored). Only the first three are new work for V3; the rest
already exist and are listed so you can see the whole surface in one place.

| Env var | Consumed by | Purpose |
| --- | --- | --- |
| `ARC_USDC_ADDRESS` | `sidecar/src/seller.ts:38`, `sidecar/src/capture-v1-fixture.ts`, and the Go side via `deployments/arc-testnet.json` `contracts.usdc.addressEnv` (`daemon/internal/chaincfg`) | The EIP-712 domain's `verifyingContract`. Set it to `0x3600000000000000000000000000000000000000`. A placeholder here means the signature authorizes nothing. |
| `SELLER_ADDRESS` | `sidecar/src/seller.ts:42`, `sidecar/src/capture-v1-fixture.ts` | The seller's `payTo`, and the payee you allowlist in Step 5. Must not stay `0x..dEaD`. |
| `V1_FIXTURE_RESOURCE` | `sidecar/src/capture-v1-fixture.ts` only | Full http(s) URL of the 0.04 USDC resource the fixture capture buys. |
| `TREASURY_PRIVATE_KEY` | `sidecar/src/buyer.ts:391` (`loadSigner`), `daemon/cmd/snapfall/main.go:848`, and the default `--key-env` of `daemon/cmd/chainops` / `--operator-key-env` of `daemon/cmd/seed-demo` | The signer. Testnet only, never a funded mainnet key. |
| `ARC_CHAIN_ID` | `sidecar/src/seller.ts:34`, `sidecar/src/capture-v1-fixture.ts` | Defaults to 5042002. The capture script refuses any other value. |
| `ARC_TESTNET_RPC` | `deployments/arc-testnet.json` `network.rpcUrlEnv` (daemon), `dashboard/app/api/float/route.ts:16`, `dashboard/app/api/jobs/[jobId]/route.ts:23` | Overrides the public RPC. Use a private endpoint on recording day: the public one 429s while chasing head (`docs/RUNBOOK.md`). |
| `PAID_API_PORT` | `sidecar/src/seller.ts:31` (default 4021) | Must agree with `SNAPFALL_SELLER_BASE_URL` or every purchase fails pre-sign with `RESOURCE_NOT_FOUND`. |
| `SNAPFALL_SELLER_BASE_URL` | `daemon/internal/discovery/discovery.go:85` (loopback-only by design) | The origin the daemon's discovery stand-in builds resource URLs on. Default `http://127.0.0.1:4021`. |
| `SNAPFALL_TREASURY_ADDRESS`, `SNAPFALL_CUSTOMER_ADDRESS` | `deployments/arc-testnet.json` `fundedWallets` via `scripts/testnet-ops` | Balance checks and guarded funding. |
| `SNAPFALL_FUNDER_ACCOUNT` | `daemon/cmd/testnet-ops/main.go:23` | Foundry keystore **account name**, not a key. |
| `CIRCLE_API_KEY` | nothing yet | Placeholder in `sidecar/.env.example`. Zero code consumers today. |
| `SIDECAR_AUTH_TOKEN`, `H2_APPROVAL_SECRET` | `sidecar/src/service.ts:44,45` | V4's H3 service, not V3. Both are refused if unset or under 32 bytes. |

## Step 7 (human): P2, prove the policy blocks an over-limit payment

The refusal must be Circle's. Our inner guard would refuse first if you route through our code,
which proves nothing about the wallet layer, so drive the wallet directly:

1. Attempt a payment of **1.00 USDC** (above the 0.10 per-transaction limit from Step 5) from the
   Agent Wallet to the allowlisted payee, using the Circle CLI or console.
2. Capture the refusal verbatim: the error code, the message, and the rule it cites.
3. Repeat with **0.04 USDC** to the same payee and confirm it is permitted. A policy that blocks
   everything is not a demonstration.

**P2 is the pair of outputs from 1 and 3.** Paste both into the V3 PR body, in the same block as
P1. Do not paraphrase them.

## Step 8: capture the V1 fixture and close V1

V1's done-when is "a real four-cent purchase completes on testnet and the raw request/response
pair is committed as a fixture." That fixture is
`sidecar/fixtures/v1-circle-payment.json`, and `.github/workflows/ci.yml` starts verifying it
against AT-18 the moment the file exists. It is produced by exactly one command, never by hand.

```bash
# 1. Terminal A: the paid demo API, with real money settings.
cd sidecar
export ARC_CHAIN_ID=5042002
export ARC_USDC_ADDRESS=0x3600000000000000000000000000000000000000
export SELLER_ADDRESS=0x...            # the payee you allowlisted in Step 5
export PAID_API_PORT=4021
npm run seller

# 2. Terminal B: same three money settings, plus the signer and the resource.
cd sidecar
export ARC_CHAIN_ID=5042002
export ARC_USDC_ADDRESS=0x3600000000000000000000000000000000000000
export SELLER_ADDRESS=0x...            # identical to Terminal A
export TREASURY_PRIVATE_KEY=0x...      # testnet only
export V1_FIXTURE_RESOURCE=http://127.0.0.1:4021/v1/company-profile
npm run capture:v1-fixture

# 3. Verify the way CI will, then commit.
npm run verify:circle-facilitator-fixture -- fixtures/v1-circle-payment.json
git add fixtures/v1-circle-payment.json
```

`capture:v1-fixture` runs the shipped `purchase()` path from `sidecar/src/buyer.ts` and records
raw HTTP by wrapping `fetch` for the duration of the call, so what lands in the fixture is what
crossed the wire. It refuses to write anything if:

- any required env var is unset (it lists every problem at once, before loading the key),
- `ARC_USDC_ADDRESS` or `SELLER_ADDRESS` is still a `seller.ts` placeholder,
- `ARC_CHAIN_ID` is not 5042002,
- the live challenge is not exactly 0.04 USDC to the allowlisted payee in real USDC (the intent
  pins all four approved terms, so `buyer.ts` refuses pre-signature),
- **the receipt's `settlement` is still `NOT_BROADCAST`**, or
- a fixture already exists (committed evidence is never silently overwritten).

### The one thing that will stop you, and it is supposed to

**Updated 2 Aug 2026: the broadcast is now wired** (`sidecar/src/facilitator.ts`). The seller
calls Circle's `verify` then `settle` and records the returned transaction hash on the receipt.
It stays dormant until `CIRCLE_API_KEY` is set, so with no key the behaviour below is unchanged
and `capture:v1-fixture` still refuses. **The client has never run against the live service** --
its wire contract is written from Circle's documented x402 facilitator interface and is an
assumption until Step 8. Treat the first live capture as the test of it.

Three refusals now guard the fixture where there was one, and the description below is the
corrected one -- my first version of this paragraph misdescribed them:

- **Refusal 5** rejects a known dry-run marker (`NOT_BROADCAST`, `PENDING`, `DRY_RUN`, ...).
- **Refusal 6** rejects a settlement whose provenance is missing, or which came from a
  facilitator other than Circle's documented endpoints. The fixture used to hardcode those URLs
  and therefore asserted a claim it had never observed.
- **Refusal 7** rejects a settlement that is not a 32-byte transaction hash. This was previously
  only a printed `note` and the fixture was still written, which meant any string outside the
  dry-run set -- `settled`, `ok`, anything -- became committed proof of a payment. Refusal 5 only
  covers markers we had already thought of, so without refusal 7 the gate's coverage depended on
  a fabricator picking a word from our list.

Until a key exists, `sidecar/src/seller.ts` reports `settlement: 'NOT_BROADCAST'` and says why in
its header: it verifies the buyer's authorization but does not submit `transferWithAuthorization`
on chain, because that is the facilitator's job. In that state `capture:v1-fixture` parks the raw
evidence at
`sidecar/fixtures/v1-circle-payment.json.pending_chain`, print a stop message, and exit 1. **No
fixture is written and V1 stays open.**

That is the correct behavior, not a bug to route around. The `.pending_chain` sibling is a local
artifact, so do not commit it: it proves the loop, not the payment. Verified on 25 Jul 2026 by
running the capture against a locally configured seller: both legs were recorded, the AT-18
contract fields validated, and the run stopped at the settlement check.

So the ordering is: Steps 1 to 7, then wire the facilitator broadcast, then Step 8. V3 does not
depend on Step 8, but Step 8 depends on V3.

## What V3 does not prove

State this in the PR rather than letting a reader over-read the evidence:

- A wallet-layer policy refusal proves the **outer** guard. AT-03 and AT-05 already prove the
  inner guard in `npm run demo:loop`. They are independent layers and the claim is that either
  one alone blocks a bad instruction (`docs/PRD.md` §11).
- Listing the wallet from the CLI proves the control plane reaches our wallet. It does not prove
  any x402 purchase settled. That is V1, and Step 8 is the only thing that closes it.
- `Circle Agent Marketplace` discovery remains roadmap. The shipped code matches a local
  stand-in catalog behind a `Catalog` seam (`docs/PRD.md` §6.7). Nothing in V3 changes that, and
  the deck should keep saying so.

## Definition of done, as a checklist

- [ ] Circle developer account created, terms accepted, testnet API key held locally only.
- [ ] `circle --version` prints a version.
- [ ] P1 captured: CLI output listing our Agent Wallet.
- [ ] Agent Wallet funded and the Gateway testnet deposit made.
- [ ] Spend policy configured: 0.10 USDC per-transaction limit, payee allowlist, USDC, Arc testnet.
- [ ] P2 captured: 1.00 USDC refused at the wallet layer with the rule cited, and 0.04 USDC permitted.
- [ ] `sidecar/.env` holds the real `ARC_USDC_ADDRESS` and `SELLER_ADDRESS`, no placeholders.
- [ ] P1 and P2 pasted verbatim into the V3 PR body.
