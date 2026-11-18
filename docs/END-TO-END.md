# Snapfall end to end

This page covers who uses Snapfall, what each of them does, where each piece runs, and how a job
moves from a customer's payment to the operator's profit. It also records what works today and
what is still missing, as of **16 Nov 2026**. Deeper references are linked at the end; this page
does not repeat them.

## In one paragraph

A customer's payment is escrowed in **JobVault**. Once the job is funded, **FloatPool** advances
working capital against that receivable to the operator: the *snap*. The operator's agents spend
the advance on what the job needs and deliver. When the customer accepts, one transaction runs the
*fall*: it repays the pool first (principal plus a 2% fee), then sends what is left to the
operator. Every accepted job raises the operator's on-chain advance rate. The owner approves or
refuses agent purchases the policy does not auto-approve. The dashboard shows every movement,
each linked to its transaction.

## Who uses it

| Who | What they want | Where they go | Credential |
|---|---|---|---|
| **Visitor** (judge, investor, would-be LP) | Check that the business is real | Overview, Jobs, Activity, Capital pool, Audit | None. Everything is public chain data. |
| **Owner / operator** (one person) | Know where the money is, and decide on purchases | Approvals, Workforce, job detail, Settings | Owner secret (`SNAPFALL_DASHBOARD_OWNER_SECRET`); a session lasts 12 hours |
| **Customer** | Pay for one job, accept the delivery, get a receipt | `/pay/<vaultJobId>`, `/portal/<jobId>?token=act_…` | Their own passkey wallet, plus a per-job magic link from the owner |
| **Liquidity provider (LP)** | Earn the pool's fee | The pool contract (the Capital pool page shows history, read-only) | Their wallet, once the admin allowlists it |
| **Admin** (today the owner, wallet `0x0f09…eb2fb`) | Keep the contracts safe | `cast` against the contracts (see [`MAINNET.md`](MAINNET.md)) | The admin keystore |

There is one owner per deployment. Snapfall is not a multi-user signup product: each operator
runs their own daemon. A customer never sees the owner's internals or another customer's job.
That is a product rule ([`PRD.md`](PRD.md)).

## Where it runs

| Surface | Network | Host | Daemon | Purpose |
|---|---|---|---|---|
| <https://app.snapfall.lol> | Arc testnet (5042002) | Render web service `snapfall-dashboard` | Render private service `snapfall-daemon` (keyless) | Full product on testnet, including owner views |
| <https://www.snapfall.lol> | Arc mainnet (5042) | Vercel project `snapfall` | none, by design | Public, read-only mainnet view. This project gets no owner credentials ([`HOSTING.md`](HOSTING.md)) |
| <https://snapfall-dashboard-mainnet.onrender.com> | Arc mainnet (5042) | Render web service `snapfall-dashboard-mainnet` | Render private service `snapfall-daemon-mainnet` (keyless, `daemon/Dockerfile.chain`) | Mainnet with owner views. Meant to take over `snapfall.lol` once its DNS moves from Vercel |

A daemon is only ever reachable on Render's private network. Each dashboard talks to it at
`http://<daemon>:4010/api/v1`, and both sides share `SNAPFALL_OWNER_TOKEN` through an environment
group (`snapfall-owner` for testnet, `snapfall-owner-mainnet` for mainnet).

These are the environment variables, by name; values live only on the host.

| Service | Variables |
|---|---|
| mainnet daemon | `PORT=4010`, `SNAPFALL_DEPLOYMENT=arc-mainnet`, `SNAPFALL_TREASURY_ADDRESS`, `SNAPFALL_CUSTOMER_ADDRESS`, `SNAPFALL_OWNER_ALLOWED_HOSTS=snapfall-daemon-mainnet`, plus from the group: `SNAPFALL_OWNER_TOKEN`, `ARC_MAINNET_RPC` |
| mainnet dashboard | `SNAPFALL_NETWORK=arc-mainnet` (read at build time), `SNAPFALL_TREASURY_ADDRESS`, `SNAPFALL_OWNER_API_URL`, `SNAPFALL_DASHBOARD_OWNER_SECRET`, plus from the group: `SNAPFALL_OWNER_TOKEN`, `ARC_MAINNET_RPC` |
| Vercel (mainnet, public) | `SNAPFALL_NETWORK=arc-mainnet`, `ARC_MAINNET_RPC`, `SNAPFALL_TREASURY_ADDRESS`, `NEXT_PUBLIC_CIRCLE_CLIENT_KEY` |

