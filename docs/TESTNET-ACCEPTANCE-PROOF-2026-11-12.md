# Arc Testnet customer acceptance proof — 2026-11-12

This records an acceptance-workflow test and a later CCTP Base Sepolia → Arc test, both on testnets. It is not a production due-diligence report or a paid-data proof. Mainnet was not used.

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

### The bridged job settled

The 2-USDC job funded by the bridge was then taken through the normal lifecycle, so its escrow does not stay open:

- Operator (Circle treasury) started work: https://testnet.arcscan.app/tx/0x174791cddc4ff7fedaeba3c55616a51391ea6ce335570546cab7187b1cf4961a
- Operator submitted delivery: https://testnet.arcscan.app/tx/0x24cb98833ac5a01f0f7c83518f17bb315039d3804609b9e26425ecebacfb537b. The delivery hash `0x3fd5a262b05ad4cdf1a0a5b8bef1dbbd7030a91e2b5b50b300e758e656acd29c` is the SHA-256 of a plain record saying the job has no deliverable beyond the bridge proof.
- Customer accepted from their passkey wallet, as one sponsored user operation: https://testnet.arcscan.app/tx/0x7624fcb26fb83ccb349e6d5541f00154171f3c4ac5ccdf73664288a3d259a12a. Receipt status 1, `JobSettled`, 2 USDC moved from JobVault to the treasury.

`jobStatus` is now 4. The customer's Arc balance is unchanged at 3.500282 USDC, because the gas was sponsored.

### Duplicate-burn prevention

Code inspection found that the source wallet's `send` waited for a user-operation receipt before returning its transaction hash, while the bridge only persisted pending state after that promise resolved. A receipt timeout or page closure after submission could therefore leave no saved burn and permit a second attempt. The component also lacked a synchronous start guard and cross-tab locking. These are verified code paths, not proof of which browser event caused the observed duplicate burns.

The fix (`dashboard/lib/cctpBurnGuard.ts`, `passkeyWallet.ts`):

- For a burn, `send` runs viem's three steps separately: prepare, sign with the passkey, then hand the signed user operation's hash to the caller **before** broadcasting it. The burn is saved as `submitted` with that hash first; if the save fails, nothing is broadcast. The locally computed hash was checked against EntryPoint v0.7 `getUserOpHash` on Arc Testnet, and the bundler's answer must equal it.
- A failure before that point (a declined passkey, a refused sponsorship, a failed save) clears the record, because nothing left the tab; the customer can simply try again.
- A failure after it keeps the `submitted` record. On reload the panel offers to finish the transfer, looks the user operation up at the bundler, and only then waits for Circle. It never burns again.
- A completed payment leaves a marker so a stale tab cannot burn for the same job. A burn that minted but did not cover the price clears the record, so the difference can still be brought over.
- A user operation that was included but whose call reverted moved nothing (the batch reverts as one), so its record is cleared and the customer may try again. A timeout or a lost answer still blocks.
- Every write runs under one exclusive Web Lock per job: starts, resumes and finishes. A finish first re-checks that the saved record is still its own burn, so a stale tab can neither pay twice nor clear another tab's `completed` marker; a resume re-reads the record before writing. An in-tab in-flight guard covers double clicks. Job IDs are lower-cased, and a legacy mixed-case, unrecognised or JSON-`null` record blocks rather than being guessed safe.

Limits: protection is per browser, not across devices. A user operation the bundler dropped stays `submitted` and needs manual inspection; there is no import or reset UI yet. Fund and accept keep viem's single `sendUserOperation` call; only the burn uses the split path, which has unit tests but has not yet run against the live bundler.
