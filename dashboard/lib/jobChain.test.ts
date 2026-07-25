import assert from 'node:assert/strict';
import test from 'node:test';
import { loadJobSnapshot, jobChainInternals, JOB_STATUS } from './jobChain';
import type { RPCTransport } from './floatChain';

const JOB_VAULT = '0xF3830D7C3B8ca873bB0b277c0e179999e3d52681';
const FLOAT_POOL = '0xde9F58A997Cf7A3258D09A797Eb5546877dc86E5';
const JOB_ID = '0x' + 'ab'.repeat(32);

const config = {
  chainId: 5042002,
  rpcUrl: 'http://127.0.0.1:0',
  jobVaultAddress: JOB_VAULT,
  floatPoolAddress: FLOAT_POOL,
  explorerUrl: 'https://testnet.arcscan.app',
};

const w = (v: bigint | number) => BigInt(v).toString(16).padStart(64, '0');
const addrWord = (a: string) => a.replace(/^0x/, '').toLowerCase().padStart(64, '0');

/** Encode a Job struct the way the public getter returns it: 9 static words. */
function jobsReturn(over: Partial<{
  customer: string;
  operator: string;
  payment: bigint;
  budget: bigint;
  expenses: bigint;
  terms: string;
  delivery: string;
  deadline: bigint;
  status: number;
}> = {}) {
  const f = {
    customer: '0x1111111111111111111111111111111111111111',
    operator: '0x2222222222222222222222222222222222222222',
    payment: 25_000_000n,
    budget: 6_000_000n,
    expenses: 0n,
    terms: 'cd'.repeat(32),
    delivery: '00'.repeat(32),
    deadline: 1800000000n,
    status: 1,
    ...over,
  };
  return (
    '0x' +
    addrWord(f.customer) +
    addrWord(f.operator) +
    w(f.payment) +
    w(f.budget) +
    w(f.expenses) +
    f.terms +
    f.delivery +
    w(f.deadline) +
    w(f.status)
  );
}

function advanceReturn(principal: bigint, fee: bigint, open: boolean) {
  return '0x' + w(principal) + w(fee) + w(open ? 1 : 0);
}

/** A transport that answers by selector, so tests never touch a network. */
function fakeRPC(answers: {
  jobs?: string | (() => never);
  advance?: string | (() => never);
  rate?: string | (() => never);
}): { rpc: RPCTransport; calls: string[] } {
  const calls: string[] = [];
  const rpc = (async (method: string, params: unknown[]) => {
    assert.equal(method, 'eth_call');
    const call = (params as [{ to: string; data: string }])[0];
    const selector = call.data.slice(0, 10);
    calls.push(selector);
    const pick =
      selector === jobChainInternals.SELECTOR.jobs
        ? answers.jobs
        : selector === jobChainInternals.SELECTOR.openAdvanceOf
          ? answers.advance
          : answers.rate;
    if (typeof pick === 'function') pick();
    if (pick === undefined) throw new Error(`unexpected selector ${selector}`);
    return pick;
  }) as RPCTransport;
  return { rpc, calls };
}

test('reads the funded demo job from the vault struct, deriving the budget bar', async () => {
  const { rpc } = fakeRPC({
    jobs: jobsReturn({ expenses: 100_000n, status: 2 }),
    advance: advanceReturn(12_500_000n, 250_000n, true),
    rate: '0x' + w(5000),
  });

  const snap = await loadJobSnapshot(config, JOB_ID, rpc);

  assert.equal(snap.exists, true);
  assert.equal(snap.status, 'InProgress');
  assert.equal(snap.statusIndex, 2);
  assert.equal(snap.customer, '0x1111111111111111111111111111111111111111');
  assert.equal(snap.customerPaymentUsdc, '25000000');
  assert.equal(snap.maxOperatingBudgetUsdc, '6000000');
  assert.equal(snap.onchainExpensesUsdc, '100000');
  assert.equal(snap.budgetRemainingUsdc, '5900000');
  // 0.10 of 6.00 = 1.666% -> 166 bps, truncated like the contract would.
  assert.equal(snap.budgetUsedBps, 166);
  assert.equal(snap.orgRateBps, 5000);
});

test('the advance card carries principal, fee and the repayment the waterfall settles first', async () => {
  const { rpc } = fakeRPC({
    jobs: jobsReturn(),
    advance: advanceReturn(12_500_000n, 250_000n, true),
    rate: '0x' + w(5000),
  });

  const snap = await loadJobSnapshot(config, JOB_ID, rpc);

  assert.deepEqual(snap.advance, {
    principalUsdc: '12500000',
    feeUsdc: '250000',
    repaymentUsdc: '12750000',
    open: true,
    drawn: true,
  });
  // 25.00 escrowed - 12.75 repaid = 12.25 to the operator, the PRD §15.2 figure.
  assert.equal(snap.operatorNetUsdc, '12250000');
});

