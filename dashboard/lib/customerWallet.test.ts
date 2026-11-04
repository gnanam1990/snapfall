import { test } from 'node:test';
import assert from 'node:assert/strict';
import { decodeFunctionData, parseAbi, toFunctionSelector } from 'viem';

import { acceptCalls, fundCalls, walletAction, type JobView } from './customerWallet';

const WALLET = '0x00000000000000000000000000000000000000a1';
const OTHER = '0x00000000000000000000000000000000000000b2';
const USDC = '0x3600000000000000000000000000000000000000';
const VAULT = '0x00000000000000000000000000000000000000f1';
const JOB = `0x${'ab'.repeat(32)}`;

const job = (over: Partial<JobView>): JobView => ({
  exists: true,
  status: 'Created',
  customer: WALLET,
  customerPaymentUsdc: '1000000',
  ...over,
});

test('the gate follows the contract: fund at Created, accept at Delivered, and only for the customer', () => {
  assert.deepEqual(walletAction(job({}), WALLET), { kind: 'fund', amountAtomic: '1000000' });
  assert.deepEqual(walletAction(job({ status: 'Delivered' }), WALLET.toUpperCase().replace('0X', '0x')), {
    kind: 'accept',
  });
  const notMine = walletAction(job({ status: 'Delivered' }), OTHER);
  assert.equal(notMine.kind, 'not-customer');
  for (const status of ['Funded', 'InProgress', 'Accepted', 'Refunded', 'Cancelled', null]) {
    assert.equal(walletAction(job({ status }), WALLET).kind, 'nothing', `status ${status}`);
  }
  assert.equal(walletAction(job({ exists: false }), WALLET).kind, 'nothing');
  assert.equal(walletAction(job({ customer: null }), WALLET).kind, 'nothing');
  // An unreadable or zero price is not something to approve.
  for (const p of [null, '', '0', '1.5', '-1']) {
    assert.equal(walletAction(job({ customerPaymentUsdc: p }), WALLET).kind, 'nothing', `price ${p}`);
  }
});

test('funding is approve(exactly the price) to JobVault, then fund(jobId), in that order', () => {
  const calls = fundCalls({ usdc: USDC, jobVault: VAULT, jobId: JOB, amountAtomic: '1000000' });
  assert.equal(calls.length, 2);
  assert.equal(calls[0]!.to.toLowerCase(), USDC);
  const approve = decodeFunctionData({
    abi: parseAbi(['function approve(address spender, uint256 amount) returns (bool)']),
    data: calls[0]!.data,
  });
  assert.equal(approve.functionName, 'approve');
  assert.equal((approve.args[0] as string).toLowerCase(), VAULT, 'the allowance goes to JobVault, nowhere else');
  assert.equal(approve.args[1], 1_000_000n, 'exactly the price, never unlimited');

  assert.equal(calls[1]!.to.toLowerCase(), VAULT);
  assert.equal(calls[1]!.data.slice(0, 10), toFunctionSelector('fund(bytes32)'));
  assert.equal(calls[1]!.data.slice(10), 'ab'.repeat(32));

  assert.throws(() => fundCalls({ usdc: USDC, jobVault: VAULT, jobId: JOB, amountAtomic: '0' }));
  assert.throws(() => fundCalls({ usdc: USDC, jobVault: VAULT, jobId: '0x1234', amountAtomic: '1' }));
});

test('accepting is acceptDelivery(jobId) on JobVault', () => {
  const [call] = acceptCalls({ jobVault: VAULT, jobId: JOB });
  assert.equal(call!.to.toLowerCase(), VAULT);
  assert.equal(call!.data, `${toFunctionSelector('acceptDelivery(bytes32)')}${'ab'.repeat(32)}`);
});