Contracts are listed in [`deployments/`](../deployments) and [`addresses.md`](addresses.md):

| | JobVault | FloatPool | AuditAnchor |
|---|---|---|---|
| testnet (redeployed 16 Nov, capital controls) | `0x449A7308A10312b9bb0E21B62064a8080eb10bD1` | `0x4780C2E47a7eE3f9F88Bd83aace58fb32c4586bE` | `0x929bA0CFE0EE582949EEB0aA5c512f1EaB421519` |
| mainnet (deployed 12 Nov, **paused**, the set from before the security-review fixes) | `0x67FeD7B5eaA966c4cc2738d1530f230B31B6f372` | `0xD2FaB9CD08a73B476e8DC81Afb7b57564dEef08f` | `0xe8886A39158C7CC504F31789513b926704CCaB4D` |

USDC is `0x3600…0000` on both networks. It is also the gas token.

## The life of one job

The numbers are the testnet demo's: a 25.00 USDC job at a 50% advance rate.

| # | Step | Who acts | What happens on chain | Where you see it |
|---|---|---|---|---|
| 1 | **Fund** | Operator creates the job, then the customer funds it from `/pay/<vaultJobId>` | The operator creates the job naming the customer's address (`createJob`). On testnet this is `scripts/seed_demo`; there is no dashboard button for it. The customer approves USDC and calls `JobVault.fund`. Only the named customer can fund (SC-JV-001). | Jobs: status `Funded` |
| 2 | **Snap** | The daemon proposes the advance (owner API `POST /api/v1/jobs/{id}/advance`; `spine_run` does this), and the owner approves it in Approvals | The daemon's treasury signer calls `FloatPool.requestAdvance`. A 12.50 advance arrives with a 0.25 fee. It is capped at 10% of pool TVL per org and 80% utilisation overall. | Overview, Capital pool, job detail |
| 3 | **Spend** | Agents. Policy auto-approves small buys. | Agents pay sellers over x402 through the sidecar, for example a 0.04 data purchase. | Approvals history, Activity |
| 4 | **Escalate** | Owner | A buy above policy (4.00) waits in Approvals. The owner can approve, refuse, or ask for a cheaper alternative. A refusal with a cost reason makes the worker look for a cheaper source. | Approvals |
| 5 | **Alternative** | Agents | The worker buys the cheaper source (0.06). | Activity |
| 6 | **Deliver** | Agents, then the QA gate | The job reaches `delivery_ready` only after the QA worker passes the draft. The delivery hash goes on chain (`submitDelivery`). | job detail |
| 7 | **Accept** | Customer, from `/portal/<jobId>?token=act_…` | `JobVault.acceptDelivery` runs the waterfall in one transaction: the pool is repaid 12.75 first, then the operator gets the rest (12.25). | Overview cascade, Audit, Activity |
| 8 | **Rate** | Nobody. The contract does it. | The org's advance rate rises (+5% per accepted job, capped at 85%), so the next job unlocks more. | Capital pool |

If a job is not delivered, it is refunded (`JobVault.refund`). The customer gets the full payment
back first, then the pool absorbs the loss through its first-loss reserve.

## Each role, step by step

### Visitor

1. Open the site. **Overview** shows what customers paid, what the pool was repaid and what the
   operator kept, as a 3D cascade you can drag to turn. Below it are the pool, the advance rate
   and pool fees.
2. **Jobs** and **Activity** list every job and every money event, each with its transaction.
3. **Audit** lists the capital controls (caps, pause state, allowlists) read live from the
   contracts, and says what has not been proven.

### Owner

1. **Sign in.** Copy `SNAPFALL_DASHBOARD_OWNER_SECRET` from the dashboard service's Environment
   page on Render. Then open **Approvals** (or **Settings**) and paste it. Without a connected
   daemon the owner cards say *No daemon connected*.
