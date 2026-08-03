/**
 * Snapfall paid demo API — the x402 SELLER (PRD §14.3 C, FR-X402-001).
 *
 * Serves the three resources in the demo seed (PRD §15.2):
 *   /v1/company-profile     0.04 USDC — auto-approved under policy (AT-02)
 *   /v1/premium-dataset     4.00 USDC — over threshold, escalates, gets rejected (AT-03/AT-04)
 *   /v1/benchmark-summary   0.06 USDC — the cheaper alternative the agent adapts to
 *
 * Unpaid request  -> 402 + challenge on BOTH transports (header v2 + body v1).
 * Paid request    -> verifies the EIP-3009 authorization, then 200 + data + receipt.
 *
 * SETTLEMENT CAVEAT, stated precisely because the short version hid the part that matters.
 * This server verifies the buyer's EIP-3009 signature and authorization fields in full. It does
 * NOT broadcast `transferWithAuthorization` until CIRCLE_API_KEY is configured, because that is
 * the facilitator's job (V3). So with no key: the handshake is cryptographically real, no money
 * moves, the receipt says NOT_BROADCAST -- and the resource is still handed over.
 *
 * That last clause is the one worth saying out loud. It is a deliberate local-demo concession,
 * not an accident and not a claim that payment occurred: it is what lets `npm run demo:loop` and
 * the spine run exercise the whole path with no Circle account. It is confined to loopback (see
 * UNSETTLED_DELIVERY_OK) so a seller reachable from a network never gives goods away, and every
 * such delivery is logged as UNSETTLED DELIVERY. Once a key exists, goods are released on a
 * confirmed settlement only.
 */

import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { verifyTypedData, type Address, type Hex } from 'viem';
import { releaseDecision } from './release-policy.js';
import {
  CircleFacilitator,
  settlementFor,
  NOT_BROADCAST,
  type PaymentRequirements,
  type SettleOutcome,
} from './facilitator.js';
import {
  encodeBase64Json,
  decodeBase64Json,
  formatUsdc,
  TRANSFER_WITH_AUTHORIZATION_TYPES,
  type AcceptOption,
  type PaymentChallenge,
  type PaymentPayload,
} from './x402.js';

const PORT = Number(process.env.PAID_API_PORT ?? 4021);

// One client for the process. Unconfigured until CIRCLE_API_KEY exists (V3), and while
// unconfigured the seller does not attempt a broadcast at all -- an attempted-and-failed
// broadcast is a different, worse claim than an honest "no facilitator configured".
const facilitator = new CircleFacilitator();
// Loopback, matching service.ts. Overridable for a container, but never silently 0.0.0.0.
const HOST = process.env.PAID_API_HOST ?? '127.0.0.1';

/**
 * Whether this seller may hand over a resource it could not confirm payment for.
 *
 * True only on loopback, and only because the alternative is worse for the two things that
 * depend on it: `npm run demo:loop` proves the whole x402 handshake with no Circle account, and
 * the spine run's beats 3 and 5 need a DELIVERED record before the run can reach the waterfall.
 * Both are local demos of a payment path whose broadcast half is not wired yet (V3).
 *
 * It is a constant rather than an inline check so that the concession has a name, and so the one
 * condition that makes it defensible -- nobody outside this machine can reach it -- is stated
 * once, next to the binding it depends on.
 */
const LOOPBACK_HOSTS = new Set(['127.0.0.1', '::1', 'localhost']);
const UNSETTLED_DELIVERY_OK = LOOPBACK_HOSTS.has(HOST);

/** Arc testnet — docs.arc.io/arc/references/connect-to-arc (verified 19 Jul 2026). */
const CHAIN_ID = Number(process.env.ARC_CHAIN_ID ?? 5042002);
const NETWORK = `eip155:${CHAIN_ID}`;

/** USDC on Arc testnet. Placeholder until C confirms the token address. */
const USDC_ADDRESS = (process.env.ARC_USDC_ADDRESS ??
  '0x0000000000000000000000000000000000000001') as Address;

