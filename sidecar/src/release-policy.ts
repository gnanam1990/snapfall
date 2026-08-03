/**
 * When a paid resource may be handed over.
 *
 * This is one `if`, and it lived inline in seller.ts where nothing could test it. It read:
 *
 *     if (outcome.state !== 'settled' && facilitator.isConfigured()) { ...withhold... }
 *
 * The second clause quietly disabled the first. With no CIRCLE_API_KEY the outcome is
 * 'not-configured' and isConfigured() is false, so the withholding block was skipped entirely and
 * the seller handed the resource over anyway -- in the default configuration, under a comment
 * promising that "goods are released on CONFIRMED payment only". Nothing was faked and no receipt
 * lied; the protection simply was not there.
 *
 * It lives here as a pure function for one reason: the version in seller.ts could not be tested
 * without standing up an HTTP server and signing an EIP-3009 authorization, so for as long as it
 * was wrong, nothing noticed. release-policy-test.ts now drives every state exhaustively.
 */

/** The settlement states seller.ts can observe. Mirrors SettleOutcome['state'] in facilitator.ts. */
export type SettlementState = 'settled' | 'not-configured' | 'rejected' | 'unknown';

export type ReleaseDecision =
  /** Payment is confirmed, or this is an acknowledged local demo. Hand the resource over. */
  | 'deliver'
  /** No facilitator, and this seller is reachable beyond loopback. Withhold; release the nonce. */
  | 'refuse-unsettled'
  /** The facilitator declined. Nothing moved, so the nonce is released. */
  | 'refuse-rejected'
  /** Timeout or unusable response. The authorization may still land, so hold the nonce. */
  | 'refuse-unknown';

/**
 * @param state what the facilitator said, or 'not-configured' when there is no key.
 * @param unsettledDeliveryOk whether an unconfirmed delivery is acceptable here. In seller.ts this
 *   is true only when the server is bound to loopback: `npm run demo:loop` and the spine run's
 *   beats 3 and 5 need a delivery with no Circle account, and that concession is defensible for a
 *   local demo and indefensible for anything a network can reach.
 *
 * Note the asymmetry, which is the whole point: 'unknown' is refused even on loopback. A missing
 * key means nothing was attempted and nothing can have moved; a timeout means the money may be in
 * flight, and delivering against that is a paid API giving away its product on a hope.
 */
export function releaseDecision(
  state: SettlementState,
  unsettledDeliveryOk: boolean,
): ReleaseDecision {
  if (state === 'settled') return 'deliver';
  if (state === 'not-configured') return unsettledDeliveryOk ? 'deliver' : 'refuse-unsettled';
  if (state === 'rejected') return 'refuse-rejected';
  return 'refuse-unknown';
}
