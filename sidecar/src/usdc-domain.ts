/**
 * The EIP-712 domain of the USDC we actually settle against.
 *
 * ── The finding (for whoever touches the signing path next) ──────────────────────────────────
 *
 * We first signed `transferWithAuthorization` with domain name **"USD Coin"**. That is *Ethereum
 * mainnet* USDC's EIP-712 name — a copy-from-mainnet reflex. Arc's USDC precompile
 * (0x3600…0000) uses name **"USDC"**, version "2".
 *
 * It looked fine for a long time because the mismatch is invisible until settlement:
 *   - The buyer signs with the seller's advertised name, the seller verifies locally with the same
 *     name, so 402 → sign → verify → 200 *passes*. The handshake never touches the chain.
 *   - But `transferWithAuthorization` recovers the signer against the precompile's OWN domain
 *     separator (name "USDC"). A "USD Coin" signature therefore recovers to a different address,
 *     and the contract reverts. Every authorization we produced would have failed at settle — on
 *     Circle Gateway or a self-facilitator alike. Settlement had simply never run (NOT_BROADCAST),
 *     so it never surfaced.
 *
 * Confirmed by computing the domain separator for each candidate name/version and matching it
 * against the deployed contract's on-chain `DOMAIN_SEPARATOR` — "USDC"/"2" is the only match.
 * `usdc-domain-chain-test.ts` re-derives that from the live chain so a chain that disagrees fails
 * the test, not just a typo.
 *
 * The general lesson: an EIP-712 name/version is a property of the deployed contract, not a
 * constant to hardcode from memory. The same mainnet reflex can bite anywhere a domain is written
 * by hand — derive it from the chain, or pin it against the chain.
 */
export const USDC_EIP712_DOMAIN = { name: 'USDC', version: '2' } as const;