test('a closed advance still counts as drawn, and the operator net reflects it', async () => {
  const { rpc } = fakeRPC({
    jobs: jobsReturn({ status: 4 }),
    // Post-settlement: open=false but the principal is still recorded.
    advance: advanceReturn(12_500_000n, 250_000n, false),
    rate: '0x' + w(5500),
  });

  const snap = await loadJobSnapshot(config, JOB_ID, rpc);

  assert.equal(snap.status, 'Accepted');
  assert.equal(snap.advance?.open, false);
  assert.equal(snap.advance?.drawn, true, 'absence is a zero principal, not open=false');
  assert.equal(snap.operatorNetUsdc, '12250000');
  assert.equal(snap.orgRateBps, 5500, 'the flywheel already lifted the rate');
});

test('no advance drawn leaves the operator the whole escrow', async () => {
  const { rpc } = fakeRPC({
    jobs: jobsReturn(),
    advance: advanceReturn(0n, 0n, false),
    rate: '0x' + w(5000),
  });

  const snap = await loadJobSnapshot(config, JOB_ID, rpc);
  assert.equal(snap.advance?.drawn, false);
  assert.equal(snap.operatorNetUsdc, '25000000');
});

test('a job the vault never saw is reported as absent, not as an error or a zero job', async () => {
  const { rpc, calls } = fakeRPC({
    jobs: jobsReturn({ customer: '0x0000000000000000000000000000000000000000', payment: 0n, budget: 0n, status: 0 }),
  });

  const snap = await loadJobSnapshot(config, JOB_ID, rpc);

  assert.equal(snap.exists, false);
  assert.equal(snap.status, null);
  assert.equal(snap.customerPaymentUsdc, null, 'an unknown job must not read as 0.00 escrowed');
  assert.equal(snap.advance, null);
  assert.deepEqual(calls, [jobChainInternals.SELECTOR.jobs], 'no follow-up reads for a job that does not exist');
  assert.match(snap.explorer.jobVault, /\/address\/0xF3830D7C/);
});

test('the delivery hash is null until submitDelivery lands (SC-JV-004)', async () => {
  const pending = await loadJobSnapshot(
    config,
    JOB_ID,
    fakeRPC({ jobs: jobsReturn(), advance: advanceReturn(0n, 0n, false), rate: '0x' + w(5000) }).rpc,
  );
  assert.equal(pending.deliveryHash, null);

  const delivered = await loadJobSnapshot(
    config,
    JOB_ID,
    fakeRPC({
      jobs: jobsReturn({ delivery: 'ef'.repeat(32), status: 3 }),
      advance: advanceReturn(0n, 0n, false),
      rate: '0x' + w(5000),
    }).rpc,
  );
  assert.equal(delivered.status, 'Delivered');
  assert.equal(delivered.deliveryHash, '0x' + 'ef'.repeat(32));
});

test('a failing advance or rate read degrades to null instead of blanking the vault facts', async () => {
  const boom = () => {
    throw new Error('rpc down');
  };
  const { rpc } = fakeRPC({ jobs: jobsReturn(), advance: boom, rate: boom });

  const snap = await loadJobSnapshot(config, JOB_ID, rpc);

  assert.equal(snap.exists, true);
  assert.equal(snap.customerPaymentUsdc, '25000000', 'the vault read still shows');
  assert.equal(snap.advance, null);
  assert.equal(snap.orgRateBps, null);
  // With the advance unknown we must not claim the operator keeps everything as fact...
  assert.equal(snap.operatorNetUsdc, '25000000');
});

test('malformed input and short returns fail closed', async () => {
  await assert.rejects(
    () => loadJobSnapshot(config, 'job_104', fakeRPC({}).rpc),
    /bytes32 hex/,
    'a non-bytes32 job id must be refused before any call',
  );
  await assert.rejects(
    () => loadJobSnapshot({ ...config, jobVaultAddress: 'nope' }, JOB_ID, fakeRPC({}).rpc),
    /not an address/,
  );
  await assert.rejects(
    () => loadJobSnapshot(config, JOB_ID, fakeRPC({ jobs: '0x' + w(1) }).rpc),
    /want at least 288/,
    'a truncated struct must not be decoded into a plausible-looking job',
  );
});

test('the status table matches the contract enum order', () => {
  assert.deepEqual(
    [...JOB_STATUS],
    ['Created', 'Funded', 'InProgress', 'Delivered', 'Accepted', 'Refunded', 'Cancelled'],
  );
  // Index 4 is the settled terminal the daemon's Oracle also relies on.
  assert.equal(JOB_STATUS[4], 'Accepted');
});
