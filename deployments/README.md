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

## Deployed — Arc testnet, 23 Jul 2026 (block 53268443)

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