/** Where the seller wants to be paid. Overridden by env in the real demo. */
const PAY_TO = (process.env.SELLER_ADDRESS ??
  '0x000000000000000000000000000000000000dEaD') as Address;

const MAX_TIMEOUT_SECONDS = 300;

interface Resource {
  /** Atomic USDC (6dp). */
  price: bigint;
  description: string;
  /** Returned once payment verifies. Deliberately small, synthetic, non-sensitive. */
  data: unknown;
}

const RESOURCES: Record<string, Resource> = {
  '/v1/company-profile': {
    price: 40_000n, // 0.04 USDC
    description: 'Competitor company profile',
    data: {
      company: 'Cursor',
      category: 'AI coding assistant',
      founded: 2022,
      pricing: { free: true, proMonthlyUsd: 20 },
      positioning: 'IDE-native autocomplete and chat',
    },
  },
  '/v1/premium-dataset': {
    price: 4_000_000n, // 4.00 USDC — the one the founder rejects on camera
    description: 'Premium market dataset (full competitive landscape)',
    data: { rows: 12_400, note: 'full dataset payload elided in demo' },
  },
  '/v1/benchmark-summary': {
    price: 60_000n, // 0.06 USDC — the cheaper alternative
    description: 'Coding-assistant benchmark summary',
    data: {
      benchmark: 'SWE-bench Verified',
      results: [
        { product: 'Cursor', score: 0.62 },
        { product: 'Copilot', score: 0.55 },
        { product: 'Cody', score: 0.48 },
      ],
    },
  },
};

/**
 * Nonces already spent, per resource. Replay protection is the seller's job:
 * an authorization is a bearer instrument until it is consumed.
 * In-memory is fine for a demo; production needs shared storage.
 */
/**
 * Nonces this process has accepted, and what became of them.
 *
 * A plain Set was enough while every purchase resolved in one request. It stopped being enough
 * the moment an UNKNOWN settlement could withhold the goods: the nonce was already consumed, so
 * the buyer's retry came back "nonce already spent (replay)" and the paid resource became
 * permanently unreachable. Withholding delivery is right; making the payment unrecoverable is
 * not, and telling the buyer to retry when retry cannot work is worse than either.
 *
 * A REPLAY is a different request reusing a nonce. The SAME authorization re-presenting itself
 * for the same resource is a retry, and it is answered from this record.
 */
interface NonceRecord {
  path: string;
  amount: string;
  /** The authorization's signer. A hold belongs to the buyer who paid, nobody else. */
  payer: string;
  /**
   * 'in-flight' from the moment this process starts talking to the facilitator, so a concurrent
   * request carrying the same authorization cannot broadcast it a second time; 'pending' when the
   * settlement is unconfirmed; 'delivered' once the goods went out.
   */
  state: 'in-flight' | 'delivered' | 'pending';
  settlement: string;
  at: string;
}
const spentNonces = new Map<string, NonceRecord>();

type NonceClaim =
  | { kind: 'claimed' }
  | { kind: 'in-flight' }
  | { kind: 'replay' }
  | { kind: 'held'; record: NonceRecord };

/**
 * Decides what this nonce means and claims it, in ONE synchronous block.
 *
 * Atomicity here IS the double-broadcast guard. Node interleaves requests only at an await, so a
 * check and a claim with nothing awaited between them cannot be raced; separate them by even one
 * await and two requests both pass the check before either claims. My previous attempt checked
 * inside verifyPayment and claimed after it resolved, with a signature verification awaited in
 * between, so it never closed the race it described.
 */