2. **Approvals.** Every purchase or advance waiting for you shows its amount, the seller and why
   it was asked for. If you approve, the purchase proceeds. If you refuse, the worker is told
   your reason. If you ask for an alternative and give a cost reason, the worker looks for a
   cheaper source.
3. **Job detail.** Once a job is `delivery_ready`, mint the customer's accept link and send it
   to them. The link is shown once, and minting it again rotates it.
4. **Workforce.** See which agents are hired, their budgets, and whether each one may sign or
   borrow.

### Customer

1. **Get a wallet.** Open `/pay/<anything>` and sign in with a passkey. The page shows your
   wallet address, and the operator names that address when creating your job. This uses
   Circle's passkey wallets and needs a Circle client key for the network: `TEST_…` on testnet,
   `LIVE_…` on mainnet.
2. **Pay.** Open `/pay/<vaultJobId>`, sign in, and fund the escrow. A stranger who opens the
   link is told, truthfully, that the escrow is not theirs.
3. **Accept.** Open the magic link the owner sent (`/portal/<jobId>?token=act_…`). It shows the
   job's status, an **Accept** button and then the receipt. Accepting triggers the waterfall.

### Liquidity provider

On mainnet the pool only accepts allowlisted depositors.

1. The admin allowlists the depositor:
   ```bash
   cast send <FloatPool> "setDepositorAllowed(address,bool)" <lp-address> true --account <admin-keystore> --rpc-url <rpc>
   ```
2. The LP approves USDC to the pool:
   ```bash
   cast send 0x3600000000000000000000000000000000000000 "approve(address,uint256)" <FloatPool> <micro-usdc> --account <lp-keystore> --rpc-url <rpc>
   ```
3. The LP deposits:
   ```bash
   cast send <FloatPool> "deposit(uint256,address)" <micro-usdc> <lp-address> --account <lp-keystore> --rpc-url <rpc>
   ```
4. To withdraw, the LP calls `withdraw(uint256 assets, address receiver, address owner)`. The
   Capital pool page lists every deposit and withdrawal.

### Admin

Pausing, caps, allowlists, the operator grant timelock and the admin handover are all in
[`MAINNET.md`](MAINNET.md). Two rules matter in practice:

- **Raising a cap and granting an operator are queued for 2 days.** Pausing and revoking are
  immediate.
- **Admin handover is two steps.** The current admin calls `transferAdmin(new)`, then the *new*
  address calls `acceptAdmin()`. On mainnet this was completed on 16 Nov 2026: admin is now
  `0x0f094eec6B569c3f33033102ad3ce33EAbFeb2fB` on JobVault (tx `0xf668b04b…0cca`, block
  31,507,782) and FloatPool (tx `0x7362bf9a…0cae`).

## Running the loop

### Testnet: the full loop is proven

`scripts/spine_run` drives all eight steps against real services and logs PASS, UNVERIFIED or
FAIL per beat ([`SPINE-RUNS.md`](SPINE-RUNS.md)). Two PASS runs on 16 Nov 2026 are recorded in
[`spine-runs/2026-11-16-first-pass.md`](spine-runs/2026-11-16-first-pass.md), along with their
caveats.

```bash
./scripts/spine_run --dry-run
```

```bash
./scripts/spine_run
```

### Mainnet: one self-test job, by hand

On 16 Nov 2026 one 0.50 USDC job ran between the owner's own two wallets, to prove the vault path
on mainnet. It was not a customer and not a sale. The customer was `0x0f09…` and the operator
`0xb6e5…`. It took no advance, because the pool is paused and empty, so the operator kept the
whole 0.50. JobVault was unpaused only for the run and paused again afterwards.

| Step | Call | Signer | Tx |
|---|---|---|---|
| unpause | `JobVault.setPaused(false)` | admin | `0xafafe309…94ba` |
| create | `createJob(id, customer, operator, 500000, 0, terms, deadline)` | operator | `0x4aef79dc…6037` |
| approve | `USDC.approve(JobVault, 500000)` | customer | `0xe9132ab7…3978` |
| fund | `fund(id)` | customer | `0xc0185083…9a55` |
| work | `startWork(id)` | operator | `0x2946840d…c5bf` |
| deliver | `submitDelivery(id, hash)` | operator | `0x85f9f4c5…adaf` |
| accept | `acceptDelivery(id)`, the settlement | customer | `0x57cd0e9c…5033` |
| re-pause | `JobVault.setPaused(true)` | admin | `0x9aa96339…bcb4` |

