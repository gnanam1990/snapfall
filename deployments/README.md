# Deployment configuration

> **The addresses committed in both files predate the operator allowlist** (FloatPool
> `allowedOperator`, Nov 2026). Those contracts cannot be patched, and any address can borrow
> from those pools and leave the LPs the write-off. Put no capital into them. Retire and
> redeploy them by following
> [docs/MAINNET.md, "Live pools deployed before the operator allowlist"](../docs/MAINNET.md#live-pools-deployed-before-the-operator-allowlist)
> before updating these files.

`arc-mainnet.json` is the same artifact for Arc **mainnet** (chain 5042, live 16 Sep 2026).
It commits the 12 Nov 2026 mainnet set (`docs/addresses.md` §9). The env vars it names still
override each address. Read `docs/MAINNET.md` before deploying — the capital caps are mandatory
there, and the RPC hostname is `rpc.mainnet.arc.io` (`rpc.mainnet.arc.network` does not resolve).

`arc-testnet.json` is the machine-readable A1 handoff used by the indexer and, later, Funding,
Billing and the dashboard. It contains no private keys. Addresses that do not exist until deploy
time are resolved from named environment variables and fail closed when missing or malformed.

Required runtime variables:

```text
SNAPFALL_JOB_VAULT_ADDRESS
SNAPFALL_FLOAT_POOL_ADDRESS
SNAPFALL_AUDIT_ANCHOR_ADDRESS
ARC_USDC_ADDRESS
SNAPFALL_TREASURY_ADDRESS
SNAPFALL_CUSTOMER_ADDRESS
```

`ARC_TESTNET_RPC` optionally overrides the canonical public RPC. For a post-genesis deployment,
`SNAPFALL_DEPLOYMENT_BLOCK` is required operationally: set it to the deployment transaction's
block before the first indexer run so catch-up does not start at genesis. Leave it at `0` only
when the indexed contracts genuinely begin at genesis.

`network.explorerUrl` is the canonical base for A5 financial-row links. Deployment loading
validates it as an absolute HTTP(S) URL; H2 consumers append only validated `/tx/{hash}` and
`/address/{address}` paths through `daemon/internal/explorer`.

The ABI files under `contracts/abi/indexer/` are a hand-curated subset of each contract's events:
the eight H1 events, `AuditAnchor.JobAnchored`, and FloatPool's operator-allowlist events
(`OperatorAllowed`, `OperatorGrantQueued`, `OperatorGrantCancelled`). Each entry is copied from
the compiler's artifact (`forge inspect <Contract> abi --json`), and
`contracts/test/IndexerAbi.t.sol` fails if an entry drifts from the compiled contract or the set
of events changes. Transaction-writing callers must use compiler-generated full ABIs.

## Deployed — Arc testnet, 15 Nov 2026 (block 72420303) — current

The audited source with capital controls, deployed with `scripts/redeploy-testnet` from the
fresh deployer `0x158771bCEF1F91Af732CeC9117D6D14cFb958Bf8`, which is admin of both contracts
and the one allowed operator. Caps: `maxTotalExposure` 50.00, `maxAdvance` 10.00,
`maxJobPayment` 25.00 USDC. The services and dashboard read this set from `arc-testnet.json`.

| Contract | Address | Deploy tx block |
|---|---|---|
| AuditAnchor | `0x929bA0CFE0EE582949EEB0aA5c512f1EaB421519` | 72420303 |
| JobVault | `0x449A7308A10312b9bb0E21B62064a8080eb10bD1` | 72420304 |
| FloatPool | `0x4780C2E47a7eE3f9F88Bd83aace58fb32c4586bE` | 72420304 |

Verified on-chain after the broadcast: code at all three addresses, `JobVault.floatPool()` and
`FloatPool.jobVault()` point at each other, both admins are the deployer, the three caps read
back as set, `allowedOperator(deployer)` is true, neither contract is paused, `usdc()` is
`0x3600…0000`.

## Superseded — Arc testnet, 23 Jul 2026 (block 53268443)

The original set, before the capital controls and the security-audit fixes. Its history stays
on chain and its links below still resolve; nothing reads it any more.

| Contract | Address | Deploy tx block |
|---|---|---|
| AuditAnchor | `0x7CDBF8a6D33d4c4C55fb94447E7E90905b3672c6` | 53268443 |
| JobVault | `0xF3830D7C3B8ca873bB0b277c0e179999e3d52681` | 53268443 |
| FloatPool | `0xde9F58A997Cf7A3258D09A797Eb5546877dc86E5` | 53268445 |

Explorer: [AuditAnchor](https://testnet.arcscan.app/address/0x7CDBF8a6D33d4c4C55fb94447E7E90905b3672c6) ·
[JobVault](https://testnet.arcscan.app/address/0xF3830D7C3B8ca873bB0b277c0e179999e3d52681) ·
[FloatPool](https://testnet.arcscan.app/address/0xde9F58A997Cf7A3258D09A797Eb5546877dc86E5)

Verified on-chain post-deploy (not trusted from script output): code present at all three
addresses; `JobVault.floatPool()` and `FloatPool.jobVault()` point at each other; both
contracts' `usdc()` is the canonical predeploy `0x3600…0000` (symbol/decimals asserted
against the live RPC pre-deploy). Total deploy gas: 0.0791036 USDC (forge estimated
0.1856 — ~2.3x conservative). Addresses are committed in `arc-testnet.json` (`address`
fields, env vars still override); `startBlock` is the deployment block so indexer
catch-up never starts at genesis.

## Arc EVM differences that matter here (docs.arc.io/arc/references/evm-differences, read 23 Jul 2026)

- **Non-decreasing timestamps** — verbatim: *"Block timestamps are non-decreasing, not strictly
  increasing."* Sub-second blocks may share identical timestamps. The frozen contracts use
  `block.timestamp` deadline logic; on Arc, two deadline comparisons in adjacent blocks can see
  the SAME timestamp — deadline expiry is therefore not guaranteed to advance between blocks,
  and anything ordering-sensitive must use block numbers. The contracts are frozen (ADR-014),
  so this is recorded as an operational caveat, not a code change.
- **Dual USDC decimals** — native gas balance (`addr.balance`) is 18-decimal; the ERC-20
  interface at `0x3600000000000000000000000000000000000000` is 6-decimal. Never mix raw values;
  gas reporting below uses the native 18-decimal figure.
- **Instant finality** — transactions are final on inclusion, no reorg risk after one
  confirmation. This is why `confirmationDepth` is 0 in `arc-testnet.json`.
- **No EIP-1559 burn** — base + priority fees go to the block beneficiary; fee estimates that
  assume burn dynamics overstate cost.
- **No blob (type-3) transactions** — the mempool rejects them; irrelevant to `forge script`
  defaults but recorded.
- **Value-transfer restriction** — native sends can revert when targeting the zero address,
  blocklisted, or self-destructed accounts even with sufficient balance; the deploy script
  sends no endowments, so no impact here.
