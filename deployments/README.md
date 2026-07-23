# Deployment configuration

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

The ABI files under `contracts/abi/indexer/` deliberately contain the event surface needed by
H1. Transaction-writing callers must use compiler-generated full ABIs from the frozen contracts.

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
