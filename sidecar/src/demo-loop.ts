/**
 * Full x402 buyer -> seller loop (PRD §14.3 C — the Tue Jul 21 deliverable).
 *
 * Boots our paid demo API, then walks the exact purchase sequence from the demo
 * script (PRD §15.1, 1:00–1:50) and asserts the safety properties hold:
 *
 *   1. 0.04 USDC company profile   — policy AUTO_APPROVE  -> 402, sign, retry, 200 (AT-02)
 *   2. 4.00 USDC premium dataset   — over threshold        -> NO signature produced (AT-03)
 *   3. 0.06 USDC benchmark summary — the cheaper adaptation -> 200 (AT-04)
 *   4. replayed authorization      — seller rejects        (nonce burned)
 *   5. price raised after approval — NO signature produced (AT-05 substitution)
 *
 * Runs entirely on localhost with an ephemeral key. No Circle account, no funded
 * wallet, no network. Settlement is NOT broadcast — see the caveat in seller.ts.
 *
 *   npm run demo:loop
 */

import { spawn, type ChildProcess } from 'node:child_process';
import { generatePrivateKey, privateKeyToAccount } from 'viem/accounts';
import { purchase, PolicyViolation, type ApprovedIntent } from './buyer.js';
import { formatUsdc } from './x402.js';

const PORT = 4099; // off the default so a running dev seller is not disturbed
const BASE = `http://127.0.0.1:${PORT}`;
const CHAIN_ID = 5042002; // Arc testnet

const account = privateKeyToAccount(generatePrivateKey());

let failures = 0;
function check(label: string, condition: boolean, detail = '') {
  const mark = condition ? 'PASS' : 'FAIL';
  if (!condition) failures++;
  console.log(`  [${mark}] ${label}${detail ? ` — ${detail}` : ''}`);
}

function intent(over: Partial<ApprovedIntent> & Pick<ApprovedIntent, 'resource' | 'maxAmount'>): ApprovedIntent {
  return {
    intentId: `pi_${Math.random().toString(16).slice(2, 10)}`,
    jobId: 'job_104',
    taskId: 'task_research_01',
    agentId: 'market-researcher',
    decision: 'AUTO_APPROVE',
    policyVersion: 'pol_7',
    ...over,
  };
}

async function waitForSeller(timeoutMs = 10_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${BASE}/health`);
      if (res.ok) return;
    } catch {
      /* not up yet */
    }
    await new Promise((r) => setTimeout(r, 150));
  }
  throw new Error(`seller did not become healthy on ${BASE}`);
}

async function main() {
  console.log(`\nSnapfall x402 loop — buyer ${account.address}\n`);

  const seller: ChildProcess = spawn(
    process.execPath,
    ['--import', 'tsx', new URL('./seller.ts', import.meta.url).pathname],
    { env: { ...process.env, PAID_API_PORT: String(PORT), ARC_CHAIN_ID: String(CHAIN_ID) }, stdio: 'inherit' },
  );

  try {
    await waitForSeller();
    console.log('');

    // ── 1. Auto-approved purchase (AT-02) ──
    console.log('1. company profile, 0.04 USDC, policy AUTO_APPROVE');
    const profile = await purchase(
      intent({ resource: `${BASE}/v1/company-profile`, maxAmount: 40_000n }),
      account,
      { chainId: CHAIN_ID },
    );
    check('402 -> signed -> retried -> 200', profile.amountPaid === 40_000n, `paid ${formatUsdc(profile.amountPaid)} USDC`);
    check('data returned', (profile.data as { company?: string })?.company === 'Cursor');
    check('receipt binds payer', profile.receipt.payer.toLowerCase() === account.address.toLowerCase());
    check('signature recorded for audit', profile.authorizationSignature.startsWith('0x'));

    // ── 2. Over-threshold purchase must never reach a signature (AT-03) ──
    console.log('\n2. premium dataset, 4.00 USDC, policy HUMAN_APPROVAL_REQUIRED');
    let signedAnyway = false;
    try {
      await purchase(
        intent({
          resource: `${BASE}/v1/premium-dataset`,
          maxAmount: 4_000_000n,
          decision: 'HUMAN_APPROVAL_REQUIRED',
        }),
        account,
        { chainId: CHAIN_ID },
      );
      signedAnyway = true;
    } catch (e) {
      check('refused before signing', e instanceof PolicyViolation, (e as Error).message);
    }
    check('no signature was produced', !signedAnyway);

    // ── 3. The cheaper alternative the agent adapts to (AT-04) ──
    console.log('\n3. benchmark summary, 0.06 USDC, the adaptation after rejection');
    const bench = await purchase(
      intent({ resource: `${BASE}/v1/benchmark-summary`, maxAmount: 60_000n }),
      account,
      { chainId: CHAIN_ID },
    );
    check('402 -> signed -> retried -> 200', bench.amountPaid === 60_000n, `paid ${formatUsdc(bench.amountPaid)} USDC`);
    check('benchmark data returned', Array.isArray((bench.data as { results?: unknown[] })?.results));

    // ── 4. Replay protection ──
    console.log('\n4. replayed authorization');
    const replay = await fetch(`${BASE}/v1/company-profile`, {
      method: 'GET',
      headers: {
        'X-PAYMENT': Buffer.from(
          JSON.stringify({
            x402Version: 2,
            scheme: 'exact',
            network: `eip155:${CHAIN_ID}`,
            payload: {
              signature: profile.authorizationSignature,
              authorization: {
                from: account.address,
                to: profile.receipt.payee,
                value: '40000',
                validAfter: '0',
                validBefore: String(Math.floor(Date.now() / 1000) + 300),
                nonce: profile.receipt.nonce,
              },
            },
          }),
        ).toString('base64'),
      },
    });
    check('spent nonce rejected', replay.status === 402, `HTTP ${replay.status}`);

    // ── 5. Substitution attack (AT-05): approved for 0.04, seller wants 0.06 ──
    console.log('\n5. substitution — approval bound to 0.04, resource costs 0.06');
    let substituted = false;
    try {
      await purchase(
        intent({ resource: `${BASE}/v1/benchmark-summary`, maxAmount: 40_000n }),
        account,
        { chainId: CHAIN_ID },
      );
      substituted = true;
    } catch (e) {
      check('refused before signing', e instanceof PolicyViolation, (e as Error).message);
    }
    check('no signature was produced', !substituted);

    // ── Demo arithmetic (PRD §15.2) ──
    console.log('\n6. demo totals');
    const spend = profile.amountPaid + bench.amountPaid;
    check('total external spend is 0.10 USDC', spend === 100_000n, `${formatUsdc(spend)} USDC`);

    console.log(
      failures === 0
        ? '\nx402 loop green: 402 -> sign -> retry -> 200, with policy gates holding.\n'
        : `\n${failures} check(s) FAILED.\n`,
    );
  } finally {
    seller.kill();
  }

  process.exit(failures === 0 ? 0 : 1);
}

main().catch((e) => {
  console.error('\nloop failed:', e);
  process.exit(1);
});
