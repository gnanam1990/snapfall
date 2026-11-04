import { test } from 'node:test';
import assert from 'node:assert/strict';
import { decodeFunctionData, parseAbi, type Hex } from 'viem';

import { CCTP, burnCalls, burnFor, fastFeeBps, finishPlan, parsePendingBurn, readAttestation, receiveCall, FAST_FINALITY } from './cctp';

const ARC_WALLET = '0x00000000000000000000000000000000000000A1';
const env = CCTP[5042002]!;
const source = env.sources[0]!;

test('the burn always delivers at least the price, even at the maximum fee it allows', () => {
  for (const bps of [0, 1, 2, 7, 13, 50, 100, 999]) {
    for (const price of [1n, 2n, 99n, 1_000n, 999_999n, 1_000_000n, 1_234_567n, 6_000_000n, 10n ** 12n]) {
      const { burn, maxFee } = burnFor(price, bps);
      assert.ok(burn - maxFee >= price, `bps=${bps} price=${price}: burn ${burn} - maxFee ${maxFee} < price`);
      // maxFee covers the protocol's own fee on the whole burn, so the burn is not refused.
      assert.ok(maxFee * 10_000n >= burn * BigInt(bps), `bps=${bps} price=${price}: maxFee below the fee`);
      // And it is not wildly oversized: at most one extra minor unit beyond the exact gross-up.
      assert.ok(burn <= price + (price * BigInt(bps)) / BigInt(10_000 - bps) + 2n, `bps=${bps} price=${price}: burn ${burn} oversized`);
    }
  }
  assert.deepEqual(burnFor(1_000_000n, 0), { burn: 1_000_000n, maxFee: 0n });
  assert.throws(() => burnFor(0n, 1));
  assert.throws(() => burnFor(1n, 10_000));
});

test('the Fast fee comes from Circle’s table and rounds up', () => {
  assert.equal(fastFeeBps([{ finalityThreshold: 1000, minimumFee: 1 }, { finalityThreshold: 2000, minimumFee: 0 }]), 1);
  assert.equal(fastFeeBps([{ finalityThreshold: 1000, minimumFee: 1.3 }]), 2);
  assert.throws(() => fastFeeBps([{ finalityThreshold: 2000, minimumFee: 0 }]));
  assert.throws(() => fastFeeBps({}));
});

test('the burn approves TokenMessengerV2 for exactly the burn, then burns to the Arc wallet on domain 26', () => {
  const [approve, burn] = burnCalls({ env, source, arcRecipient: ARC_WALLET, burn: 1_000_101n, maxFee: 101n });
  assert.equal(approve!.to, source.usdc);
  const a = decodeFunctionData({ abi: parseAbi(['function approve(address,uint256) returns (bool)']), data: approve!.data });
  assert.deepEqual(a.args, [env.tokenMessenger, 1_000_101n]);
  assert.equal(burn!.to, env.tokenMessenger);
  const d = decodeFunctionData({
    abi: parseAbi([
      'function depositForBurn(uint256,uint32,bytes32,address,bytes32,uint256,uint32)',
    ]),
    data: burn!.data,
  });
  const [amount, domain, recipient, token, caller, maxFee, finality] = d.args;
  assert.equal(amount, 1_000_101n);
  assert.equal(domain, 26);
  assert.equal(recipient, `0x${'0'.repeat(24)}${ARC_WALLET.slice(2).toLowerCase()}`);
  assert.equal(token, source.usdc);
  assert.equal(caller, `0x${'0'.repeat(64)}`, 'anyone may relay');
  assert.equal(maxFee, 101n);
  assert.equal(finality, FAST_FINALITY);
});

test('receiveMessage targets MessageTransmitterV2', () => {
  const call = receiveCall(env, '0x01' as Hex, '0x02' as Hex);
  assert.equal(call.to, env.messageTransmitter);
});

const answer = (over: Record<string, unknown> = {}, body: Record<string, unknown> = {}) => ({
  messages: [
    {
      status: 'complete',
      message: '0xabcd',
      eventNonce: `0x${'5'.repeat(64)}`,
      attestation: '0xef01',
      decodedMessage: {
        destinationDomain: '26',
        decodedMessageBody: {
          mintRecipient: `0x${'0'.repeat(24)}${ARC_WALLET.slice(2).toLowerCase()}`,
          amount: '1000101',
          feeExecuted: '100',
          maxFee: '101',
          ...body,
        },
      },
      ...over,
    },
  ],
});

