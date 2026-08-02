/**
 * V1 FIXTURE CAPTURE: record a REAL four-cent x402 purchase as committed evidence.
 *
 * V1's done-when (docs/WORK-SPLIT.md §2) is: "a real four-cent purchase completes on testnet
 * and the raw request/response pair is committed as a fixture." This script is the only
 * sanctioned way to produce `sidecar/fixtures/v1-circle-payment.json`, and CI verifies that
 * file the moment it exists (.github/workflows/ci.yml, AT-18).
 *
 * THE FIXTURE MUST NEVER COME FROM A SIMULATED OR MOCKED PURCHASE. That is the entire point
 * of the artifact: it is the proof that money actually moved, not that our code compiles.
 * So this script deliberately has NO dry-run flag, NO offline mode, NO mock seller, and it
 * does not boot anything (contrast `npm run demo:loop`, which spawns a seller, generates an
 * ephemeral key and asserts against placeholders: that loop is cryptographically real and
 * financially a dry run, and its output is NOT admissible here).
 *
 * Five refusals stand between an operator and a written fixture. Every one of them is a
 * refusal, never a warning that gets papered over:
 *
 *   1. Required env unset. Reported as one list, before a key is ever loaded.
 *   2. Placeholder money. `ARC_USDC_ADDRESS` / `SELLER_ADDRESS` still holding seller.ts's
 *      placeholders (0x..01 / 0x..dEaD) means the EIP-712 domain is not real USDC and the
 *      payee is not a real payee. Refused.
 *   3. Wrong chain. Arc testnet 5042002 only (SEC-010: testnet assets exclusively).
 *   4. Wrong terms. The approved intent PINS merchant, asset, network and amount = 40000
 *      atomic USDC, so buyer.ts's own pre-sign equality checks (the AT-05 defence) are what
 *      enforce "four cents, to the real payee, in real USDC". A resource priced at anything
 *      else fails PRICE_CHANGED before a signature exists. No validation is duplicated here.
 *   5. No settlement. If the seller's receipt still says `NOT_BROADCAST`, nothing reached the
 *      chain, so no fixture is written. The raw evidence is parked at
 *      `v1-circle-payment.json.pending_chain` (a labeled pending record, never a fabricated
 *      one, matching the daemon's *.pending_chain discipline) and the script exits non-zero.
 *      That sibling file is a local artifact: do not commit it, it proves nothing.
 *
 * The purchase itself is the SHIPPED path: `purchase()` from buyer.ts, unmodified. Raw HTTP is
 * captured by wrapping `globalThis.fetch` for the duration of the call, so the recording
 * observes the real exchange instead of reimplementing it. Nothing about the request is
 * synthesized.
 *
 * Env (see sidecar/.env.example; runbook: docs/V3-CIRCLE-SETUP.md):
 *   V1_FIXTURE_RESOURCE   full http(s) URL of the 0.04 USDC resource to buy (this file only)
 *   TREASURY_PRIVATE_KEY  testnet-only signer, read by buyer.ts loadSigner()
 *   ARC_USDC_ADDRESS      real Arc-testnet USDC; the EIP-712 verifyingContract (seller.ts too)
 *   SELLER_ADDRESS        the real payee the seller quotes (seller.ts too)
 *   ARC_CHAIN_ID          optional, must be 5042002 if set (seller.ts too)
 *
 *   npm run capture:v1-fixture
 */

