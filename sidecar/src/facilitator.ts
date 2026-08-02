/**
 * The Circle Gateway x402 facilitator client — the piece that actually moves money.
 *
 * `seller.ts` verifies that the buyer's EIP-3009 authorization is well formed, correctly signed,
 * for the right amount, to the right payee, and unspent. None of that broadcasts anything: an
 * authorization is a bearer instrument, and somebody has to submit `transferWithAuthorization` on
 * chain for USDC to move. That somebody is the facilitator, which is why the seller has been
 * honestly reporting `settlement: NOT_BROADCAST` rather than claiming a payment it had not taken.
 *
 * ── The rule this file exists to keep ────────────────────────────────────────────────────────
 *
 * A settlement string is EVIDENCE. `capture-v1-fixture.ts` refuses to write V1's fixture unless
 * the receipt carries a real 0x-64 transaction hash, and that refusal is the only thing standing
 * between a fabricated string and committed proof of a payment that never happened.
 *
 * So: this module NEVER invents a settlement value. Not for a dry run, not for a test, not when
 * the facilitator is unreachable. Either Circle returns a transaction hash it broadcast, or the
 * seller keeps saying NOT_BROADCAST and V1 stays open. The injectable `fetch` below exists so the
 * request and response SHAPING can be tested offline; it cannot be used to manufacture a
 * settlement, because every path that does not carry a real hash returns a non-settled result.
 *
 * ── Post-sign semantics ──────────────────────────────────────────────────────────────────────
 *
 * Once `settle` has been called, the authorization has left this process and may land even if the
 * response never arrives. A timeout is therefore NOT a failure: it is an unknown, and the caller
 * must reconcile rather than release. This mirrors the buyer-side taxonomy in `buyer.ts`
 * (POST_SIGN_CODES) for the same reason.
 *
 * ── Not verified against the live API ────────────────────────────────────────────────────────
 *
 * Written from Circle's documented x402 facilitator interface and the endpoints already pinned in
 * `circle-facilitator-fixture.ts` (AT-18). It has NOT been exercised against the real service,
 * because that needs the V3 credentials. The request/response shaping is covered by tests; the
 * wire contract is an assumption until a real key exists. Treat the first live run as the test.
 */

import { CIRCLE_TESTNET_FACILITATOR_ENDPOINTS } from './circle-facilitator-fixture.js';

/** A 32-byte transaction hash, the only shape that counts as settled. */
const TX_HASH_RE = /^0x[0-9a-fA-F]{64}$/;

/** The seller's honest marker when no broadcast was attempted. Must match seller.ts. */
export const NOT_BROADCAST = 'NOT_BROADCAST';

/**
 * Submitted, outcome unknown. NOT a success and NOT a failure.
 *
 * `capture-v1-fixture.ts` treats this as a dry-run marker and refuses to write a fixture for it,
 * which is correct: an unknown outcome is not evidence of a payment. It is distinct from
 * NOT_BROADCAST because the money may in fact have moved, so the operator must go and look
 * rather than assume nothing happened.
 */
export const PENDING_SETTLEMENT = 'PENDING';

export interface PaymentRequirements {
  scheme: string;
  network: string;
  maxAmountRequired: string;
  resource: string;
  payTo: string;
  asset: string;
  maxTimeoutSeconds?: number;
  extra?: Record<string, unknown>;
}

export type SettleOutcome =
  | { state: 'settled'; txHash: string; payer?: string }
  /** The facilitator refused BEFORE broadcasting. Nothing moved; safe to treat as terminal. */
  | { state: 'rejected'; reason: string }
  /** Submitted, outcome unknown. The authorization may still land. Reconcile, never release. */
  | { state: 'unknown'; reason: string }
  /** No facilitator configured, so no broadcast was attempted. */
  | { state: 'not-configured' };

export interface VerifyOutcome {
  valid: boolean;
  reason?: string;
  payer?: string;
}

type FetchLike = (input: string, init: RequestInit) => Promise<Response>;

export interface FacilitatorConfig {
  apiKey?: string;
  verifyUrl?: string;
  settleUrl?: string;
  /** Injected only by tests, to exercise request/response shaping without a network. */
  fetchImpl?: FetchLike;
  /** Milliseconds before a settle is treated as UNKNOWN rather than failed. */
  timeoutMs?: number;
}

export class CircleFacilitator {
  private readonly apiKey: string;
  private readonly verifyUrl: string;
  private readonly settleUrl: string;
  private readonly doFetch: FetchLike;
  private readonly timeoutMs: number;

  constructor(cfg: FacilitatorConfig = {}) {
    this.apiKey = cfg.apiKey ?? process.env.CIRCLE_API_KEY ?? '';
    this.verifyUrl = cfg.verifyUrl ?? process.env.CIRCLE_X402_VERIFY_URL ?? CIRCLE_TESTNET_FACILITATOR_ENDPOINTS.verify;
    this.settleUrl = cfg.settleUrl ?? process.env.CIRCLE_X402_SETTLE_URL ?? CIRCLE_TESTNET_FACILITATOR_ENDPOINTS.settle;
    this.doFetch = cfg.fetchImpl ?? ((input, init) => fetch(input, init));
    this.timeoutMs = cfg.timeoutMs ?? Number(process.env.CIRCLE_X402_TIMEOUT_MS ?? 20_000);
  }

