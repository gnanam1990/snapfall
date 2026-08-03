/**
 * Where a displayed advance rate actually came from.
 *
 * The Float page resolves the rate as `snapshot.orgRateBps ?? fallbackRateBps`, collapsing two
 * very different figures into one variable: an eth_call against FloatPool.advanceRate, and the
 * local daemon's own assertion off the H2 stream (which, under SNAPFALL_DEMO_STREAM, is a
 * hardcoded constant in lib/mockData.ts). The surface then printed a check mark and the words
 * "computed entirely on-chain" over whichever one it got.
 *
 * The rule is one line, but it was previously expressed as a ternary inside JSX where nothing
 * could test it. It lives here so the invariant -- only a chain read may carry the on-chain
 * proof claim -- is asserted by rateProvenance.test.ts instead of assumed.
 */
export type RateProvenance = 'chain' | 'daemon' | 'none';

/**
 * @param chainRateBps FloatPool.advanceRate for the configured org, or null when no organisation
 *   is configured and the call was therefore never issued.
 * @param fallbackRateBps the daemon's asserted rate, or null when no daemon is connected.
 *
 * Both parameters are compared against null explicitly: 0 is a legitimate rate, and a truthiness
 * check would misattribute a real on-chain 0 to the daemon.
 */
export function rateProvenance(
  chainRateBps: number | null | undefined,
  fallbackRateBps: number | null | undefined,
): RateProvenance {
  if (chainRateBps !== null && chainRateBps !== undefined) return 'chain';
  if (fallbackRateBps !== null && fallbackRateBps !== undefined) return 'daemon';
  return 'none';
}

/** Whether a rate of this provenance may be presented as verified on chain. */
export function mayClaimOnChain(provenance: RateProvenance): boolean {
  return provenance === 'chain';
}