function claimNonce(nonce: string, path: string, amount: string, payer: string): NonceClaim {
  const seen = spentNonces.get(nonce);
  if (seen) {
    if (seen.state === 'in-flight') return { kind: 'in-flight' };
    // A hold belongs to the buyer who paid. Without the payer check any signer presenting that
    // nonce inherits it, which is the difference between "your payment is safe" and "claimable".
    const sameBuyer = seen.payer.toLowerCase() === payer.toLowerCase();
    if (seen.state === 'pending' && seen.path === path && seen.amount === amount && sameBuyer) {
      return { kind: 'held', record: seen };
    }
    return { kind: 'replay' };
  }
  spentNonces.set(nonce, {
    path, amount, payer, state: 'in-flight', settlement: '', at: new Date().toISOString(),
  });
  return { kind: 'claimed' };
}

function challengeFor(path: string, resource: Resource): PaymentChallenge {
  const accept: AcceptOption = {
    scheme: 'exact',
    network: NETWORK,
    amount: resource.price.toString(),
    asset: USDC_ADDRESS,
    payTo: PAY_TO,
    maxTimeoutSeconds: MAX_TIMEOUT_SECONDS,
    description: `${resource.description} (${formatUsdc(resource.price)} USDC)`,
    extra: { name: 'USD Coin', version: '2' },
  };
  return { x402Version: 2, accepts: [accept] };
}

function send(res: ServerResponse, status: number, body: unknown, headers: Record<string, string> = {}) {
  const json = JSON.stringify(body, null, 2);
  res.writeHead(status, {
    'content-type': 'application/json',
    'content-length': Buffer.byteLength(json),
    ...headers,
  });
  res.end(json);
}

/** 402 with the challenge on both transports. */
function sendChallenge(res: ServerResponse, challenge: PaymentChallenge, error?: string) {
  const body = error ? { ...challenge, error } : challenge;
  send(res, 402, body, { 'payment-required': encodeBase64Json(body) });
}

type Verdict = { ok: true } | { ok: false; reason: string };

/** Atomic USDC on the wire is a base-10 STRING (H1 §2). Not a number, not a decimal. */
const ATOMIC_RE = /^[0-9]{1,20}$/;

/**
 * Rejects a malformed authorization BEFORE any of it reaches BigInt() or .toLowerCase().
 *
 * Every one of these used to be an uncaught throw inside an async request handler, which on Node
 * is an unhandled rejection and takes the whole process down. A well-formed-looking client that
 * sent `value` as a JSON number instead of a string -- an honest mistake, not an attack -- killed
 * the seller for everyone. Verified: ten different malformed bodies, one fresh process each, all
 * ten exited.
 *
 * These are shape checks only. Whether the amount is CORRECT is verifyPayment's job; this just
 * guarantees it can ask the question without crashing.
 */
function authorizationShape(a: unknown): { ok: true } | { ok: false; reason: string } {
  if (typeof a !== 'object' || a === null) return { ok: false, reason: 'authorization is not an object' };
  const r = a as Record<string, unknown>;
  for (const field of ['value', 'validAfter', 'validBefore'] as const) {
    const v = r[field];
    if (typeof v !== 'string') return { ok: false, reason: `${field} must be a base-10 atomic string, got ${typeof v}` };
    if (!ATOMIC_RE.test(v)) return { ok: false, reason: `${field} is not a base-10 atomic amount` };
  }
  for (const field of ['from', 'to', 'nonce'] as const) {
    if (typeof r[field] !== 'string') return { ok: false, reason: `${field} must be a string, got ${typeof r[field]}` };
  }
  return { ok: true };
}

