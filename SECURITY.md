# Security

Snapfall moves real USDC on Arc mainnet, so a vulnerability report is the most useful thing you
can send us. Thank you for taking the time.

## Reporting a vulnerability

Report privately through GitHub: open the repository's **Security** tab and choose
**Report a vulnerability**. Please do not open a public issue, pull request or discussion for a
security problem.

A useful report says:

- what is affected (contract, daemon, sidecar or dashboard) and at which commit;
- the steps or transactions that reproduce it, ideally against Arc testnet or a local chain;
- what an attacker gains, and what they need first (a role, a signature, a secret, a timing).

There is no bug bounty. Reports are read by the maintainer, who replies on the advisory thread,
agrees a fix and a disclosure plan with you, and credits you in the advisory unless you would
rather not be named.

## Scope

| Part | Where |
| --- | --- |
| Contracts: JobVault, FloatPool, AuditAnchor | [`contracts/`](contracts/), addresses in [`docs/addresses.md`](docs/addresses.md) |
| Owner daemon, including the owner API and approvals | [`daemon/`](daemon/) |
| Payment sidecar and its signing path | [`sidecar/`](sidecar/) |
| Dashboard and its owner-data guards | [`dashboard/`](dashboard/) |

Out of scope: Arc itself, Circle's services, and third-party RPC providers. Report those to
their owners.

## Testing rules

- Use Arc testnet or a local chain. Do not run an exploit against the mainnet contracts, and do
  not move funds that are not yours on any network.
- Do not try to read or change another person's job, customer link or owner data on the hosted
  sites. Show the flaw on a deployment you run yourself.
- No denial-of-service, spam or load testing against the hosted sites or their RPC providers.

## Current mainnet state

The mainnet contracts listed in the README are a small, capped set that is kept paused outside
deliberate self-tests. They were deployed before the audit fixes in
[#126](https://github.com/gnanam1990/snapfall/pull/126) and
[#127](https://github.com/gnanam1990/snapfall/pull/127); those findings are public. The plan in
[`docs/MAINNET.md`](docs/MAINNET.md) retires this set and redeploys from the fixed source before
any liquidity is accepted. Until then, please do not deposit into the mainnet pool.
