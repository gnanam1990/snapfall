# USYC idle-capital sweep decision

Verified against the official Arc and USYC documentation on 23 July 2026.

## Arc testnet contracts

| Contract | Address |
| --- | --- |
| USYC token | `0xe9185F0c5F296Ed1797AaE4238D26CCaBEadb86C` |
| USYC Teller | `0x9fdF14c5B14173D74C08Af27AebFf39240dC105A` |
| Entitlements | `0xcc205224862c7641930c87679e98999d23c26113` |

Read-only checks against `https://rpc.testnet.arc.network` confirmed code at all three
addresses on 23 July 2026. The token reports symbol `USYC` and 6 decimals. No subscription,
redemption, approval, or other transaction was attempted.

Sources:

- [Arc testnet contract addresses](https://docs.arc.io/arc/references/contract-addresses)
- [USYC smart-contract addresses](https://usyc.docs.hashnote.com/overview/smart-contracts)
- [USYC Teller integration](https://usyc.docs.hashnote.com/integration-guides/teller-smart-contract)

## Decision: mock ships for A8

Real USYC exists on Arc testnet, but it is permissioned. The official Arc instructions require
opening a Circle Support ticket with the wallet address; allowlisting typically takes 24–48
hours. That is not a trivial integration inside A8's half-day investigation timebox.

Snapfall therefore ships:

- `IIdleCapitalStrategy`, the stable application seam;
- `MockUSYCStrategy`, a clearly disclosed testnet fallback that moves real mock USDC, tracks
  proportional positions, simulates donated yield, reports asset-denominated balances, and
  redeems on demand;
- no claim that a real USYC subscription or redemption has occurred.

The mock's `isMock()` value is always `true` so the dashboard and demo caption cannot silently
present simulated yield as Circle/Hashnote yield.

## Real-adapter activation checklist

Do not replace the mock until all of these are true:

1. The designated Arc testnet wallet is allowlisted by Circle.
2. An eligibility-approved depositor submits a compliant Teller
   `deposit(assets, receiver)` meeting USYC's documented 100,000 USD minimum investment.
3. The resulting USYC balance is observed at the documented token address.
4. `redeem(shares, receiver, account)` returns testnet USDC on demand.
5. A real adapter implements `IIdleCapitalStrategy` and returns `false` from `isMock()`.
6. Deposit, balance, redemption, and failure-path fixtures are committed with explorer links.

The real-adapter prerequisites above are not claims that Snapfall is eligible. USYC production
eligibility and minimum-investment rules must be independently reviewed before any production
claim or use.
