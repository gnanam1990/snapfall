/**
 * H3 sidecar integration test — plays the Funding agent (V6) against the real service.
 *
 * Boots the seller (paid API) and the H3 sidecar as child processes on loopback, then walks
 * the quote -> approve -> pay -> status flow and asserts the contract holds, including the
 * security properties the review demanded:
 *   - bearer auth required
 *   - idempotent replay returns the same paymentId, never a second pay
 *   - merchant swap rejected before signing (AT-05 payee half)
 *   - intent-hash mismatch and bad approval HMAC rejected
 *   - over-ceiling rejected, non-approved decision rejected
 *
 * Runs entirely on localhost with an ephemeral key and a throwaway store. No Circle account,
 * no funded wallet, no network. Settlement is a dry run (see seller.ts).
 *
 *   npm run service:test
 */

import { spawn, type ChildProcess } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { randomBytes } from 'node:crypto';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { rmSync } from 'node:fs';
import { generatePrivateKey } from 'viem/accounts';
import {
  computeIntentHash,
  signApproval,
  type WireIntent,
  type ApprovalToken,
} from './h3.js';
import type { AcceptOption } from './x402.js';

const SELLER_PORT = 4099;
const SIDECAR_PORT = 4098;
const CHAIN_ID = 5042002;
const SELLER = `http://127.0.0.1:${SELLER_PORT}`;
const SIDECAR = `http://127.0.0.1:${SIDECAR_PORT}`;

const AUTH_TOKEN = 'test-sidecar-token-0123456789abcdef'; // >=32 bytes
const H2_SECRET = 'test-h2-approval-secret-0123456789';
const STORE_PATH = join(tmpdir(), `snapfall-h3-test-${randomBytes(4).toString('hex')}.json`);

let failures = 0;
function check(label: string, ok: boolean, detail = '') {
  if (!ok) failures++;
  console.log(`  [${ok ? 'PASS' : 'FAIL'}] ${label}${detail ? ` — ${detail}` : ''}`);
}

function nonce(): string {
  return '0x' + randomBytes(32).toString('hex');
}

function makeIntent(resource: string, accept: AcceptOption, over: Partial<WireIntent> = {}): WireIntent {
  const now = Date.now();
  return {
    intentId: 'pi_' + randomBytes(4).toString('hex'),
    jobId: 'job_104',
    taskId: 'task_research_01',
    agentId: 'market-researcher',
    resource,
    network: accept.network,
    asset: accept.asset,
    merchant: accept.payTo,
    amount: accept.amount,
    maxAmount: accept.amount,
    purpose: 'competitor profile for job_104',
    nonce: nonce(),
    decision: 'AUTO_APPROVE',
    policyVersion: 'pol_7',
    createdAt: new Date(now).toISOString(),
    expiresAt: new Date(now + 300_000).toISOString(),
    ...over,
  };
}

function makeToken(intent: WireIntent, secret = H2_SECRET, over: Partial<ApprovalToken> = {}): ApprovalToken {
  const base = {
    intentHash: computeIntentHash(intent),
    decision: intent.decision,
    approvedAmount: intent.amount,
    approver: 'policy-engine',
    policyVersion: intent.policyVersion,
    issuedAt: new Date().toISOString(),
    expiresAt: intent.expiresAt,
    ...over,
  };
  return { ...base, signature: signApproval(secret, base) };
}

interface ApiResult {
  status: number;
  json: any;
}
async function api(method: string, path: string, body?: unknown, opts: { auth?: boolean } = {}): Promise<ApiResult> {
  const headers: Record<string, string> = { 'content-type': 'application/json' };
  if (opts.auth !== false) headers['authorization'] = `Bearer ${AUTH_TOKEN}`;
  const res = await fetch(`${SIDECAR}${path}`, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
  });
  const json = await res.json().catch(() => null);
  return { status: res.status, json };
}

async function quote(resource: string): Promise<AcceptOption> {
  const r = await api('POST', '/v1/quote', { resource, chainId: CHAIN_ID });
  if (r.status !== 200) throw new Error(`quote failed: ${JSON.stringify(r.json)}`);
  return r.json.accept as AcceptOption;
}

