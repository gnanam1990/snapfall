/**
 * Which facilitator settles a payment.
 *
 * DEFAULT is Circle Gateway (`CircleFacilitator`) — the documented integration, unchanged. The
 * self-hosted broadcaster (`SelfFacilitator`) is opt-in via `SNAPFALL_SELF_FACILITATOR=1`. It was
 * added BESIDE Gateway to route around an access gate we could not pass on the submission
 * timeline, never to replace it: a reader should see that we integrated Circle's facilitator and
 * then relayed the authorization ourselves, not that we swapped Circle out.
 */
import { CircleFacilitator, type PaymentRequirements, type SettleOutcome, type VerifyOutcome } from './facilitator.js';
import { SelfFacilitator } from './self-facilitator.js';
import type { PaymentPayload } from './x402.js';

/** The one shape the seller holds — either settlement path satisfies it. */
export interface Facilitator {
  isConfigured(): boolean;
  verify(payment: PaymentPayload, requirements: PaymentRequirements): Promise<VerifyOutcome>;
  settle(payment: PaymentPayload, requirements: PaymentRequirements): Promise<SettleOutcome>;
  /** Records HOW a payment settled, on the receipt. Circle returns its verify/settle URLs;
   *  the self-facilitator returns self-markers, so a self-facilitated receipt can never be
   *  mistaken for a Circle one — and capture-v1-fixture (AT-18) correctly refuses it as evidence. */
  endpoints(): { verify: string; settle: string };
}

export function selectFacilitator(env: NodeJS.ProcessEnv = process.env): Facilitator {
  if (env.SNAPFALL_SELF_FACILITATOR === '1') return new SelfFacilitator();
  return new CircleFacilitator();
}
