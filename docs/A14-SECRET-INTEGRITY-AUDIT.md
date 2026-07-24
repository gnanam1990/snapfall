# A14 Secret and Recording-Integrity Audit

Audit time: 2026-07-24T13:41:35Z

Audited baseline: `main` at `4260a9c3880f3e8fe674976f36730bc783d8e5b1`

## Result

The repository and all reachable Git history are clean of high-confidence private-key,
provider-token, bearer-token, mnemonic, embedded-credential URL, and private-key-file
findings. No committed runtime logs, screenshots, HAR captures, or video files exist to
inspect. `sidecar/.env.example` is the only `.env` path ever committed and all of its secret
values are empty.

No credential rotation or history rewrite is required by this audit.

Run the repeatable filename-only preflight from the repository root:

```bash
./scripts/a14-audit
```

The command reports file paths, never matching values. It scans the current tracked tree and
every reachable commit. High-confidence matches fail the command; tracked recording artifacts
are inventoried for the mandatory manual visual review below.

## Secret review

The audit covered:

- PEM private-key headers; AWS, GitHub, Slack, OpenAI-style and Telegram token formats;
- hard-coded 32-byte values assigned to key, mnemonic, API-key, owner-token, approval-secret,
  auth-token, or bot-token names;
- customer `act_` credentials and URLs containing embedded username/password material;
- all historical filenames resembling `.env`, keystores, private keys, credentials, or seed
  material;
- all tracked logs and media, including deleted paths still reachable from Git history;
- runtime secret ingress and logging call sites in the daemon, dashboard, and sidecar.

Hard-coded values remaining in tests are deliberately inert test markers or generated
ephemeral keys. Public contract addresses, transaction hashes, event topics, ABI hashes, and
Go/npm integrity hashes are not secrets. Runtime credentials continue to enter through
environment variables or encrypted Foundry keystores; the code does not log their values.

GitGuardian also passed on the audited A13 head before it merged into this baseline. That
external result is supporting evidence, not a substitute for the history scan above.

## Recording-integrity review

### Verified live evidence

Read-only checks against `https://rpc.testnet.arc.network` at Arc block `53427837` returned
chain ID `5042002`, non-empty bytecode at all three committed deployment addresses, reciprocal
JobVault/FloatPool wiring, and canonical USDC
`0x3600000000000000000000000000000000000000`.

The deployment receipts are successful and create the addresses committed in
`deployments/arc-testnet.json`:

| Contract | Deployment transaction |
|---|---|
| AuditAnchor | [`0x7476…80d`](https://testnet.arcscan.app/tx/0x7476b09723b8b1e823dbb882b51dd60226643703cc2a96e4ec0a0cd638ce480d) |
| JobVault | [`0x22af…2c9`](https://testnet.arcscan.app/tx/0x22af2e113de047d19afd4620d8dc54e5b5a2386d94ae079a04efc3569a5062c9) |
| FloatPool | [`0x26bb…10e`](https://testnet.arcscan.app/tx/0x26bbe400e9de3d41b4c7cd18651a71a2c035754906d3e9809dfa4eda0e03c10e) |

This proves the deployment only. It does not prove that the complete demo spine has run.

### Evidence that must not be presented as live

- `daemon/internal/indexer/testdata/h1-spine-logs.json` is synthetic test input. Its repeated
  addresses, block hash, job IDs, and sequential transaction hashes are intentionally fake.
- The sidecar localhost demo uses an ephemeral key and verifies x402 authorization locally,
  but `sidecar/README.md` correctly states that settlement is not broadcast.
- `sidecar/fixtures/v1-circle-payment.json`, the declared real Circle V1 payment fixture, is
  not present. Therefore AT-18's endpoint-contract test is green, but no real Circle settlement
  fixture is currently available as recording evidence.
- No screenshots or video fixtures are committed. This audit cannot certify footage that does
  not yet exist.

### Gate before recording or publishing

For every on-chain beat in PRD Appendix A.1, capture the Arc explorer transaction hash and
confirm its receipt status, contract address, emitted events, amounts, and ordering against the
daemon's indexed row. The opening zero-balance view and explicitly off-chain rejection/QA
beats must be labeled as such. The final edit must disclose that it is a replay of real runs,
and must not splice the synthetic H1 fixture or localhost sidecar dry run into a claim of live
settlement.

Until the real x402 fixture and full-spine transaction set exist, the honest status is:
**deployment verified; full recorded demo integrity not yet certifiable**.
