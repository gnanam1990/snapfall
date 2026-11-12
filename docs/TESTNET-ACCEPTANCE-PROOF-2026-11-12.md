# Arc Testnet customer acceptance proof — 2026-11-12

This is an acceptance-workflow test, not a production due-diligence report, a paid-data proof, or a CCTP proof. Mainnet was not used.

## Verified lifecycle

Network: Arc Testnet (5042002). JobVault: `0xF3830D7C3B8ca873bB0b277c0e179999e3d52681`.
Job: `0xdc833803caf1faee0773f5baf28ce30f0bd462ff99b873795ed28689b97c5a0e`.

- Customer funded 19.5 testnet USDC: https://testnet.arcscan.app/tx/0xf0c57665267aa4755b0e2dc8a14f01ed7fc1e9d25c51bb8d06752e3b74ceba50
- Operator started work: https://testnet.arcscan.app/tx/0xf6ad3ca87f6b81610df56422062e130b3c1069f1fd97f028e741f8135eb4c373
- Operator submitted delivery: https://testnet.arcscan.app/tx/0x7ea9f29e9c4914fb416dcf6baee74d22de41ab8289824c128168d5ddbba3ce15
- Customer used their own passkey wallet to accept and settle: https://testnet.arcscan.app/tx/0x9a06929d4c91a02a6848cbf7585625f657fa6267df0f183c68349437a23de947

Settlement receipt status is `0x1`; JobVault `jobStatus` is `4`. The browser displayed “This job is accepted and settled.” The local daemon subsequently observed the existing settlement and recorded `settlement.executed` with actor `customer`; no duplicate settlement transaction was submitted.

The delivery hash is SHA-256 `0x0a660b2c3ba44fd8e7a21aa75839ec86f1f7893480c340cd9dc49e934ab79bff` of the acceptance-test evidence artifact, which explicitly records limitations. It does not anchor the generated stub due-diligence claims as verified facts.

## Checks and accounting

- Daemon `CGO_ENABLED=0 go test ./...`: passed.
- Dashboard tests: 110/110 passed; typecheck passed.
- Local indexer scanned recent test blocks starting at 71960000: three decoded chain events, zero reconciliation mismatches. This is a bounded test-window scan, not a complete historical backfill.
- Invoice version 2: funded 19.5 testnet USDC; advance repaid 0; operator net 19.5.

## Limits that remain open

The bootstrap DD worker produces explicitly stub content. H3 payment sidecar was unavailable. The 0.04 USDC profile approval reached pending integration, released its budget hold, and did not execute a payment. The 4 USDC source was rejected without requesting an alternative; no 0.06 USDC fallback was purchased.

No advance was requested for this test. The generated invoice honestly remains “partial — awaiting chain records” for advance and expense gaps. Those gaps do not negate the verified customer settlement, but this run does not demonstrate advance repayment, live x402 purchase, or end-to-end financing.

The original acceptance portal/backend integration was not deployed: the existing `/pay/<vaultJobId>` wallet panel handled on-chain acceptance, followed by local daemon reconciliation. No public tunnel or production environment setting was added. Temporary daemon processes were stopped.

At the initial acceptance milestone, CCTP Base Sepolia → Arc Testnet had not yet been tested (see subsequent result and recovery below). The source wallet had zero Base Sepolia USDC and 1.5 Arc testnet USDC at the handoff. Because the bridge-test job costs 1 USDC, the current UI selects direct Arc funding unless the Arc balance is below that price. A faucet and a separately authorized balance adjustment or different test job are prerequisites.

Earlier proof hashes from prior work were not reverified in this run and are not endorsed by this document.

## Subsequent CCTP test: successful mint/fund plus an unminted earlier burn

A separately authorized 2-USDC job was created to expose the existing bridge UI without moving the customer's existing 1.5 Arc USDC: `0xfe71f7c1203e5c82e56d267d839fd29f2745ca607484d6542f2cb0d8b8ee6808`.
Creation: https://testnet.arcscan.app/tx/0x759fd2bc393552c7999b66844b64a6d8925ca240b9f2af1b0fd11c8d2e435458

