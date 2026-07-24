# scripts

- `./scripts/testnet-ops` — checks every funded wallet from
  `deployments/arc-testnet.json`. Add `--fund --funder-account <name>` to top up exact
  deficits from an encrypted Foundry keystore while retaining the configured gas reserve.
- `./scripts/redeploy-testnet --account <name>` — broadcasts the frozen deployment script
  only after 48 chain-hours have elapsed from the later of the current deployment and last
  successful broadcast. It resolves and passes the keystore's sender explicitly.
- seed_demo.(ts|go) — seeded customer request + demo wallets (PRD §15.2)
- reset_demo — clean state between recording takes; must run clean twice before video day