async function verifyPayment(payment: PaymentPayload, resource: Resource, path: string): Promise<Verdict> {
  const { signature, authorization: a } = payment.payload;

  if (payment.scheme !== 'exact') return { ok: false, reason: `unsupported scheme ${payment.scheme}` };
  if (payment.network !== NETWORK) return { ok: false, reason: `wrong network ${payment.network}, expected ${NETWORK}` };

  // Amount must be EXACT — over-payment is as wrong as under-payment, because the
  // policy engine reserved a specific number against the job budget (FR-PAY-006).
  if (BigInt(a.value) !== resource.price) {
    return { ok: false, reason: `expected ${resource.price} atomic USDC, got ${a.value}` };
  }
  if (a.to.toLowerCase() !== PAY_TO.toLowerCase()) {
    return { ok: false, reason: `wrong payee ${a.to}` };
  }

  const now = BigInt(Math.floor(Date.now() / 1000));
  if (now < BigInt(a.validAfter)) return { ok: false, reason: 'authorization not yet valid' };
  if (now >= BigInt(a.validBefore)) return { ok: false, reason: 'authorization expired' };

  // The nonce is claimed in handle(), synchronously, before anything is awaited. Checking it
  // HERE would be a check followed by `await verifySignature(...)`, and two requests arriving
  // together would both pass it before either claimed anything -- which is exactly what my
  // previous "reserve before the await" did while its comment claimed otherwise.

  // The actual cryptography: does this signature bind THIS authorization to `from`?
  // verifyTypedData does not merely return false on a malformed signature -- it THROWS
  // ("Invalid yParityOrV value") when the bytes cannot be parsed into (r, s, v) at all. A bad
  // signature is the most ordinary thing a real client gets wrong, so it belongs in the same
  // 402-with-a-reason bucket as every other verification failure, not in the outer catch as a
  // 500. The outer catch stays as the backstop for what neither of us anticipated.
  let valid: boolean;
  try {
    valid = await verifySignature(payment, a);
  } catch (e) {
    return { ok: false, reason: `signature is not parseable: ${(e as Error).message}` };
  }
  if (!valid) return { ok: false, reason: 'signature does not recover to authorization.from' };
  return { ok: true };
}

async function verifySignature(payment: PaymentPayload, a: PaymentPayload['payload']['authorization']): Promise<boolean> {
  const { signature } = payment.payload;
  return verifyTypedData({
    address: a.from as Address,
    domain: {
      name: 'USD Coin',
      version: '2',
      chainId: CHAIN_ID,
      verifyingContract: USDC_ADDRESS,
    },
    types: TRANSFER_WITH_AUTHORIZATION_TYPES,
    primaryType: 'TransferWithAuthorization',
    message: {
      from: a.from as Address,
      to: a.to as Address,
      value: BigInt(a.value),
      validAfter: BigInt(a.validAfter),
      validBefore: BigInt(a.validBefore),
      nonce: a.nonce as Hex,
    },
    signature: signature as Hex,
  });
}

/**
 * The x402 PaymentRequirements the facilitator is asked to enforce, rebuilt from OUR resource
 * table rather than echoed from the buyer's payload. Echoing the client's own terms back to the
 * facilitator would let a buyer nominate the amount and payee it settles against.
 */
function paymentRequirements(path: string, resource: Resource): PaymentRequirements {
  return {
    scheme: 'exact',
    network: NETWORK,
    maxAmountRequired: resource.price.toString(),
    resource: path,
    payTo: PAY_TO,
    asset: USDC_ADDRESS,
    maxTimeoutSeconds: MAX_TIMEOUT_SECONDS,
    extra: { name: 'USD Coin', version: '2' },
  };
}

/**
 * Verify then settle.
 *
 * verify() first because a pre-broadcast refusal is clean: nothing was submitted, so the seller
 * can decline outright. Once settle() is called the authorization has left this process and a
 * missing response is an UNKNOWN, not a failure -- settlementFor maps that to a marker the
 * fixture capture refuses, so an unknown outcome can never become committed evidence.
 */
async function settleWithFacilitator(
  payment: PaymentPayload,
  requirements: PaymentRequirements,
  path: string,
): Promise<SettleOutcome> {
  const pre = await facilitator.verify(payment, requirements);
  if (!pre.valid) {
    return { state: 'rejected', reason: pre.reason ?? 'facilitator verify failed' };
  }
  const outcome = await facilitator.settle(payment, requirements);
  if (outcome.state === 'unknown') {
    // Loud on purpose: money may have moved and nobody can tell from here. This is the line an
    // operator needs to find in the log before deciding whether to retry anything.
    console.error(
      `[seller] SETTLEMENT UNKNOWN ${path} — ${outcome.reason}. The authorization may still ` +
        'land on chain; reconcile against the payee balance before retrying.',
    );
  }
  return outcome;
}

