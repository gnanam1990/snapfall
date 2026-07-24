# scripts

- `./scripts/testnet-ops` — checks every funded wallet from
  `deployments/arc-testnet.json`. Add `--fund --funder-account <name>` to top up exact
  deficits from an encrypted Foundry keystore while retaining the configured gas reserve.
- `./scripts/redeploy-testnet --account <name>` — broadcasts the frozen deployment script
  only after 48 chain-hours have elapsed from the later of the current deployment and last
  successful broadcast. It resolves and passes the keystore's sender explicitly.
- `./scripts/a14-audit` — scans the tracked tree and all reachable Git history for
  high-confidence secret formats and inventories committed logs/recording artifacts without
  printing matched values. Manual recording claims still follow
  `docs/A14-SECRET-INTEGRITY-AUDIT.md`.
- seed_demo.(ts|go) — seeded customer request + demo wallets (PRD §15.2)
- reset_demo — clean state between recording takes; must run clean twice before video day