test('an attestation is relayed only when signed, for Arc, and minting to this wallet', () => {
  const ok = readAttestation(answer(), { arcDomain: 26, recipient: ARC_WALLET });
  assert.equal(ok.ready, true);
  if (ok.ready) {
    assert.equal(ok.amount, 1_000_101n);
    assert.equal(ok.feeExecuted, 100n);
    assert.equal(ok.maxFee, 101n);
    assert.equal(ok.nonce, `0x${'5'.repeat(64)}`);
  }
  const notYet = [
    { messages: [] },
    answer({ status: 'pending_confirmations', attestation: 'PENDING', message: '0x' }),
    answer({ attestation: null }),
    answer({ eventNonce: undefined }),
  ];
  for (const b of notYet) assert.equal(readAttestation(b, { arcDomain: 26, recipient: ARC_WALLET }).ready, false);

  const delayed = readAttestation(answer({ status: 'pending_confirmations', delayReason: 'insufficient_fee' }), {
    arcDomain: 26,
    recipient: ARC_WALLET,
  });
  assert.ok(!delayed.ready && /insufficient fee/.test(delayed.reason));

  const wrongDomain = readAttestation(answer({ decodedMessage: { destinationDomain: 6, decodedMessageBody: {} } }), {
    arcDomain: 26,
    recipient: ARC_WALLET,
  });
  assert.equal(wrongDomain.ready, false);
  const wrongRecipient = readAttestation(answer({}, { mintRecipient: `0x${'0'.repeat(24)}${'b2'.repeat(20)}` }), {
    arcDomain: 26,
    recipient: ARC_WALLET,
  });
  assert.ok(!wrongRecipient.ready && /not your wallet/.test(wrongRecipient.reason));
});

test('finishing a bridged payment funds only when what lands covers the price', () => {
  const base = { alreadyMinted: false, amount: 1_000_200n, feeExecuted: null, maxFee: 200n, arcBalance: 0n, price: 1_000_000n };
  // An unreported fee is taken to be the cap: 1_000_200 - 200 covers exactly.
  assert.deepEqual(finishPlan(base), { mint: true, pay: 'now' });
  // The executed fee, when reported, is what counts.
  assert.deepEqual(finishPlan({ ...base, amount: 1_000_100n, feeExecuted: 100n }), { mint: true, pay: 'now' });
  // Short by one unit: mint, but do not put a fund call that reverts into the operation.
  assert.deepEqual(finishPlan({ ...base, amount: 1_000_199n }), { mint: true, pay: 'short' });
  // What is already in the wallet counts towards the price.
  assert.deepEqual(finishPlan({ ...base, amount: 1_000_199n, arcBalance: 1n }), { mint: true, pay: 'now' });
  // Someone else relayed the mint: never mint twice, decide on the balance alone.
  assert.deepEqual(finishPlan({ ...base, alreadyMinted: true, arcBalance: 999_999n }), { mint: false, pay: 'short' });
  assert.deepEqual(finishPlan({ ...base, alreadyMinted: true, arcBalance: 1_000_000n }), { mint: false, pay: 'now' });
  // No fee reported at all: never assume zero. Mint alone, then decide on what landed.
  assert.deepEqual(finishPlan({ ...base, maxFee: null }), { mint: true, pay: 'after-mint' });
  // A fee above the amount mints nothing towards the price.
  assert.deepEqual(finishPlan({ ...base, amount: 5n, feeExecuted: 9n, arcBalance: 999_999n }), { mint: true, pay: 'short' });
});

test('a saved burn is resumed only if it is a listed source domain and a 32-byte hash', () => {
  const tx = `0x${'ab'.repeat(32)}`;
  assert.deepEqual(parsePendingBurn(JSON.stringify({ domain: source.domain, tx }), env), { domain: source.domain, tx });
  for (const raw of [
    null,
    '',
    'not json',
    JSON.stringify({ domain: 26, tx }),
    JSON.stringify({ domain: String(source.domain), tx }),
    JSON.stringify({ domain: source.domain, tx: '0xabc' }),
    JSON.stringify({ domain: source.domain, tx: `${tx}&source=0` }),
  ]) {
    assert.equal(parsePendingBurn(raw, env), null, String(raw));
  }
});

test('an attestation that reports no fee cap says so, rather than claiming a zero fee', () => {
  const a = readAttestation(answer({}, { feeExecuted: null, maxFee: undefined }), { arcDomain: 26, recipient: ARC_WALLET });
  assert.equal(a.ready, true);
  if (a.ready) {
    assert.equal(a.feeExecuted, null);
    assert.equal(a.maxFee, null);
  }
});
