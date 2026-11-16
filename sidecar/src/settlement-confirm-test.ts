/**
 * DELIVERED -> SETTLED is decided by the chain, never by the seller's hash.
 *
 * Hermetic: a fake reader stands in for the node. The one live case is opt-in and reads the real
 * 0.04 USDC settlement from spine run 20261116T025542Z:
 *   SNAPFALL_LIVE_SETTLEMENT=1 npm run test:settlement-confirm
 */
import assert from 'node:assert/strict';
import { test } from 'node:test';
import type { Hex } from 'viem';
import {
  AUTHORIZATION_USED_TOPIC,
  TRANSFER_TOPIC,
  confirmSettlement,
  confirmWithRetry,
  rpcSettlementReader,
  type ReceiptLog,
  type SettlementClaim,
  type SettlementReader,
  type TxReceipt,
} from './settlement-confirm.js';

const USDC = '0x3600000000000000000000000000000000000000';
const SYSTEM = '0xfffffffffffffffffffffffffffffffffffffffe'; // Arc's native-USDC event emitter
const PAYER = '0x158771bCEF1F91Af732CeC9117D6D14cFb958Bf8';
const PAYEE = '0xc7b56285bE2F7459B520801bEE4431Ed4FF87c3B';
const OTHER = '0x000000000000000000000000000000000000dEaD';
const NONCE = '0x64f0819af97a54fa77f40f71cdc2098bf4e2e0d258893638fcc1a4b7db6a9a07';
const HASH = '0x0b15e1072ee6d5c269b9ffbaced6e3dc1ad359e2eb00ea944cb4c27c3054e008';

const word = (a: string) => `0x${a.slice(2).toLowerCase().padStart(64, '0')}`;
const amountWord = (n: bigint) => `0x${n.toString(16).padStart(64, '0')}`;

const authUsed = (payer = PAYER, nonce = NONCE, address = USDC): ReceiptLog => ({
  address,
  topics: [AUTHORIZATION_USED_TOPIC, word(payer), nonce],
  data: '0x',
});
const transfer = (to = PAYEE, value = 40000n, address = USDC, from = PAYER): ReceiptLog => ({
  address,
  topics: [TRANSFER_TOPIC, word(from), word(to)],
  data: amountWord(value),
});

/** The shape of the real receipt: AuthorizationUsed, the system Transfer, the token Transfer. */
const realLogs = (): ReceiptLog[] => [authUsed(), transfer(PAYEE, 40000n, SYSTEM), transfer()];

function reader(receipt: TxReceipt | null | Error, used: boolean | Error = true): SettlementReader & { calls: number } {
  const r = {
    calls: 0,
    async receipt() {
      r.calls++;
      if (receipt instanceof Error) throw receipt;
      return receipt;
    },
    async authorizationUsed() {
      if (used instanceof Error) throw used;
      return used;
    },
  };
  return r;
}

const claim = (over: Partial<SettlementClaim> = {}): SettlementClaim => ({
  txHash: HASH,
  usdc: USDC,
  payer: PAYER,
  payee: PAYEE,
  amount: '40000',
  authNonce: NONCE,
  ...over,
});

test('a receipt that consumes our authorization and pays the signed payee the signed amount is SETTLED', async () => {
  const c = await confirmSettlement(reader({ status: 'success', logs: realLogs() }), claim());
  assert.deepEqual(c, { confirmed: true, txHash: HASH });
});

test('case does not matter for addresses or the nonce', async () => {
  const c = await confirmSettlement(
    reader({ status: 'success', logs: realLogs() }),
    claim({ payer: PAYER.toLowerCase(), payee: PAYEE.toUpperCase().replace('0X', '0x'), authNonce: NONCE.toUpperCase().replace('0X', '0x') }),
  );
  assert.equal(c.confirmed, true);
});

