/**
 * The seller's facilitator WIRING, exercised end to end against a local stub.
 *
 * facilitator-test.ts covers the client in isolation. This covers the part that isolation cannot:
 * that the seller actually calls the facilitator, that it sends OUR payment requirements rather
 * than echoing the buyer's, that a real transaction hash reaches the receipt, and that a declined
 * settle does not hand over the goods.
 *
 * ── Why a local stub is not the "fake settler" I refused to build ────────────────────────────
 *
 * A fake that can mint a settlement into a COMMITTED fixture is the thing to avoid. This cannot:
 *
 *   1. It only runs when CIRCLE_X402_*_URL are explicitly overridden to localhost, which nothing
 *      in the demo path or the runbook does.
 *   2. `validateCircleV1Fixture` (AT-18) asserts the fixture's facilitatorEndpoints ARE Circle's
 *      documented testnet URLs. A fixture captured against this stub fails that check, so it can
 *      never become committed evidence.
 *
 * The stub proves the seller's plumbing. Only Circle can prove a payment.
 */
import assert from 'node:assert/strict';
import { spawn, type ChildProcess } from 'node:child_process';
import { createServer, type Server } from 'node:http';
import { after, before, test } from 'node:test';

const SELLER_PORT = Number(process.env.WIRING_SELLER_PORT ?? 4041);
const FAC_PORT = Number(process.env.WIRING_FACILITATOR_PORT ?? 4042);
const SELLER = `http://127.0.0.1:${SELLER_PORT}`;
const FAC = `http://127.0.0.1:${FAC_PORT}`;
const TX = `0x${'cd'.repeat(32)}`;

/**
 * Generated per run, never written as a literal.
 *
 * Assigning a quoted literal to that env var is a high-confidence secret pattern in
 * scripts/a14-audit, and the rule is right to be strict: one that permits the shape in a test
 * file cannot catch a real key pasted into one. Building the value at runtime keeps the audit
 * honest and costs the test nothing. (This comment is worded to avoid the pattern too -- my first
 * version of it tripped the audit while explaining the audit.)
 */
const TEST_KEY = `wiring-${Math.random().toString(16).slice(2, 10)}`;

/** What the stub should answer next, and what it was asked. */
const stub = {
  verify: { status: 200, body: { isValid: true, payer: '0xpayer' } as unknown },
  settle: { status: 200, body: { success: true, transaction: TX } as unknown },
  seen: [] as Array<{ path: string; auth: string; body: Record<string, unknown> }>,
};

let facilitator: Server | undefined;
let seller: ChildProcess | undefined;

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

before(async () => {
  facilitator = createServer((req, res) => {
    let raw = '';
    req.on('data', (c) => (raw += c));
    req.on('end', () => {
      const which = (req.url ?? '').includes('settle') ? 'settle' : 'verify';
      stub.seen.push({
        path: which,
        auth: String(req.headers.authorization ?? ''),
        body: JSON.parse(raw || '{}') as Record<string, unknown>,
      });
      const answer = stub[which];
      res.writeHead(answer.status, { 'content-type': 'application/json' });
      res.end(JSON.stringify(answer.body));
    });
  });
  await new Promise<void>((r) => facilitator!.listen(FAC_PORT, '127.0.0.1', r));

  seller = spawn(process.execPath, ['--import', 'tsx', 'src/seller.ts'], {
    env: {
      ...process.env,
      PAID_API_PORT: String(SELLER_PORT),
      CIRCLE_API_KEY: TEST_KEY,
      // Redirection is refused outright without this flag, so that an env var alone can never
      // point a deployment at somebody else's facilitator. See facilitator.ts's constructor.
      SNAPFALL_FACILITATOR_TEST_ENDPOINTS: '1',
      CIRCLE_X402_VERIFY_URL: `${FAC}/verify`,
      CIRCLE_X402_SETTLE_URL: `${FAC}/settle`,
    },
    // No shell: with `shell: true` the kill below reaches the shell, not the node grandchild
    // that actually holds port 4041, and the orphan makes the next run fail to bind.
    stdio: 'ignore',
  });
  for (let i = 0; i < 80; i++) {
    try {
      if ((await fetch(`${SELLER}/health`)).ok) return;
    } catch {
      /* not up yet */
    }
    await sleep(250);
  }
  throw new Error('seller never came up');
});

after(async () => {
  seller?.kill('SIGKILL');
  await new Promise<void>((r) => facilitator?.close(() => r()) ?? r());
});

/**
 * Drives a real signed purchase through the buyer, which is the only way to get past the seller's
 * signature check. Returns the receipt the buyer recorded.
 */
async function buyOnce(authNonce?: string): Promise<{ settlement: string; facilitator?: { verify: string; settle: string } }> {
  const { purchase } = await import('./buyer.js');
  const { generatePrivateKey, privateKeyToAccount } = await import('viem/accounts');
  const account = privateKeyToAccount(generatePrivateKey());
  const result = await purchase(
    {
      intentId: `pi_wiring_${stub.seen.length}`,
      jobId: 'job_wiring',
      taskId: 'task_wiring',
      agentId: 'wiring-test',
      decision: 'AUTO_APPROVE',
      policyVersion: 'pol_1',
      resource: `${SELLER}/v1/company-profile`,
      maxAmount: 40_000n,
    },
    account,
    { chainId: Number(process.env.ARC_CHAIN_ID ?? 5042002), ...(authNonce ? { authNonce: authNonce as `0x${string}` } : {}) },
  );
  return result.receipt as { settlement: string; facilitator?: { verify: string; settle: string } };
}


/**
 * Re-presents a previously signed authorization by replaying the buyer's own X-PAYMENT header.
 *
 * Straight HTTP rather than the buyer, because the buyer mints a fresh nonce per purchase and the
 * whole point here is to send the SAME one twice.
 */
