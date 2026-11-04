# Circle treasury wallet (operator signer without a raw key)

Part of [CIRCLE-INTEGRATIONS.md](CIRCLE-INTEGRATIONS.md), which covers the customer's passkey
wallet, CCTP and Gateway as well.

The daemon's treasury lane signs `requestAdvance`. Until now it could only do that with
`TREASURY_PRIVATE_KEY`, a raw secp256k1 key sitting in a shell file. This lane signs the same
calls through a Circle **Developer-Controlled Wallet** instead. The key is an MPC share Circle
holds, and the daemon holds an API key plus the entity secret that authorizes each request.

Code: `daemon/internal/circlewallet` (chosen in `daemon/cmd/snapfall/treasury.go`) and
`sidecar/src/circle-wallet.ts` (chosen in `loadSigner`, `sidecar/src/buyer.ts`). Both use the same rule.

## What this is and is not

- **It is developer custody.** Whoever holds `CIRCLE_API_KEY` **and** `CIRCLE_ENTITY_SECRET`
  can sign anything this wallet can. The raw key in a file is gone; a signing authority on a
  server is not. Do not call it non-custodial anywhere.
- **It is for the operator's own treasury only.** Customer funds never go through it. The
  customer lane (`SNAPFALL_CUSTOMER_PRIVATE_KEY`, demo-custodial) is a separate problem, and the
  answer there is the customer's own wallet, not a Circle wallet the operator controls.
- **It is not the admin key.** The deployer/admin can pause, set caps and refund. That key
  belongs on a hardware wallet or a multisig, never behind a server API key.
- **The chain still decides.** Circle signs and broadcasts. Once Circle reports a transaction
  hash, the daemon reads the receipt from its own RPC and the receipt's status decides success
  vs reverted, the same rule as the raw-key lane.
- **EOA wallets only.** For a smart-contract account, Circle's reported hash is the bundler
  transaction, which can succeed while the inner call reverted. The lane refuses an SCA wallet
  at boot rather than mis-reporting a revert as success.
- **The sidecar uses the same wallet.** The x402 buyer (`sidecar/src/buyer.ts`) asks Circle to
  sign each EIP-3009 authorization (`POST /v1/w3s/developer/sign/typedData`) and recovers the
  signature locally before using it; one that does not recover to the wallet is refused. The
  ops scripts `x402-simulate.ts` and `x402-broadcast.ts` still read `TREASURY_PRIVATE_KEY`, so
  remove that key from the shell file once you no longer run them.
- **The relayer can be a Circle wallet too.** With `SNAPFALL_SELF_FACILITATOR=1`, set
  `CIRCLE_BROADCASTER_WALLET_ID` to a second Circle EOA wallet holding only gas, and the seller
  submits `transferWithAuthorization` through `contractExecution` instead of
  `BROADCASTER_PRIVATE_KEY`. Circle's hash is not the verdict: the receipt from our own RPC
  decides settled vs reverted. Circle reporting `DENIED`, `CANCELLED`, a 4xx, or `FAILED` with no
  hash is "not broadcast" (rejected, nothing moved); anything else that goes wrong after the
  request leaves, including a timeout, is `unknown` (may still land; reconcile, never release).
  It can be the treasury wallet's id, but a separate wallet keeps the gas float apart from the
  money that pays for work.

## Selection rule

| Environment | Treasury signer |
| --- | --- |
| `CIRCLE_TREASURY_WALLET_ID` unset or empty | `TREASURY_PRIVATE_KEY`, unchanged |
| `CIRCLE_TREASURY_WALLET_ID` set | the Circle wallet; needs `CIRCLE_API_KEY` and `CIRCLE_ENTITY_SECRET` |

If the Circle lane is configured but fails to open (missing variable, wrong network, frozen
wallet, SCA, Circle unreachable), **no treasury lane is wired** and advances stop at
`advance.pending_chain`. It never falls back to the raw key.

Boot checks, all fail-closed: the API key has Circle's `PREFIX:ID:SECRET` shape, the entity
secret is 32 bytes of hex, the wallet id is a UUID, and the wallet is `LIVE`, `DEVELOPER`
custody, `EOA`, and on the deployment's network (`ARC-TESTNET` for 5042002, `ARC` for 5042).

## The operator address changes

The Circle wallet has its own address. On chain that is a **new operator**:

- FloatPool keys the advance rate by org address, so the new address starts at the base rate,
  with none of the old address's accepted-job history.
- `startWork`, `recordExpense` and `submitDelivery` require `msg.sender == j.operator`. Jobs
  for this lane must be created with the Circle address as `operator` (`createJob` accepts the
  admin or the operator as sender), and those three calls must be signed by the Circle wallet:
  `chainops -signer circle start-work <jobid32>` and so on.
- Set `SNAPFALL_TREASURY_ADDRESS` to the Circle address. The owner API reads the org's rate and
  advances from it, and the daemon warns at boot if it names a different address from the
  signer.
- `seed-demo` and `scripts/spine_run` still expect raw keys and are unchanged.

## Setup (human steps)

These need your Circle account and your secrets, so they are yours to do:

1. In the Circle developer console, create an API key, generate and register an entity secret,
   and **store the recovery file somewhere safe**. Losing both the entity secret and the
   recovery file loses these wallets for good.
2. Create a wallet set, then an **EOA** wallet on `ARC-TESTNET` (later `ARC` for mainnet).
   Note its wallet id and address.
3. Send the wallet some testnet USDC for gas. On Arc, USDC is the gas token.
4. Put the three variables in the shell file you already source (never in the repo):

   ```
   export CIRCLE_API_KEY=...
   export CIRCLE_ENTITY_SECRET=...
   export CIRCLE_TREASURY_WALLET_ID=...
   export SNAPFALL_TREASURY_ADDRESS=<the Circle wallet address>
   ```

5. Start the daemon with the deployment config as usual. The boot log should show
   `treasury chain lane wired ... via="circle developer-controlled wallet"`.

## Behaviour on failure

| Circle reports | Lane result |
| --- | --- |
| `CONFIRMED` / `COMPLETE` with a hash | receipt from our RPC decides: success or `Reverted` |
| `FAILED` with a hash | receipt decides (usually a mined revert) |
| `FAILED` without a hash | submission failure, Circle's `errorReason` passed on; nothing mined |
| `DENIED` / `CANCELLED` | submission failure; a cancelled transaction's hash is never treated as landed |
| still pending after 90s (`STUCK`, `SENT`) | error handed to the restart oracle, as a missing receipt is today |

Each request uses a fresh idempotency key and a freshly encrypted entity secret (Circle rejects
a reused ciphertext). Duplicate advances are prevented upstream by the write-ahead claim and
the chain oracle (SC-FP-003 allows one advance per job), not by Circle.