const refusals: Array<[string, TxReceipt | null | Error, Partial<SettlementClaim>, boolean | Error]> = [
  ['the seller sent no hash at all', { status: 'success', logs: realLogs() }, { txHash: 'NOT_BROADCAST' }, true],
  ['the node does not know the transaction', null, {}, true],
  ['the transaction reverted', { status: 'reverted', logs: realLogs() }, {}, true],
  ['the transaction consumed a different authorization', { status: 'success', logs: [authUsed(PAYER, `0x${'11'.repeat(32)}`), transfer()] }, {}, true],
  ['someone else\'s authorization was consumed', { status: 'success', logs: [authUsed(OTHER), transfer()] }, {}, true],
  ['the AuthorizationUsed event came from a contract that is not USDC', { status: 'success', logs: [authUsed(PAYER, NONCE, OTHER), transfer()] }, {}, true],
  ['the money went to another payee', { status: 'success', logs: [authUsed(), transfer(OTHER)] }, {}, true],
  ['a different amount moved', { status: 'success', logs: [authUsed(), transfer(PAYEE, 39999n)] }, {}, true],
  ['only Arc\'s system Transfer is present, not the token contract\'s', { status: 'success', logs: [authUsed(), transfer(PAYEE, 40000n, SYSTEM)] }, {}, true],
  ['the transfer came from someone other than the payer', { status: 'success', logs: [authUsed(), transfer(PAYEE, 40000n, USDC, OTHER)] }, {}, true],
  ['USDC reports the authorization as unused', { status: 'success', logs: realLogs() }, {}, false],
  ['the receipt read failed', new Error('connection refused'), {}, true],
  ['the authorizationState read failed', { status: 'success', logs: realLogs() }, {}, new Error('timeout')],
  ['the payee is not an address', { status: 'success', logs: realLogs() }, { payee: 'api.research-data.example' }, true],
  ['the amount is not atomic', { status: 'success', logs: realLogs() }, { amount: '0.04' }, true],
];

for (const [name, receipt, over, used] of refusals) {
  test(`stays DELIVERED when ${name}`, async () => {
    const c = await confirmSettlement(reader(receipt, used), claim(over));
    assert.equal(c.confirmed, false, JSON.stringify(c));
  });
}

test('retries an unanswered read, but not a contradiction', async () => {
  const missing = reader(null);
  const c1 = await confirmWithRetry(missing, claim(), { tries: 3, delayMs: 1 });
  assert.equal(c1.confirmed, false);
  assert.equal(missing.calls, 3, 'a receipt the node does not know yet is retried');

  const wrong = reader({ status: 'success', logs: [authUsed(), transfer(OTHER)] });
  const c2 = await confirmWithRetry(wrong, claim(), { tries: 3, delayMs: 1 });
  assert.equal(c2.confirmed, false);
  assert.equal(wrong.calls, 1, 'a receipt that contradicts the payment is final');
});

test('the RPC URL never reaches an error message', async () => {
  const secret = 's3cr3t-provider-key';
  const r = rpcSettlementReader(`http://127.0.0.1:9/v2/${secret}`);
  const c = await confirmSettlement(r, claim());
  assert.equal(c.confirmed, false);
  assert.ok(!(c as { reason: string }).reason.includes(secret), (c as { reason: string }).reason);
});

test('live: the 0.04 USDC payment from spine run 20261116T025542Z is SETTLED on Arc testnet', { skip: process.env.SNAPFALL_LIVE_SETTLEMENT !== '1' }, async () => {
  const rpc = process.env.ARC_TESTNET_RPC || 'https://rpc.testnet.arc.network';
  const live = rpcSettlementReader(rpc);
  assert.deepEqual(await confirmSettlement(live, claim()), { confirmed: true, txHash: HASH as Hex });
  // The same transaction does not settle a payment it did not carry.
  const c = await confirmSettlement(live, claim({ amount: '60000' }));
  assert.equal(c.confirmed, false);
});