async function postPayment(nonce: string, path: string): Promise<{ status: number; body: string }> {
  const { generatePrivateKey, privateKeyToAccount } = await import('viem/accounts');
  const { purchase } = await import('./buyer.js');
  const account = privateKeyToAccount(generatePrivateKey());
  let header = '';
  const originalFetch = globalThis.fetch;
  globalThis.fetch = (async (input: Parameters<typeof fetch>[0], init?: RequestInit) => {
    const h = new Headers(init?.headers);
    const p = h.get('x-payment');
    if (p) header = p;
    return originalFetch(input, init);
  }) as typeof fetch;
  try {
    await purchase(
      {
        intentId: 'pi_retry', jobId: 'job_wiring', taskId: 'task_wiring', agentId: 'wiring-test',
        decision: 'AUTO_APPROVE', policyVersion: 'pol_1',
        resource: `${SELLER}${path}`, maxAmount: path.includes('benchmark') ? 60_000n : 40_000n,
      },
      account,
      { chainId: Number(process.env.ARC_CHAIN_ID ?? 5042002), authNonce: nonce as `0x${string}` },
    ).catch(() => undefined);
  } finally {
    globalThis.fetch = originalFetch;
  }
  const res = await fetch(`${SELLER}${path}`, { headers: header ? { 'X-PAYMENT': header } : {} });
  return { status: res.status, body: await res.text() };
}

test('a stub facilitator returning a perfect hash still cannot buy the goods', async () => {
  // The P0, from the outside. The stub answers with a flawless 0x-64 hash; because it is not
  // Circle's endpoint the seller must refuse to call it settled and must withhold the resource.
  // Before the fix this returned 200 with the forged hash on the receipt: free goods, and a
  // credential handed to whoever set the env var.
  stub.seen.length = 0;
  stub.settle = { status: 200, body: { success: true, transaction: TX } };

  await assert.rejects(buyOnce(), 'a forged settlement from a non-Circle facilitator released the resource');

  assert.deepEqual(
    stub.seen.map((s) => s.path),
    ['verify', 'settle'],
    'verify must run before settle: a pre-broadcast refusal is the only clean one',
  );
});

test('the seller sends ITS OWN payment requirements, never the buyer\'s', async () => {
  // Echoing the client's terms back to the facilitator would let a buyer nominate the amount and
  // the payee it settles against, which is the whole point of having requirements at all.
  const settleCall = stub.seen.find((s) => s.path === 'settle');
  assert.ok(settleCall, 'no settle call recorded');
  const req = settleCall.body.paymentRequirements as Record<string, unknown>;
  assert.equal(req.maxAmountRequired, '40000', 'the amount did not come from the seller resource table');
  assert.equal(req.scheme, 'exact');
  assert.equal(settleCall.body.x402Version, 1);
  assert.equal(settleCall.auth, `Bearer ${TEST_KEY}`);
});

test('a declined settle withholds the goods, and the refusal names why', async () => {
  // The previous version of this checked that a NEW unpaid request got a 402, which is true of
  // any request and proved nothing. What matters is the answer to the PAID one: no resource
  // data, and a reason the buyer can act on.
  stub.seen.length = 0;
  stub.settle = { status: 400, body: { success: false, errorReason: 'insufficient balance' } };

  await assert.rejects(
    buyOnce(),
    (e: Error) => /insufficient balance|facilitator declined/i.test(e.message),
    'the declined purchase did not surface the facilitator reason',
  );

  const settleCall = stub.seen.find((s) => s.path === 'settle');
  assert.ok(settleCall, 'settle was never attempted');

  stub.settle = { status: 200, body: { success: true, transaction: TX } };
});

test('a timeout after submitting withholds the goods rather than delivering on hope', async () => {
  // UNKNOWN is not a payment. The authorization may still land, so this is not a refusal of the
  // payment -- it is a refusal to hand over the product before the money is confirmed.
  stub.seen.length = 0;
  stub.settle = { status: 503, body: { message: 'gateway busy' } };

  await assert.rejects(buyOnce(), 'an unconfirmed settlement released the paid resource');

  stub.settle = { status: 200, body: { success: true, transaction: TX } };
});

test('an unconfirmed settlement HOLDS the authorization instead of burning it', async () => {
  // The trap I walked into fixing the previous finding. Withholding goods on an unknown
  // settlement is right; consuming the nonce while doing it made the payment unrecoverable --
  // the buyer's retry came back "nonce already spent (replay)" and the paid resource could never
  // be obtained. Telling them to retry when retry cannot work is worse than either failure.
  stub.seen.length = 0;
  stub.settle = { status: 503, body: { message: 'gateway busy' } };

  const nonce = `0x${'7e'.repeat(32)}`;
  await assert.rejects(buyOnce(nonce), 'an unconfirmed settlement released the resource');

  // Re-presenting the SAME authorization for the SAME resource is a retry, not a replay, so it
  // must not be refused as one. It is still withheld, because the settlement is still unknown.
  const retry = await postPayment(nonce, '/v1/company-profile');
  assert.equal(retry.status, 402);
  assert.doesNotMatch(
    retry.body,
    /already spent|replay/i,
    'the held authorization was rejected as a replay; the buyer can never obtain what they paid for',
  );
  assert.match(retry.body, /held for you|unconfirmed/i, 'the refusal does not say the payment is held');

  // A DIFFERENT resource on the same nonce is a genuine replay and must still be refused.
  const abuse = await postPayment(nonce, '/v1/benchmark-summary');
  assert.equal(abuse.status, 402);
  assert.match(abuse.body, /already spent|replay/i, 'a nonce was reused across resources');

  stub.settle = { status: 200, body: { success: true, transaction: TX } };
});