// Nothing reachable from a request may end the process. The shape guard above covers what we
// know about; this covers what we do not. viem's signature recovery throws on a signature that
// does not recover ("Invalid yParityOrV value") and that is a WELL-FORMED payment with a bad
// signature -- the single most likely thing a real client gets wrong, and it killed the seller
// mid-demo with an ECONNRESET the client could not interpret.
const server = createServer(async (req: IncomingMessage, res: ServerResponse) => {
  try {
    await handle(req, res);
  } catch (err) {
    console.error(`[seller] 500 ${req.url ?? ''} — unhandled:`, err);
    if (!res.headersSent) {
      send(res, 500, { error: 'internal error' });
    } else {
      res.end();
    }
  }
});

async function handle(req: IncomingMessage, res: ServerResponse) {
  const path = (req.url ?? '').split('?')[0] ?? '';

  if (path === '/health') return send(res, 200, { ok: true, network: NETWORK, payTo: PAY_TO });

  const resource = RESOURCES[path];
  if (!resource) return send(res, 404, { error: 'no such resource', available: Object.keys(RESOURCES) });

  const challenge = challengeFor(path, resource);
  const header = req.headers['x-payment'];

  // FR-X402-001: no payment presented -> advertise the price.
  if (typeof header !== 'string' || header.length === 0) {
    console.log(`[seller] 402 ${path} — ${formatUsdc(resource.price)} USDC`);
    return sendChallenge(res, challenge);
  }

  const payment = decodeBase64Json<PaymentPayload>(header);
  if (!payment?.payload?.authorization) {
    console.log(`[seller] 402 ${path} — malformed X-PAYMENT`);
    return sendChallenge(res, challenge, 'malformed X-PAYMENT header');
  }

  // Shape before semantics. A 402 with a reason is the correct answer to a malformed payment;
  // exiting the process is not, and that is what every one of these used to do.
  const shape = authorizationShape(payment.payload.authorization);
  if (!shape.ok) {
    console.log(`[seller] 402 ${path} — malformed authorization: ${shape.reason}`);
    return sendChallenge(res, challenge, `malformed authorization: ${shape.reason}`);
  }

  // ── claim the nonce ─────────────────────────────────────────────────────────────────────
  //
  // Node runs one request at a time between awaits, so a check and a claim in the SAME
  // synchronous block is atomic against concurrent requests. Split them with any await and it is
  // not, however early the check looks. This is the whole of the double-broadcast guard.
  const auth = payment.payload.authorization;
  const claim = claimNonce(auth.nonce, path, auth.value, auth.from);

  if (claim.kind === 'in-flight') {
    console.log(`[seller] 402 ${path} — already settling: ${auth.nonce}`);
    return sendChallenge(res, challenge, 'this authorization is already being settled; wait for it');
  }
  if (claim.kind === 'replay') {
    console.log(`[seller] 402 ${path} — replay: ${auth.nonce}`);
    return sendChallenge(res, challenge, 'nonce already spent (replay)');
  }
  if (claim.kind === 'held') {
    // Answered from the record. Resubmitting a held authorization is worse than useless: if the
    // timed-out first attempt actually landed, a gateway that rejects the second as already-used
    // leaves a buyer who HAS paid stuck at a 402 forever, and every retry throws somebody's live
    // bearer instrument at the network again.
    const why =
      `settlement still unconfirmed (${claim.record.settlement}); this authorization is held for ` +
      `you, reference ${auth.nonce}, since ${claim.record.at}. It has NOT been resubmitted. ` +
      'Reconcile against the payee balance; the operator releases the resource once the transfer ' +
      'is confirmed.';
    console.log(`[seller] 402 ${path} — held, not resubmitted: ${auth.nonce}`);
    return sendChallenge(res, challenge, why);
  }

  const verdict = await verifyPayment(payment, resource, path);
  if (!verdict.ok) {
    // Release it. Claiming before verifying is what makes the guard atomic, but holding a nonce
    // on a request that never verified would let anyone lock a buyer's authorization by sending
    // a bad signature alongside it.
    spentNonces.delete(auth.nonce);
    console.log(`[seller] 402 ${path} — rejected: ${verdict.reason}`);
    return sendChallenge(res, challenge, verdict.reason);
  }

  const nonce = auth.nonce;

  // ── the broadcast ───────────────────────────────────────────────────────────────────────
  //
  // Everything above proves the authorization is valid; none of it moves money. USDC moves when
  // somebody submits transferWithAuthorization, and that is the facilitator's job. This is the
  // step that closes V1, and until CIRCLE_API_KEY exists it does not run at all and the receipt
  // keeps saying NOT_BROADCAST, exactly as before.
  //
  // The order matters: the nonce is already consumed, so a settle that lands cannot be replayed;
  // and settlement is resolved BEFORE the goods are handed over, so the receipt the buyer
  // records is the one the chain will agree with.
  const requirements = paymentRequirements(path, resource);
  // A throw between the claim and the outcome would otherwise leave this authorization in-flight
  // for the life of the process: no retry, no hold, no route back to the resource. An exception
  // after submitting is the same thing as a timeout -- unknown, not failed.
  let outcome: SettleOutcome;
  try {
    outcome = facilitator.isConfigured()
      ? await settleWithFacilitator(payment, requirements, path)
      : { state: 'not-configured' };
  } catch (e) {
    outcome = { state: 'unknown', reason: `settle threw: ${(e as Error).message}` };
  }

  // Goods are released on CONFIRMED payment only.
  //
  // 'rejected' is easy: nothing moved, so 402 and no data. 'unknown' is the one I got wrong
  // first time -- a timeout, a 5xx, or a success with no usable hash all left the resource being
  // handed over with settlement PENDING, which is a paid API giving away its product on the hope
  // that the money lands. The authorization may still settle, so this is NOT a refusal of the
  // payment; it is a refusal to deliver before the payment is confirmed. The nonce stays spent
  // either way, so a settle that does land cannot be replayed for a second copy.
  //
  // This guard used to read `outcome.state !== 'settled' && facilitator.isConfigured()`, and that
  // second clause quietly disabled the whole paragraph above. With no CIRCLE_API_KEY the outcome
  // is 'not-configured', isConfigured() is false, and the block was skipped -- so the seller
  // handed the paid resource over and stamped the receipt NOT_BROADCAST. Goods for free, under a
  // comment promising the opposite, in the DEFAULT configuration. Nothing was faked and no claim
  // was false, but the protection this paragraph describes did not exist.
  //
  // Unsettled delivery is still wanted: it is what lets `npm run demo:loop` and the spine run's
  // beats 3 and 5 work with no Circle account. But it is a LOCAL DEMO concession, not a property
  // of a paid API, so it is now explicit, announced, and confined to loopback. A seller reachable
  // from a network never gives away goods it could not confirm payment for -- see the HOST note
  // above, where this process was once accidentally exposed on conference wifi.
  const release = releaseDecision(outcome.state, UNSETTLED_DELIVERY_OK);
  if (release !== 'deliver') {
    if (release === 'refuse-unsettled') {
      // No facilitator AND reachable beyond loopback. Refuse rather than give the goods away.
      spentNonces.delete(nonce);
      const why =
        `settlement is impossible (no facilitator configured) and this seller is bound to ${HOST}, ` +
        `not loopback, so the resource is withheld rather than given away`;
      console.log(`[seller] 402 ${path} — ${why}`);
      return sendChallenge(res, challenge, why);
    } else if (outcome.state === 'rejected') {
      // Nothing moved, so the nonce is genuinely unused and the reservation is released --
      // keeping it would burn an authorization the buyer never got value for.
      spentNonces.delete(nonce);
      const why = `facilitator declined: ${outcome.reason}`;
      console.log(`[seller] 402 ${path} — ${why}`);
      return sendChallenge(res, challenge, why);
    } else {
      // UNKNOWN. The authorization may still land, so the nonce is held against THIS purchase:
      // nobody else can spend it, and this buyer can re-present the identical request. The goods
      // stay withheld until the settlement is confirmed.
      //
      // An `else` rather than a fall-through, because the loopback concession above must reach
      // the delivery path below. Written as a fall-through first, it warned and then refused.
      //
      // Honest limitation: confirming it needs a settlement-status read this client does not have,
      // because the shape of that call is one of the things V3 has to establish. Until then the
      // reference below is what an operator reconciles by hand against the payee balance.
      spentNonces.set(nonce, {
        path, amount: payment.payload.authorization.value, payer: payment.payload.authorization.from,
        state: 'pending', settlement: settlementFor(outcome), at: new Date().toISOString(),
      });
      const why =
        `settlement unconfirmed (${settlementFor(outcome)}); this authorization is held for you, ` +
        `reference ${nonce}. Reconcile against the payee balance before re-presenting it.`;
      console.log(`[seller] 402 ${path} — ${why}`);
      return sendChallenge(res, challenge, why);
    }
  }

  if (outcome.state === 'not-configured') {
    // Deliberate, announced, and confined to loopback by UNSETTLED_DELIVERY_OK. Never silent:
    // the whole defect was that this happened without anyone being told.
    console.warn(
      `[seller] UNSETTLED DELIVERY ${path} — no CIRCLE_API_KEY, so nothing was broadcast and no ` +
        `money moved. Releasing the resource because this seller is bound to ${HOST}. The ` +
        `authorization is cryptographically valid; the receipt says ${NOT_BROADCAST}.`,
    );
  }

  console.log(
    `[seller] 200 ${path} — paid ${formatUsdc(resource.price)} USDC by ${payment.payload.authorization.from}` +
      (outcome.state === 'settled' ? ` — settled ${outcome.txHash}` : ` — settlement ${settlementFor(outcome)}`),
  );

  // FR-X402-004: the receipt the buyer ties to its task + accounting category.
  const receipt = {
    resource: path,
    amount: payment.payload.authorization.value,
    asset: USDC_ADDRESS,
    network: NETWORK,
    payer: payment.payload.authorization.from,
    payee: PAY_TO,
    nonce: payment.payload.authorization.nonce,
    // A real transaction hash when Circle broadcast one, and a marker capture-v1-fixture.ts
    // already refuses otherwise. No path here invents a settlement string.
    settlement: settlementFor(outcome),
    facilitator: facilitator.isConfigured() ? facilitator.endpoints() : undefined,
  };

  // Recorded as delivered only now that the goods are actually going out, so a crash between
  // settle and send leaves the nonce held rather than burned.
  spentNonces.set(nonce, {
    path, amount: payment.payload.authorization.value, payer: payment.payload.authorization.from,
    state: 'delivered', settlement: receipt.settlement, at: new Date().toISOString(),
  });

  return send(res, 200, { data: resource.data, receipt }, {
    'x-payment-response': encodeBase64Json(receipt),
  });
}

// Loopback by default. `server.listen(PORT, cb)` binds 0.0.0.0 while the banner below prints
// 127.0.0.1, so the demo seller was reachable from the whole network on conference wifi while
// claiming otherwise. service.ts already binds this way; the seller did not.
server.listen(PORT, HOST, () => {
  console.log(`[seller] Snapfall paid demo API on http://${HOST}:${PORT}`);
  console.log(`[seller] network ${NETWORK} · payTo ${PAY_TO}`);
  for (const [path, r] of Object.entries(RESOURCES)) {
    console.log(`[seller]   ${path} — ${formatUsdc(r.price)} USDC`);
  }
});