  /**
   * False when no API key is present, which is the state of every machine until V3 is done.
   * The seller checks this and keeps reporting NOT_BROADCAST rather than attempting a call it
   * knows will fail — an attempted-and-failed broadcast is a different, worse claim than an
   * honest "no facilitator configured".
   */
  isConfigured(): boolean {
    return this.apiKey.trim() !== '';
  }

  /** Endpoints actually in use, recorded into the V1 fixture so AT-18 can assert them. */
  endpoints(): { verify: string; settle: string } {
    return { verify: this.verifyUrl, settle: this.settleUrl };
  }

  private headers(): Record<string, string> {
    return {
      'content-type': 'application/json',
      accept: 'application/json',
      authorization: `Bearer ${this.apiKey}`,
    };
  }

  private body(paymentPayload: unknown, requirements: PaymentRequirements): string {
    // x402Version 1 to match the fixture contract AT-18 pins.
    return JSON.stringify({ x402Version: 1, paymentPayload, paymentRequirements: requirements });
  }

  /**
   * Pre-broadcast check. A failure here is safe: nothing has been submitted, so the caller may
   * reject the request outright without any reconciliation worry.
   */
  async verify(paymentPayload: unknown, requirements: PaymentRequirements): Promise<VerifyOutcome> {
    if (!this.isConfigured()) return { valid: false, reason: 'facilitator not configured' };
    try {
      const res = await this.withTimeout((signal) =>
        this.doFetch(this.verifyUrl, {
          method: 'POST',
          headers: this.headers(),
          body: this.body(paymentPayload, requirements),
          signal,
        }),
      );
      const parsed = (await res.json().catch(() => ({}))) as Record<string, unknown>;
      if (!res.ok) {
        return { valid: false, reason: `verify HTTP ${res.status}: ${stringField(parsed, 'invalidReason', 'message') || 'no reason given'}` };
      }
      const valid = parsed.isValid === true;
      return {
        valid,
        reason: valid ? undefined : stringField(parsed, 'invalidReason', 'message') || 'facilitator said the payment is not valid',
        payer: stringField(parsed, 'payer'),
      };
    } catch (e) {
      // verify never broadcasts, so a transport failure here is simply "we do not know yet" and
      // the caller should not settle on it.
      return { valid: false, reason: `verify unreachable: ${(e as Error).message}` };
    }
  }

  /**
   * Broadcasts. After this call returns anything other than `rejected`, assume the authorization
   * may have reached the chain.
   */
  async settle(paymentPayload: unknown, requirements: PaymentRequirements): Promise<SettleOutcome> {
    if (!this.isConfigured()) return { state: 'not-configured' };

    let res: Response;
    try {
      res = await this.withTimeout((signal) =>
        this.doFetch(this.settleUrl, {
          method: 'POST',
          headers: this.headers(),
          body: this.body(paymentPayload, requirements),
          signal,
        }),
      );
    } catch (e) {
      // The request left this process. It may have been received and broadcast even though no
      // response came back, so this is UNKNOWN, never a failure.
      return { state: 'unknown', reason: `settle did not return: ${(e as Error).message}` };
    }

    const parsed = (await res.json().catch(() => ({}))) as Record<string, unknown>;

    if (!res.ok) {
      // A 4xx is the facilitator declining before broadcast; a 5xx may be a failure AFTER it
      // already submitted, which nobody downstream can distinguish from the outside.
      const reason = `settle HTTP ${res.status}: ${stringField(parsed, 'errorReason', 'message') || 'no reason given'}`;
      return res.status >= 500 ? { state: 'unknown', reason } : { state: 'rejected', reason };
    }

    const txHash = stringField(parsed, 'transaction', 'txHash', 'transactionHash');
    if (parsed.success === false) {
      return { state: 'rejected', reason: stringField(parsed, 'errorReason', 'message') || 'facilitator reported failure' };
    }
    if (!txHash || !TX_HASH_RE.test(txHash)) {
      // Reported success with nothing to verify it. Refusing to treat this as settled is the
      // whole point: a settlement string that cannot be opened on an explorer is not evidence,
      // and passing it along would let a fixture be written for a payment nobody can check.
      return {
        state: 'unknown',
        reason: `facilitator reported success without a usable transaction hash (got ${JSON.stringify(txHash ?? null)})`,
      };
    }
    return { state: 'settled', txHash, payer: stringField(parsed, 'payer') };
  }

  private async withTimeout(run: (signal: AbortSignal) => Promise<Response>): Promise<Response> {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeoutMs);
    try {
      return await run(controller.signal);
    } finally {
      clearTimeout(timer);
    }
  }
}

/** First present string among the given keys. Circle's field naming is the part I am least sure of. */
function stringField(obj: Record<string, unknown>, ...keys: string[]): string {
  for (const k of keys) {
    const v = obj[k];
    if (typeof v === 'string' && v !== '') return v;
  }
  return '';
}

/**
 * Maps a settle outcome to the receipt's `settlement` field.
 *
 * The ONLY value that reads as settled is a real transaction hash. Every other state maps to a
 * marker `capture-v1-fixture.ts` already refuses, so no failure mode here can produce committed
 * evidence of a payment that did not happen.
 */
export function settlementFor(outcome: SettleOutcome): string {
  switch (outcome.state) {
    case 'settled':
      return outcome.txHash;
    case 'unknown':
      return PENDING_SETTLEMENT;
    case 'rejected':
    case 'not-configured':
      return NOT_BROADCAST;
  }
}