Job id: `0x1693dd7b88579cd34d9f57675c48ad40f999285b3c1f4c53184faf679eaa9bc2`. Status on chain:
`Accepted`. Both mainnet dashboards show it.

## What works today

| Capability | Testnet | Mainnet |
|---|---|---|
| Public chain views (jobs, activity, pool, audit, cascade) | yes | yes |
| Owner sign-in, approvals inbox, workforce | yes | yes, on the Render dashboard. It is keyless, so nothing reaches the chain. |
| Advance (snap) signed by the daemon | yes (raw testnet key) | **no**: no treasury signer. It must come from a Circle developer-controlled wallet, never a raw key on the host. |
| Agent purchases over x402 | yes (self-facilitated) | **no**: no sidecar, and no mainnet settlement path configured |
| Customer pays with a passkey wallet | yes (`TEST_` client key) | **no**: needs a `LIVE_` Circle client key and the `snapfall.lol` passkey domain |
| Settlement waterfall | yes, with a pool repayment | vault path proven with no advance (the self-test job above) |
| Pool deposits | yes | **no**: the pool is paused and empty |
| Contracts with the operator allowlist fix | yes (16 Nov set) | **no**: the live set is the earlier one, so it stays paused |

## Going live on mainnet, in order

1. **Circle LIVE setup (owner).** You need a live API key and entity secret, mainnet wallets via
   `circle-setup -mainnet`, a `LIVE_` client key with the passkey domain `snapfall.lol`, and a
   paid Gas Station. See [`CIRCLE-TREASURY-WALLET.md`](CIRCLE-TREASURY-WALLET.md) and
   [`V3-CIRCLE-SETUP.md`](V3-CIRCLE-SETUP.md).
2. **Redeploy the fixed contracts** on mainnet and point `deployments/arc-mainnet.json` at them.
   Follow [`MAINNET.md`](MAINNET.md), "Live pools deployed before the operator allowlist". The
   new treasury is the Circle wallet from step 1. Then complete the admin handover on the new set.
3. **Allowlist the operator.** It is queued for 2 days, then executed. **Allowlist the first LP**
   and deposit, inside the caps.
4. **Wire the daemon's signers** through Circle (no raw key on Render), and run the sidecar with
   a mainnet settlement path.
5. **Set the `LIVE_` client key** on the mainnet dashboard, so customers can pay.
6. **Move `snapfall.lol`** from Vercel to the Render mainnet dashboard (Hostinger DNS). Keep
   Vercel as the read-only fallback.

## When something looks wrong

| You see | It means | Do |
|---|---|---|
| *No daemon connected* | This dashboard has no `SNAPFALL_OWNER_API_URL`. This is expected on Vercel. | Use the Render dashboard for owner views. |
| History says `HTTP 429: rate limit exceeded` | The dashboard is on the public RPC | Set `ARC_MAINNET_RPC` (or `ARC_TESTNET_RPC`) to a private endpoint |
| `NotPendingAdmin` from `acceptAdmin()` | It was sent from the wrong wallet | Send it from the address in `pendingAdmin()` |
| `EnforcedPause` | The contract is paused, as mainnet is on purpose | An admin calls `setPaused(false)`, but only for a deliberate run |
| `cast wallet import` says `invalid string length` | The paste picked up extra characters | The key must be exactly 64 hex characters. Import from the clipboard with whitespace stripped. |
| Advance or settlement shows `*.pending_chain` | That chain lane has no signer | Expected on a keyless daemon. The startup log names the missing one. |

## Related

- [`PRD.md`](PRD.md): the product definition
- [`architecture.md`](architecture.md): the split between what runs locally and on chain, and the waterfall
- [`HOSTING.md`](HOSTING.md): every host and its variables
- [`MAINNET.md`](MAINNET.md): capital controls, deployment, emergency stop
- [`SPINE-RUNS.md`](SPINE-RUNS.md): the full-loop harness
- [`RUNBOOK.md`](RUNBOOK.md): day-to-day operations and known traps
- [`addresses.md`](addresses.md): every deployment, read back from chain
