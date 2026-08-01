# A14 Secret and Recording-Integrity Audit

Audit refreshed: 2026-08-01

Audited integration baseline: `main` at `e562c4aed36f8b626d9d30cafee2faa6103fecab`

## Result

The repository's current tracked tree and every ref-reachable commit tree are clean of the
high-confidence credential formats listed below. No committed runtime logs, screenshots, HAR
captures, or video files exist to inspect. `sidecar/.env.example` is the only `.env` path ever
committed; every reachable version has empty values for its secret-named variables.

No credential rotation or history rewrite is required by this audit.

Run the repeatable filename-only preflight from the repository root:

```bash
./scripts/a14-audit
```

The command reports file paths and variable names, never matching values. It scans the current
tracked tree and every ref-reachable commit tree. It fails closed for shallow clones, empty
history, missing objects, or Git command errors, and prints its revision/path coverage
denominator. High-confidence matches fail the command; tracked recording artifacts are
inventoried for the mandatory manual visual review below.

The automated preflight does not claim to scan unreachable objects, force-pushed GitHub
objects absent from local refs, commit/tag messages, untracked files, arbitrary bearer tokens,
or BIP-39 word sequences. A confirmed credential leak requires rotation even if a later local
checkout cannot reach the leaked object.

## Secret review

The audit covered:

- PEM private-key headers; AWS, GitHub, Slack, OpenAI/Anthropic-style and Telegram token
  formats;
- quoted or unquoted 32-byte values assigned to private-key, API-key, owner-token,
  approval-secret, auth-token, access-key, bot-token, or seed-phrase names;
- non-empty assignments to Snapfall's real credential variables, including Circle, H2/H3,
  treasury, customer, LP, and owner credentials;
- raw `--private-key` arguments, Circle API-key shapes, and JSON V3 keystore structures;
- customer `act_` credentials and URLs containing embedded username/password material;
- all historical filenames resembling `.env`, keystores, private keys, credentials, mnemonics,
  or seed phrases, including paths introduced only by merge resolution;
- all tracked logs and media, including deleted paths still reachable from Git history;
- runtime secret ingress and logging call sites in the daemon, dashboard, and sidecar.

Hard-coded values remaining in tests are deliberately inert test markers or generated
ephemeral keys. Public contract addresses, transaction hashes, event topics, ABI hashes, and
Go/npm integrity hashes are not secrets. Runtime credentials continue to enter through
environment variables or encrypted Foundry keystores; the code does not log their values.

GitGuardian remains supporting evidence, not a substitute for the fail-closed history scan.

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
  fixture is currently available as recording evidence. The human-gated path to that proof is
  maintained in `docs/V3-CIRCLE-SETUP.md`.
- No screenshots or video fixtures are committed. This audit cannot certify footage that does
  not yet exist.

### Gate before recording or publishing

Follow `docs/SPINE-RUNS.md` for the executable run and evidence format. For every on-chain beat
in PRD Appendix A.1, capture the Arc explorer transaction hash and
confirm its receipt status, contract address, emitted events, amounts, and ordering against the
daemon's indexed row. The opening zero-balance view and explicitly off-chain rejection/QA
beats must be labeled as such. The final edit must disclose that it is a replay of real runs,
and must not splice the synthetic H1 fixture or localhost sidecar dry run into a claim of live
settlement.

Until the real x402 fixture and full-spine transaction set exist, the honest status is:
**deployment verified; full recorded demo integrity not yet certifiable**.
