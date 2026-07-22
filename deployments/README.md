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

The ABI files under `contracts/abi/indexer/` deliberately contain the event surface needed by
H1. Transaction-writing callers must use compiler-generated full ABIs from the frozen contracts.