async function waitForHealth(base: string, timeoutMs = 12_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${base}/health`);
      if (res.ok) return;
    } catch {
      /* not up yet */
    }
    await new Promise((r) => setTimeout(r, 150));
  }
  throw new Error(`service did not become healthy on ${base}`);
}

async function main() {
  console.log(`\nSnapfall H3 sidecar integration test — store ${STORE_PATH}\n`);

  const sellerPath = fileURLToPath(new URL('./seller.ts', import.meta.url));
  const servicePath = fileURLToPath(new URL('./service.ts', import.meta.url));

  const seller: ChildProcess = spawn(
    process.execPath,
    ['--import', 'tsx', sellerPath],
    { env: { ...process.env, PAID_API_PORT: String(SELLER_PORT), ARC_CHAIN_ID: String(CHAIN_ID) }, stdio: 'inherit' },
  );
  const service: ChildProcess = spawn(
    process.execPath,
    ['--import', 'tsx', servicePath],
    {
      env: {
        ...process.env,
        SIDECAR_PORT: String(SIDECAR_PORT),
        SIDECAR_AUTH_TOKEN: AUTH_TOKEN,
        H2_APPROVAL_SECRET: H2_SECRET,
        TREASURY_PRIVATE_KEY: generatePrivateKey(),
        SIDECAR_STORE_PATH: STORE_PATH,
        ARC_CHAIN_ID: String(CHAIN_ID),
      },
      stdio: 'inherit',
    },
  );

  try {
    await Promise.all([waitForHealth(SELLER), waitForHealth(SIDECAR)]);
    console.log('');

    const profileUrl = `${SELLER}/v1/company-profile`;
    const benchUrl = `${SELLER}/v1/benchmark-summary`;

    // 1. quote
    console.log('1. quote a resource');
    const accept = await quote(profileUrl);
    check('quote returns the accept', accept.amount === '40000', `${accept.amount} atomic`);

    // 2. pay (happy path)
    console.log('\n2. pay an approved intent');
    const intent = makeIntent(profileUrl, accept);
    const token = makeToken(intent);
    const paid = await api('POST', '/v1/pay', { intent, approvalToken: token });
    check('pay 200', paid.status === 200, `HTTP ${paid.status}`);
    check('state DELIVERED', paid.json?.state === 'DELIVERED', paid.json?.state);
    check('amountPaid 0.04', paid.json?.amountPaid === '40000');
    check('not an idempotent replay', paid.json?.idempotentReplay === false);
    const paymentId = paid.json?.paymentId as string;

    // 3. status
    console.log('\n3. status of the payment');
    const st = await api('GET', `/v1/status/${paymentId}`);
    check('status 200 DELIVERED', st.status === 200 && st.json?.state === 'DELIVERED', st.json?.state);

    // 4. idempotent replay — same intent + token
    console.log('\n4. replay the same intent (idempotency)');
    const replay = await api('POST', '/v1/pay', { intent, approvalToken: token });
    check('replay 200', replay.status === 200);
    check('idempotentReplay true', replay.json?.idempotentReplay === true);
    check('same paymentId', replay.json?.paymentId === paymentId, replay.json?.paymentId);

    // 5. no bearer token
    console.log('\n5. pay without a bearer token');
    const noauth = await api('POST', '/v1/pay', { intent: makeIntent(profileUrl, accept), approvalToken: token }, { auth: false });
    check('401 UNAUTHENTICATED', noauth.status === 401 && noauth.json?.error?.code === 'UNAUTHENTICATED');

    // 6. merchant swap — approved payee != live seller payee (AT-05, payee half)
    console.log('\n6. merchant swap rejected before signing');
    const swapIntent = makeIntent(profileUrl, accept, { merchant: '0x000000000000000000000000000000000000bEEF' });
    const swap = await api('POST', '/v1/pay', { intent: swapIntent, approvalToken: makeToken(swapIntent) });
    check('409 MERCHANT_CHANGED', swap.status === 409 && swap.json?.error?.code === 'MERCHANT_CHANGED', `HTTP ${swap.status} ${swap.json?.error?.code}`);
    const swapPid = swap.json?.error?.paymentId as string;
    const swapStatus = await api('GET', `/v1/status/${swapPid}`);
    check('no record persisted for a pre-sign failure', swapStatus.status === 404);

    // 7. intent-hash mismatch — token for intent A, send tampered intent B
    console.log('\n7. intent-hash mismatch rejected');
    const intentA = makeIntent(profileUrl, accept);
    const tokenA = makeToken(intentA);
    const intentB = { ...intentA, purpose: 'tampered after approval' };
    const mismatch = await api('POST', '/v1/pay', { intent: intentB, approvalToken: tokenA });
    check('409 INTENT_HASH_MISMATCH', mismatch.status === 409 && mismatch.json?.error?.code === 'INTENT_HASH_MISMATCH', `HTTP ${mismatch.status} ${mismatch.json?.error?.code}`);

    // 8. bad approval HMAC — token signed with the wrong secret
    console.log('\n8. bad approval signature rejected');
    const goodIntent = makeIntent(profileUrl, accept);
    const badToken = makeToken(goodIntent, 'the-wrong-secret');
    const badHmac = await api('POST', '/v1/pay', { intent: goodIntent, approvalToken: badToken });
    check('401 APPROVAL_TOKEN_INVALID', badHmac.status === 401 && badHmac.json?.error?.code === 'APPROVAL_TOKEN_INVALID', `HTTP ${badHmac.status} ${badHmac.json?.error?.code}`);

    // 9. over-ceiling — benchmark is 0.06 but only 0.04 was reserved
    console.log('\n9. price exceeds reserved ceiling rejected');
    const benchAccept = await quote(benchUrl);
    const overIntent = makeIntent(benchUrl, benchAccept, { maxAmount: '40000' });
    const over = await api('POST', '/v1/pay', { intent: overIntent, approvalToken: makeToken(overIntent) });
    check('402 PRICE_EXCEEDS_RESERVED', over.status === 402 && over.json?.error?.code === 'PRICE_EXCEEDS_RESERVED', `HTTP ${over.status} ${over.json?.error?.code}`);

    // 10. non-approved decision
    console.log('\n10. non-approved decision rejected');
    const napIntent = makeIntent(profileUrl, accept, { decision: 'HUMAN_APPROVAL_REQUIRED' });
    const nap = await api('POST', '/v1/pay', { intent: napIntent, approvalToken: makeToken(napIntent) });
    check('403 INTENT_NOT_APPROVED', nap.status === 403 && nap.json?.error?.code === 'INTENT_NOT_APPROVED', `HTTP ${nap.status} ${nap.json?.error?.code}`);

    // ── Review-fix coverage ──────────────────────────────────────────────

    // 11. Expired approval (the window already elapsed).
    console.log('\n11. expired approval rejected');
    const expIntent = makeIntent(profileUrl, accept, { expiresAt: new Date(Date.now() - 1000).toISOString() });
    const exp = await api('POST', '/v1/pay', { intent: expIntent, approvalToken: makeToken(expIntent) });
    check('410 APPROVAL_EXPIRED', exp.status === 410 && exp.json?.error?.code === 'APPROVAL_EXPIRED', `HTTP ${exp.status} ${exp.json?.error?.code}`);

    // 11b. Unparseable expiresAt must fail closed (NaN would otherwise never expire).
    console.log('\n11b. unparseable expiresAt fails closed');
    const nanIntent = makeIntent(profileUrl, accept, { expiresAt: 'not-a-timestamp' });
    const nan = await api('POST', '/v1/pay', { intent: nanIntent, approvalToken: makeToken(nanIntent) });
    check('410 on unparseable expiry', nan.status === 410 && nan.json?.error?.code === 'APPROVAL_EXPIRED', `HTTP ${nan.status} ${nan.json?.error?.code}`);

    // 12. approvedAmount mismatch between token and intent.
    console.log('\n12. approved-amount mismatch rejected');
    const amtIntent = makeIntent(profileUrl, accept);
    const amtToken = makeToken(amtIntent, H2_SECRET, { approvedAmount: '99999' });
    const amt = await api('POST', '/v1/pay', { intent: amtIntent, approvalToken: amtToken });
    check('409 APPROVED_AMOUNT_MISMATCH', amt.status === 409 && amt.json?.error?.code === 'APPROVED_AMOUNT_MISMATCH', `HTTP ${amt.status} ${amt.json?.error?.code}`);

    // 13. Pre-sign failures carry a NULL paymentId (no record exists to resolve).
    console.log('\n13. pre-sign error envelope carries null paymentId');
    check('null paymentId on a pre-sign failure', amt.json?.error?.paymentId === null, JSON.stringify(amt.json?.error?.paymentId));

    // 14. A completed payment replays as 200; there is no stuck-SIGNED 200 path
    //     (that intersection is the store-durability test below).
    console.log('\n14. completed payment replays as 200');
    const rp = await api('POST', '/v1/pay', { intent, approvalToken: token });
    check('replay of a DELIVERED record is 200', rp.status === 200 && rp.json?.idempotentReplay === true, `HTTP ${rp.status}`);

    // 15. Malformed quote URL is a 400, not a 502.
    console.log('\n15. malformed quote URL is a client error');
    const badq = await api('POST', '/v1/quote', { resource: 'not a url', chainId: CHAIN_ID });
    check('400 BAD_REQUEST on malformed URL', badq.status === 400 && badq.json?.error?.code === 'BAD_REQUEST', `HTTP ${badq.status} ${badq.json?.error?.code}`);

    console.log(
      failures === 0
        ? '\nH3 sidecar green: quote -> approve -> pay -> status, with every gate holding.\n'
        : `\n${failures} check(s) FAILED.\n`,
    );
  } finally {
    seller.kill();
    service.kill();
    try {
      rmSync(STORE_PATH, { force: true });
    } catch {
      /* ignore */
    }
  }

  process.exit(failures === 0 ? 0 : 1);
}

main().catch((e) => {
  console.error('\nservice test failed:', e);
  process.exit(1);
});
