# First PASS spine run — 16 Nov 2026 (reduced scale)

`scripts/spine_run --scaled`, run `20261116T033545Z` at commit `1eccb43`, against the Arc testnet
contracts deployed the same day (JobVault `0x449A…0bD1`, FloatPool `0x4780…86bE`, start block
72420303). Every beat printed PASS, and each one was read back from chain or from the sidecar's
durable record, not from the request that caused it. The log line is in `spine-runs.tsv`.

**This is a reduced-scale run: the job price was 1.00 USDC, not the PRD's 25.00.** It proves the
machinery end to end. It is not evidence for a done-when clause that names the PRD figures.

## What happened, in chain order

| Beat | What | Transaction | Block |
| --- | --- | --- | --- |
| 1 FUND | LP tops the pool up to the 6.00 cap floor (0.4912 deposit) | [`0x7540dc28…f76603`](https://testnet.arcscan.app/tx/0x7540dc2818872d8c7dd1b01b5d8a30a871d670e2eafc274f5f801c970ff76603) | |
| 1 FUND | operator creates the job | [`0x8b6109cc…c2ca3a`](https://testnet.arcscan.app/tx/0x8b6109cc3a39b327f083b8ae2dfc813b370261d1e2e76db7416db3cef2c2ca3a) | |
| 1 FUND | customer escrows 1.00 USDC | [`0x7f2df2d3…a8e8249`](https://testnet.arcscan.app/tx/0x7f2df2d36cde1216d0fb77a5a07348863c9071b4ebd0b0fe97a8ca17a89e8249) | |
| 3 SPEND | x402 purchase, 0.04 USDC, operator → seller | [`0xbda95656…22a7d06`](https://testnet.arcscan.app/tx/0xbda95656e0ad2ee7054ea29064c3601c508f373dbd52eedce21635d2222a7d06) | 72509700 |
| 2 SNAP | `requestAdvance`: 0.60 USDC principal, 0.012 fee, rate 6000 bps | [`0xfea88ec2…71ad2e3`](https://testnet.arcscan.app/tx/0xfea88ec2cd2770376d7e879ef15c0b28e46b9b2ed505a1f77df9d66d871ad2e3) | 72509704 |
| 2b | `startWork`: Funded → InProgress | [`0x5da0f717…c6c04d6`](https://testnet.arcscan.app/tx/0x5da0f717bd26ad5a08041af898640d488696c6afd25c6d3ddd64a0afcc6c04d6) | 72509718 |
| 4 REJECT | 4.00 source escalated, answered `request_alternative` (no transaction) | — | |
| 5 ALTERNATIVE | x402 purchase, 0.06 USDC, operator → seller | [`0xefa48a88…ceda0c931`](https://testnet.arcscan.app/tx/0xefa48a88dbeb51daede10c7368f867f18a2a83e4016f177e0f028b6ceda0c931) | 72509722 |
| 5c | `submitDelivery`: InProgress → Delivered | [`0xd1697523…d2fa123`](https://testnet.arcscan.app/tx/0xd16975239ca0c2c39a0bcf0fd12c2880ae58359e0eddbfb79219ada5fd2fa123) | 72509734 |
| 6 SETTLE | customer `acceptDelivery`: the waterfall | [`0x03b26490…511e16f44`](https://testnet.arcscan.app/tx/0x03b26490f46996487beb5a579db3b3ab2289b6bc87b2e4c57462bdd511e16f44) | 72509744 |
| 7 RATE | the org's advance rate read 6000 → 6500 bps | — (read) | |

### The settlement, decoded

USDC `Transfer` logs in `0x03b26490…`, status success, sent by the customer:

| log | from | to | USDC |
| --- | --- | --- | --- |
| 14 | JobVault | FloatPool | 0.612 (0.60 principal + 0.012 fee) |
| 17 | JobVault | operator | 0.388 (what remains of the escrow) |

The pool is paid at a lower log index than the operator, in the same transaction.

### The two purchases

Both sidecar records are `SETTLED`. The sidecar wrote that state only after reading each
transaction from its own RPC and finding the operator's EIP-3009 authorization consumed
(`AuthorizationUsed`), the signed amount transferred to the signed payee, and
`authorizationState` true (`sidecar/src/settlement-confirm.ts`, commit `9e3ce17`). The seller
wallet `0xc7b5…7c3B` is a fresh receive-only address; it holds 0.30 USDC after this and the two
earlier runs that day (0.10 each).

## What this run does NOT show

- **The purchase was not paid with borrowed money.** The 0.04 purchase landed at block 72509700,
  four blocks before the advance at 72509704. The DD worker does not wait for the snap, and the
  operator wallet also deployed the contracts, so it already held faucet USDC (21.60 at the start
  of the run). The run proves each piece happened and settled; it does not prove the agents could
  only spend what the pool advanced. A zero-balance operator would.
- **Full scale.** At the PRD's 25.00 price and today's 6500 bps rate the advance would be 16.25,
  above FloatPool's 10.00 per-advance cap, so the PRD figures cannot run against these contracts
  as configured.
- **The screen.** No dashboard was running, so V7, V10, V11 and A9 (the human-watched beats) were
  not observed.
- **The LP is allowlisted by hand.** The pool's depositor allowlist admits only the deployer at
  deploy time; the LP wallet was added with `setDepositorAllowed` (tx
  [`0x093b4115…7edd807`](https://testnet.arcscan.app/tx/0x093b4115f05a61aab6358b0d47c01de75a84e0aa06e2183da192451627edd807))
  before the run, because `seed_demo` refuses an operator-funded pool.

## The runs before it, same day

| run | verdict | why |
| --- | --- | --- |
| `20261116T024414Z` | FAIL at 1-FUND | the LP was not on the new pool's depositor allowlist (`DepositorNotAllowed`) |
| `20261116T024812Z` | UNVERIFIED at 3-SPEND | the payments settled on chain, but the sidecar had no code to mark them SETTLED |
| `20261116T025542Z` | UNVERIFIED at 3-SPEND | the same, plus beat 7 read the rate at the 25.00 default and hit the 10.00 advance cap |

The last two runs' purchases are on chain too; the fixes (`9e3ce17`, `554bf7d`) changed what was
recorded and checked, not what was paid.

## The second PASS, back to back

Run `20261116T035924Z` at commit `a8aca00`, started straight after, with no fix between the two:
PASS on every beat, again at price 1.00.

| | |
| --- | --- |
| job | `0x4b31149604127db4d7d40ccc74d6221973e3048a2d3235b97765c169ea09e6ec`, read Accepted (4) |
| advance | 0.65 principal, 0.013 fee, rate 6500 bps ([`0x281d446c…eb963b9`](https://testnet.arcscan.app/tx/0x281d446c59f6b7a7db0489882549f2461180e2137e9571d9b2c514a36eb963b9), block 72511740) |
| purchases | 0.04 [`0xe4375964…e26ebbc5`](https://testnet.arcscan.app/tx/0xe43759644c9fced3975a3e4c1f2efb8be843605377f657b9b9970141e26ebbc5) (block 72511738) and 0.06 [`0x53f9f480…805e4703`](https://testnet.arcscan.app/tx/0x53f9f480f27094b24c08ef305c102ce0542c00136100b6be4b42a942805e4703) (block 72511759), both `SETTLED`, both created during this run |
| settlement | [`0xf41e9fe8…d7c427ad`](https://testnet.arcscan.app/tx/0xf41e9fe8c750f9ea37843bf935b9d09cb5123c9899b02f3399fd4ef4d7c427ad), block 72511774: FloatPool 0.663 at log 75, operator 0.337 at log 78 |
| rate | 6500 -> 7000 bps |

**The ordering caveat repeated.** The 0.04 purchase landed two blocks before the advance, so in
both runs the first purchase was paid before any borrowed money existed. Twice out of twice, so
this is how the code behaves: the DD worker starts buying without waiting for the snap.

**Not V12.** These are two consecutive PASSes with no manual fix between them, but V12's clause
is "reset to full spine, twice in a row": `scripts/reset_demo` was not run between them and both
were at reduced scale.