import { access, mkdir, writeFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { purchase, loadSigner, type ApprovedIntent, type PurchaseResult } from './buyer.js';
import { DRY_RUN_SETTLEMENTS, isConfirmedSettlement } from './settlement-markers.js';
import {
  CIRCLE_TESTNET_FACILITATOR_ENDPOINTS,
  validateCircleV1Fixture,
} from './circle-facilitator-fixture.js';
import { formatUsdc } from './x402.js';

/** Arc testnet. SEC-010: the demo runs on testnet assets exclusively. */
const ARC_TESTNET_CHAIN_ID = 5042002;

/** V1 is specifically a FOUR-CENT purchase (PRD §15.2, the 0.04 USDC company profile). */
const FOUR_CENTS_ATOMIC = 40_000n;

/** seller.ts's stand-ins. Signing against either means the purchase is not real money. */
const PLACEHOLDER_ADDRESSES = new Set(
  [
    '0x0000000000000000000000000000000000000000',
    '0x0000000000000000000000000000000000000001', // seller.ts ARC_USDC_ADDRESS default
    '0x000000000000000000000000000000000000dead', // seller.ts SELLER_ADDRESS default
  ].map((a) => a.toLowerCase()),
);

/** Receipt values that mean "nothing was broadcast". Refusal 5. */
/**
 * The facilitator endpoints this purchase actually settled through.
 *
 * seller.ts puts them on the receipt when a broadcast was attempted. An older seller, or one with
 * no facilitator configured, reports nothing -- and in that case the settlement is a dry-run
 * marker anyway, so refusal 5 stops the run before refusal 6 can read this.
 */
function settledEndpoints(result: { receipt?: { facilitator?: { verify?: string; settle?: string } } }): {
  verify: string;
  settle: string;
} | null {
  const f = result?.receipt?.facilitator;
  // BOTH legs or nothing. Defaulting a missing leg to Circle's URL is how the original version
  // of this made a claim it had never observed: a receipt with no provenance at all came out
  // asserting Circle settled it. A settled payment that cannot say who settled it is not
  // evidence, so the caller parks it instead.
  if (!f || typeof f.verify !== 'string' || typeof f.settle !== 'string') return null;
  return { verify: f.verify, settle: f.settle };
}


/** Resolved from the module, not from cwd, so the script works from any directory. */
const FIXTURE_PATH = fileURLToPath(new URL('../fixtures/v1-circle-payment.json', import.meta.url));
const PENDING_PATH = `${FIXTURE_PATH}.pending_chain`;

/** Request headers that must never be committed. `x-payment` is deliberately NOT here:
 *  the signed testnet authorization IS the evidence V1 asks for. */
const REDACTED_HEADERS = new Set(['authorization', 'cookie', 'proxy-authorization', 'set-cookie']);

// ── raw HTTP recording ──────────────────────────────────────────────────────

interface RecordedExchange {
  /** Which leg of the x402 loop this was. */
  phase: string;
  request: { method: string; url: string; headers: Record<string, string> };
  response: {
    status: number;
    statusText: string;
    headers: Record<string, string>;
    /** Verbatim response text. "Raw" is the requirement, so it is stored unparsed. */
    bodyText: string;
  };
}

/**
 * Normalize any `HeadersInit` shape (or a live `Headers`) to a lowercase record.
 * Typed against `unknown` on purpose: the DOM lib is not loaded in this package's tsconfig,
 * so the script must not lean on `HeadersInit`/`Request` type names.
 */
function headerRecord(headers: unknown): Record<string, string> {
  const out: Record<string, string> = {};
  if (headers === null || headers === undefined) return out;

  const put = (key: string, value: string) => {
    const name = key.toLowerCase();
    out[name] = REDACTED_HEADERS.has(name) ? '[redacted before commit]' : value;
  };

  // Array first: arrays also have `forEach`, and the entry-pair shape must win.
  if (Array.isArray(headers)) {
    for (const pair of headers) {
      if (!Array.isArray(pair)) continue;
      const key = pair[0];
      const value = pair[1];
      if (typeof key === 'string' && typeof value === 'string') put(key, value);
    }
    return out;
  }
  const iterable = headers as { forEach?: (cb: (value: string, key: string) => void) => void };
  if (typeof iterable.forEach === 'function') {
    iterable.forEach((value, key) => put(key, value));
    return out;
  }
  for (const [key, value] of Object.entries(headers as Record<string, unknown>)) {
    put(key, String(value));
  }
  return out;
}

function requestUrl(input: unknown): string {
  if (typeof input === 'string') return input;
  if (input instanceof URL) return input.href;
  const candidate = input as { url?: unknown };
  return typeof candidate.url === 'string' ? candidate.url : '[unrecognized fetch input]';
}

/**
 * Wrap `globalThis.fetch` so the real purchase is observed rather than re-implemented.
 * Returns the recorded exchanges plus a `restore()` the caller MUST run in a finally.
 */
function recordFetch(): { exchanges: RecordedExchange[]; restore: () => void } {
  const exchanges: RecordedExchange[] = [];
  const realFetch: typeof globalThis.fetch = globalThis.fetch;

  const recording: typeof globalThis.fetch = async (input, init) => {
    const response = await realFetch(input, init);
    // Clone BEFORE the buyer consumes the body; a Response body reads exactly once.
    let bodyText = '';
    try {
      bodyText = await response.clone().text();
    } catch (e) {
      bodyText = `[body unreadable: ${(e as Error).message}]`;
    }
    exchanges.push({
      // The probe is the only unpaid leg, so the presence of X-PAYMENT names the phase.
      phase: headerRecord(init?.headers)['x-payment'] ? 'pay' : 'probe',
      request: {
        method: init?.method ?? 'GET',
        url: requestUrl(input),
        headers: headerRecord(init?.headers),
      },
      response: {
        status: response.status,
        statusText: response.statusText,
        headers: headerRecord(response.headers),
        bodyText,
      },
    });
    return response;
  };

  globalThis.fetch = recording;
  return {
    exchanges,
    restore: () => {
      globalThis.fetch = realFetch;
    },
  };
}

// ── refusals 1 to 3: the environment ────────────────────────────────────────

interface CaptureConfig {
  resource: string;
  chainId: number;
  usdc: string;
  seller: string;
}

function isAddress(value: string): boolean {
  return /^0x[0-9a-fA-F]{40}$/.test(value);
}

function readConfig(): CaptureConfig {
  const refusals: string[] = [];
  const resource = process.env.V1_FIXTURE_RESOURCE ?? '';
  const usdc = process.env.ARC_USDC_ADDRESS ?? '';
  const seller = process.env.SELLER_ADDRESS ?? '';
  const chainIdRaw = process.env.ARC_CHAIN_ID ?? String(ARC_TESTNET_CHAIN_ID);

  if (!resource) {
    refusals.push(
      'V1_FIXTURE_RESOURCE is not set. It must be the full http(s) URL of the 0.04 USDC ' +
        'resource to buy, for example https://<paid-api-host>/v1/company-profile',
    );
  } else {
    let url: URL | null = null;
    try {
      url = new URL(resource);
    } catch {
      refusals.push(`V1_FIXTURE_RESOURCE is not a valid URL: ${resource}`);
    }
    if (url && url.protocol !== 'http:' && url.protocol !== 'https:') {
      refusals.push(`V1_FIXTURE_RESOURCE must be http(s), got ${url.protocol}`);
    }
  }

  if (!process.env.TREASURY_PRIVATE_KEY) {
    refusals.push(
      'TREASURY_PRIVATE_KEY is not set. The fixture must be signed by the real testnet ' +
        'treasury key, never an ephemeral one (see sidecar/.env.example).',
    );
  }

  for (const [name, value] of [
    ['ARC_USDC_ADDRESS', usdc],
    ['SELLER_ADDRESS', seller],
  ] as const) {
    if (!value) {
      refusals.push(`${name} is not set. A real purchase cannot be captured against a placeholder.`);
      continue;
    }
    if (!isAddress(value)) {
      refusals.push(`${name} is not a 20-byte hex address: ${value}`);
      continue;
    }
    if (PLACEHOLDER_ADDRESSES.has(value.toLowerCase())) {
      refusals.push(
        `${name} is still seller.ts's placeholder (${value}). ` +
          (name === 'ARC_USDC_ADDRESS'
            ? 'The EIP-712 verifyingContract must be real Arc-testnet USDC or the signature ' +
              'authorizes nothing.'
            : 'The payee must be the real seller address or no money moves.'),
      );
    }
  }

  const chainId = Number(chainIdRaw);
  if (!Number.isInteger(chainId) || chainId !== ARC_TESTNET_CHAIN_ID) {
    refusals.push(
      `ARC_CHAIN_ID must be ${ARC_TESTNET_CHAIN_ID} (Arc testnet) to capture the V1 fixture; ` +
        `got ${chainIdRaw}. SEC-010 allows testnet assets only.`,
    );
  }

  if (refusals.length > 0) {
    throw new Error(
      `refusing to capture the V1 fixture, ${refusals.length} problem(s):\n` +
        refusals.map((r) => `  - ${r}`).join('\n') +
        '\n\nThis fixture is the proof that a real four-cent purchase settled on testnet. ' +
        'It is never written from a simulated run. Runbook: docs/V3-CIRCLE-SETUP.md',
    );
  }

  return { resource, chainId, usdc, seller };
}

// ── fixture assembly ────────────────────────────────────────────────────────

function buildFixture(
  config: CaptureConfig,
  payer: string,
  result: PurchaseResult,
  exchanges: RecordedExchange[],
): Record<string, unknown> {
  return {
    // The AT-18 contract fields validateCircleV1Fixture asserts. Everything below them is
    // additional evidence the validator deliberately ignores.
    x402Version: 1,
    // The endpoints the SELLER reports having settled through, falling back to the documented
    // Circle ones only when the receipt does not say. Hardcoding them here made the fixture
    // assert something it had not observed: any settlement, from any facilitator, was written
    // out claiming Circle's URLs, and AT-18 then validated that claim happily. Refusal 6 below
    // is what turns this from a decorative field into a check.
    // Refusal 6 runs before this is written, so by here the provenance is present and Circle's.
    facilitatorEndpoints: settledEndpoints(result) ?? CIRCLE_TESTNET_FACILITATOR_ENDPOINTS,

    x402VersionNote:
      '1 is the AT-18 fixture-contract version asserted by validateCircleV1Fixture, not the ' +
      'wire version. The recorded exchange used the x402 v2 transports (payment-required ' +
      'response header, X-PAYMENT payload with x402Version 2); our seller emits the v1 body ' +
      'challenge alongside it. See exchanges[] for what actually crossed the wire.',
    facilitatorEndpointSource:
      'sidecar/src/circle-facilitator-fixture.ts CIRCLE_TESTNET_FACILITATOR_ENDPOINTS: the ' +
      'Circle Gateway testnet endpoints this sidecar is configured to settle through. Recorded ' +
      'as configuration, not scraped from this exchange, because a signed EIP-3009 ' +
      'authorization is a bearer instrument and the buyer cannot observe which facilitator the ' +
      'resource host submits it to (docs/handshakes/H3-sidecar-api.md §8).',

    capturedAt: new Date().toISOString(),
    capturedBy: 'sidecar/src/capture-v1-fixture.ts (npm run capture:v1-fixture)',
    requirement: 'V1 (docs/WORK-SPLIT.md §2) proven via AT-18 (docs/PRD.md §12)',

    network: { chainId: config.chainId, caip2: `eip155:${config.chainId}` },
    approvedTerms: {
      resource: config.resource,
      merchant: config.seller,
      asset: config.usdc,
      amountAtomic: FOUR_CENTS_ATOMIC.toString(),
      amountUsdc: formatUsdc(FOUR_CENTS_ATOMIC),
      note:
        'Pinned into the ApprovedIntent before the purchase, so buyer.ts refused to sign ' +
        'anything that did not match these exact terms (the AT-05 pre-sign equality checks).',
    },
    payer,
    purchase: {
      amountPaidAtomic: result.amountPaid.toString(),
      amountPaidUsdc: formatUsdc(result.amountPaid),
      authorizationSignature: result.authorizationSignature,
      receipt: result.receipt,
    },

    /** The raw request/response pair V1 asks for, observed on the shipped purchase path. */
    exchanges,
  };
}

// ── main ────────────────────────────────────────────────────────────────────

async function exists(path: string): Promise<boolean> {
  try {
    await access(path);
    return true;
  } catch {
    return false;
  }
}

async function writeJson(path: string, value: unknown): Promise<void> {
  await mkdir(fileURLToPath(new URL('../fixtures/', import.meta.url)), { recursive: true });
  await writeFile(path, `${JSON.stringify(value, null, 2)}\n`, 'utf8');
}

async function main(): Promise<void> {
  // Committed evidence is not silently replaced. If a fixture is already there, the operator
  // decides whether to delete it; a re-capture must be a deliberate act.
  if (await exists(FIXTURE_PATH)) {
    throw new Error(
      `${FIXTURE_PATH} already exists. It is committed V1 evidence and will not be ` +
        'overwritten. Delete it deliberately if you are re-capturing.',
    );
  }

  const config = readConfig();
  const account = loadSigner();

  console.log('\nV1 fixture capture: a REAL four-cent purchase, or nothing.\n');
  console.log(`  resource : ${config.resource}`);
  console.log(`  network  : eip155:${config.chainId}`);
  console.log(`  asset    : ${config.usdc}`);
  console.log(`  payee    : ${config.seller}`);
  console.log(`  payer    : ${account.address}`);
  console.log(`  price    : ${formatUsdc(FOUR_CENTS_ATOMIC)} USDC (pinned)\n`);

  const host = new URL(config.resource).hostname;
  if (host === 'localhost' || host === '127.0.0.1' || host === '::1') {
    console.log(
      '  note: the resource is on loopback. That is fine, our paid API is our own service, ' +
        'but the settlement check below is what decides whether this run counts.\n',
    );
  }

  // Every approved term is pinned, so buyer.ts's own equality checks enforce the four cents,
  // the real payee, the real token and the right chain. Refusal 4 lives inside purchase().
  const intent: ApprovedIntent = {
    intentId: 'v1_fixture_capture',
    jobId: 'job_v1_fixture',
    taskId: 'task_v1_capture',
    agentId: 'v1-fixture-capture',
    resource: config.resource,
    maxAmount: FOUR_CENTS_ATOMIC,
    // A human deliberately ran this command with the terms above, which is exactly what
    // HUMAN_APPROVED means. It is not an automated policy decision.
    decision: 'HUMAN_APPROVED',
    policyVersion: 'v1-fixture-capture',
    merchant: config.seller,
    amount: FOUR_CENTS_ATOMIC,
    asset: config.usdc,
    network: `eip155:${config.chainId}`,
  };

  const recorder = recordFetch();
  let result: PurchaseResult;
  try {
    result = await purchase(intent, account, { chainId: config.chainId });
  } finally {
    recorder.restore();
  }

  const fixture = buildFixture(config, account.address, result, recorder.exchanges);

  // ── Refusal 5: no settlement, no fixture. ──
  const settlement = String(result.receipt.settlement ?? '');
  if (settlement === '' || DRY_RUN_SETTLEMENTS.has(settlement.toUpperCase())) {
    await writeJson(PENDING_PATH, fixture);
    console.error(
      `\nSTOPPED, honestly. The seller's receipt reports settlement "${settlement || '(empty)'}", ` +
        'which means the authorization was verified but never broadcast. Nothing settled on ' +
        'Arc, so this is not the real four-cent purchase V1 requires and NO fixture was ' +
        'written.\n\n' +
        `  raw evidence parked at: ${PENDING_PATH}\n` +
        '  (local artifact, do not commit: it proves the loop, not the payment)\n\n' +
        'Wire a real Circle Gateway facilitator broadcast first. Runbook: ' +
        'docs/V3-CIRCLE-SETUP.md\n',
    );
    process.exit(1);
  }
  // ── Refusal 6: settled, but not by Circle. ──
  //
  // A transaction hash proves money moved; it does not prove WHO broadcast it. The submission's
  // claim is specifically that Circle's facilitator settled the payment, and a fixture captured
  // against a local stub or any other facilitator would otherwise be written out asserting
  // Circle's endpoints because they used to be hardcoded above.
  const used = settledEndpoints(result);
  if (!used) {
    await writeJson(PENDING_PATH, fixture);
    console.error(
      '\nSTOPPED, honestly. The receipt reports a settlement but does not say which facilitator ' +
        'produced it.\n\n' +
        '  Evidence has to name its own source. A transaction hash proves money moved; only the ' +
        'facilitator provenance supports the claim that CIRCLE moved it, which is what V1 and ' +
        'AT-18 assert.\n' +
        `  raw evidence parked at: ${PENDING_PATH}\n\n` +
        'This usually means the seller predates the facilitator wiring. Rebuild it and capture ' +
        'again.\n',
    );
    process.exit(1);
  }
  for (const leg of ['verify', 'settle'] as const) {
    if (used[leg] !== CIRCLE_TESTNET_FACILITATOR_ENDPOINTS[leg]) {
      await writeJson(PENDING_PATH, fixture);
      console.error(
        `\nSTOPPED, honestly. This payment settled through ${used[leg]} for the ${leg} leg, not ` +
          `Circle's documented ${CIRCLE_TESTNET_FACILITATOR_ENDPOINTS[leg]}.\n\n` +
          '  A transaction hash proves money moved. It does not prove Circle moved it, and that ' +
          'is the specific claim V1 and AT-18 make.\n' +
          `  raw evidence parked at: ${PENDING_PATH}\n\n` +
          'If you were pointing at a local facilitator stub, unset CIRCLE_X402_VERIFY_URL and ' +
          'CIRCLE_X402_SETTLE_URL and capture again against the real service.\n',
      );
      process.exit(1);
    }
  }

  // ── Refusal 7: a settlement nobody can open is not evidence. ──
  //
  // This used to be a `note` that still wrote the fixture, which meant any string outside the
  // dry-run set -- "settled", "ok", anything -- became committed proof of a payment. Refusal 5
  // only covers the KNOWN markers, so without this the gate's coverage depended on a fabricator
  // choosing a word we had already thought of.
  if (!isConfirmedSettlement(settlement)) {
    await writeJson(PENDING_PATH, fixture);
    console.error(
      `\nSTOPPED, honestly. Settlement "${settlement}" is not a 32-byte transaction hash, so ` +
        'nothing here can be opened on an explorer and checked.\n\n' +
        `  raw evidence parked at: ${PENDING_PATH}\n\n` +
        'A fixture is a claim that a specific transaction settled. It has to name one.\n',
    );
    process.exit(1);
  }

  // Never write something CI would reject: the AT-18 validator runs before the file lands.
  validateCircleV1Fixture(fixture);
  await writeJson(FIXTURE_PATH, fixture);

  console.log(
    `\nCaptured: ${formatUsdc(result.amountPaid)} USDC paid, settlement ${settlement}\n` +
      `  fixture: ${FIXTURE_PATH}\n` +
      `  exchanges recorded: ${recorder.exchanges.map((e) => `${e.phase} -> ${e.response.status}`).join(', ')}\n\n` +
      'Verify it the way CI will, then commit it:\n' +
      '  npm run verify:circle-facilitator-fixture -- fixtures/v1-circle-payment.json\n',
  );
}

main().catch((e: unknown) => {
  console.error(`\nV1 fixture capture failed: ${(e as Error).message}\n`);
  process.exit(1);
});