The customer then performed the bridge/payment. Successful Base Sepolia burn:
https://sepolia.basescan.org/tx/0x7d2f7f2526dfe5cc772343eebec75290d249ac4779d3af004760ddf2cf88f7be
Arc mint plus funding, receipt status 1:
https://testnet.arcscan.app/tx/0x5059467f58fd9620205f9ad427764609814f07f44e100795483641ee50e13fa1

Circle attestation matches source domain 6 and destination 26: burned 2.000401, actual fee 0.000260, minted 2.000141 test USDC. The transaction funded exactly 2 USDC into the new job; jobStatus is Funded (1), not settled. The message nonce is used on Arc.

However, balance reconciliation revealed an earlier burn of the same amount:
https://sepolia.basescan.org/tx/0xa41df59426a62e9542980e9a09a34a15debef73104b4b9538f9d20b5ec57c3e8
Its Circle attestation is complete, but Arc usedNonces is 0 for nonce `0x63a9a76f8c56946061900e414107219d73840213537dcd643e0a6a0cfc8db3a4`. It therefore still requires mint-only recovery of 2.000141 test USDC to the same customer. No recovery transaction has been submitted. Source balance is 15.999198 USDC; destination wallet balance after the successful funding is 1.500141 USDC. Do not initiate another burn.

Current UI limitations: bridge controls disappear once the job is funded, and the finish path appends a funding call rather than offering standalone mint recovery. The earlier transfer must not be described as recovered until its nonce is used and receipt/balance evidence confirms minting. This test provides a live CCTP success proof but also exposes a recovery gap; it is not a clean single-attempt end-to-end result.

### Recovery completed

The customer explicitly authorized mint-only recovery. The existing attestation was relayed on Arc without another burn or funding call:
https://testnet.arcscan.app/tx/0xe545954dd78647d1dd61f734f2b0bd543b7a53f5ae5cecd2bba443f699d7516d
Receipt status is 1; the earlier nonce is now used. Recovered 2.000141 test USDC to the original customer. Final customer Arc balance verified at 3.500282 USDC; the new job separately holds 2 USDC escrow. Both burns are now minted. The bridge UI recovery limitation remains a product gap, but no transfer remains pending from these two burns. Dashboard regression suite after the local guard change: 115/115 passed; typecheck passed.

During preflight, the daemon CCTP parser rejected Circle's 20-byte decoded recipient address because it expected a padded 32-byte value. The first attempt stopped before signing. A local fix now accepts either exact 20-byte addresses or zero-padded 32-byte forms, preserving recipient identity validation. Regression tests passed before the successful mint-only relay.

### Duplicate-burn prevention prepared locally (not deployed)

Code inspection found that the source wallet send helper waits for a user-operation receipt before returning its transaction hash, while the bridge only persisted pending state after that promise resolved. A receipt timeout or page closure after submission could therefore leave no saved burn and permit a second attempt. The component also lacked a synchronous start guard and cross-tab locking. These are verified code paths, not proof of which browser event caused the observed duplicate burns.

The local fix writes a durable submitting/unknown marker before any wallet call, preserves it on ambiguous errors, rejects retries when any prior record exists, and uses an exclusive Web Lock plus a synchronous component guard. Completed transfers retain a marker to block stale-tab re-burns. Storage failure prevents signing. Regression tests cover ambiguous receipt failure, retry after success, unavailable storage, and concurrent calls. This is fail-closed: a rejected passkey prompt may require manual investigation/reset, rather than guessing it is safe to retry. No production deployment has been performed. A friendly automated recovery/import interface remains open.

Review also identified case-variant job URLs as a lock bypass. The local guard now canonicalizes the job ID and blocks legacy case-variant pending records. This remains browser-local protection, not cross-device coordination. Helper regressions do not replace a real multi-tab browser test. The existing finish path is still not serialized across tabs, so duplicate mint/fund attempts may revert and waste testnet gas; this was not exercised or claimed fixed. The dashboard production build passed locally before the final key-normalization change; tests and typecheck passed after that change. No deployment or external publication has occurred.
