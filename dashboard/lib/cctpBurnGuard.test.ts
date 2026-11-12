import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { Hex } from 'viem';
import {
  UserOperationFailedError,
  burnStorageKey,
  clearMintedBurn,
  isCurrentBurn,
  markBurnCompleted,
  parseSubmittedBurn,
  resumeSubmitted,
  submitBurnOnce,
} from './cctpBurnGuard';

const tx = ('0x' + 'a'.repeat(64)) as Hex;
const uop = ('0x' + 'b'.repeat(64)) as Hex;
const key = burnStorageKey('0xJOB');

function memory() {
  const m = new Map<string, string>();
  return {
    get length() {
      return m.size;
    },
    key: (i: number) => [...m.keys()][i] ?? null,
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => {
      m.set(k, v);
    },
    removeItem: (k: string) => {
      m.delete(k);
    },
  };
}

/** A wallet that signs, hands over the hash, then broadcasts and confirms. */
const wallet =
  (opts: { failAfterBroadcast?: boolean } = {}) =>
  async (beforeBroadcast: (h: Hex) => void) => {
    beforeBroadcast(uop);
    if (opts.failAfterBroadcast) throw new Error('receipt timeout');
    return tx;
  };

test('a declined passkey leaves nothing behind, so the customer can try again', async () => {
  const s = memory();
  await assert.rejects(
    submitBurnOnce(s, key, 6, async () => {
      throw new Error('NotAllowedError: the user cancelled');
    }),
    /Nothing was sent; you can try again/,
  );
  assert.equal(s.getItem(key), null);
  assert.deepEqual(await submitBurnOnce(s, key, 6, wallet()), { domain: 6, tx });
});

test('the user operation hash is saved before broadcast, and a lost receipt blocks a second burn', async () => {
  const s = memory();
  let savedAtBroadcast: string | null = null;
  await assert.rejects(
    submitBurnOnce(s, key, 6, async (before) => {
      before(uop);
      savedAtBroadcast = s.getItem(key);
      throw new Error('receipt timeout');
    }),
    /sent but is not confirmed yet/,
  );
  assert.deepEqual(JSON.parse(savedAtBroadcast!), { status: 'submitted', domain: 6, userOpHash: uop });
  let calls = 0;
  await assert.rejects(
    submitBurnOnce(s, key, 6, async () => {
      calls++;
      return tx;
    }),
    /prior transfer/,
  );
  assert.equal(calls, 0);
});

test('a submitted burn resumes from its user operation into a known burn', async () => {
  const s = memory();
  await assert.rejects(submitBurnOnce(s, key, 6, wallet({ failAfterBroadcast: true })));
  const submitted = parseSubmittedBurn(s.getItem(key), [6]);
  assert.deepEqual(submitted, { status: 'submitted', domain: 6, userOpHash: uop });
  await assert.rejects(
    resumeSubmitted(s, key, submitted!, async () => {
      throw new Error('still pending');
    }),
    /not confirmed yet/,
  );
  assert.deepEqual(parseSubmittedBurn(s.getItem(key), [6]), submitted, 'a failed lookup keeps the record');
  assert.deepEqual(await resumeSubmitted(s, key, submitted!, async (h) => (h === uop ? tx : '0x')), { domain: 6, tx });
  assert.deepEqual(JSON.parse(s.getItem(key)!), { domain: 6, tx });
});

test('a submitted record for an unlisted domain or with a bad hash is not resumable', () => {
  assert.equal(parseSubmittedBurn(JSON.stringify({ status: 'submitted', domain: 3, userOpHash: uop }), [6]), null);
  assert.equal(parseSubmittedBurn(JSON.stringify({ status: 'submitted', domain: 6, userOpHash: '0x12' }), [6]), null);
});

test('a failed save before broadcast stops the wallet from broadcasting', async () => {
  const s = memory();
  let broadcast = false;
  const blocked = { ...s, setItem: () => {
    throw new Error('denied');
  } };
  await assert.rejects(
    submitBurnOnce(blocked, key, 6, async (before) => {
      before(uop);
      broadcast = true;
      return tx;
    }),
    /Nothing was submitted/,
  );
  assert.equal(broadcast, false);

  // The first save succeeds, the pre-broadcast one fails: still nothing broadcast.
  const m = memory();
  let writes = 0;
  const failSecond = { ...m, get length() { return m.length; }, setItem: (k: string, v: string) => {
    if (++writes === 2) throw new Error('quota');
    m.setItem(k, v);
  } };
  await assert.rejects(
    submitBurnOnce(failSecond, key, 6, async (before) => {
      before(uop);
      broadcast = true;
      return tx;
    }),
    /Nothing was sent/,
  );
  assert.equal(broadcast, false);
});

test('a successful burn is durable and cannot be repeated', async () => {
  const s = memory();
  const p = await submitBurnOnce(s, key, 6, wallet());
  assert.deepEqual(JSON.parse(s.getItem(key)!), p);
  await assert.rejects(submitBurnOnce(s, key, 6, wallet()), /prior transfer/);
});

test('a burn in flight in this tab blocks a concurrent attempt', async () => {
  const s = memory();
  let release!: () => void;
  let calls = 0;
  const first = submitBurnOnce(s, key, 6, async (before) => {
    calls++;
    await new Promise<void>((r) => {
      release = r;
    });
    before(uop);
    return tx;
  });
  await assert.rejects(submitBurnOnce(s, key, 6, wallet()), /prior transfer/);
  release();
  await first;
  assert.equal(calls, 1);
});

test('a leftover signing record (tab closed at the passkey prompt) does not block', async () => {
  const s = memory();
  s.setItem(key, JSON.stringify({ status: 'signing', domain: 6 }));
  assert.deepEqual(await submitBurnOnce(s, key, 6, wallet()), { domain: 6, tx });
});

test('completed and unrecognised records block; a minted-but-short burn is cleared', async () => {
  const s = memory();
  const p = await submitBurnOnce(s, key, 6, wallet());
  markBurnCompleted(s, key, p);
  assert.deepEqual(JSON.parse(s.getItem(key)!), { status: 'completed' });
  await assert.rejects(submitBurnOnce(s, key, 6, wallet()), /prior transfer/);
  s.setItem(key, JSON.stringify({ status: 'submitting-or-unknown', domain: 6 }));
  await assert.rejects(submitBurnOnce(s, key, 6, wallet()), /prior transfer/);
  s.setItem(key, 'null');
  await assert.rejects(submitBurnOnce(s, key, 6, wallet()), /prior transfer/, 'a stored JSON null is not an absent key');
  s.removeItem(key);
  const q = await submitBurnOnce(s, key, 6, wallet());
  assert.equal(clearMintedBurn(s, key, q), true);
  assert.deepEqual(await submitBurnOnce(s, key, 6, wallet()), { domain: 6, tx });
});

test('a stale finish neither clears nor completes a record that is no longer its burn', async () => {
  const s = memory();
  const p = await submitBurnOnce(s, key, 6, wallet());
  markBurnCompleted(s, key, p);
  assert.equal(isCurrentBurn(s, key, p), false);
  assert.equal(clearMintedBurn(s, key, p), false, 'the completed marker survives a stale short finish');
  assert.deepEqual(JSON.parse(s.getItem(key)!), { status: 'completed' });
  const other = { domain: 6, tx: ('0x' + 'c'.repeat(64)) as Hex };
  s.setItem(key, JSON.stringify(other));
  markBurnCompleted(s, key, p);
  assert.deepEqual(JSON.parse(s.getItem(key)!), other, 'another burn is not marked completed by this one');
});

test('a failed removal after a short mint is reported, not hidden', async () => {
  const s = memory();
  const p = await submitBurnOnce(s, key, 6, wallet());
  const stuck = { ...s, get length() { return s.length; }, removeItem: () => {
    throw new Error('denied');
  } };
  assert.equal(clearMintedBurn(stuck, key, p), false);
  await assert.rejects(submitBurnOnce(s, key, 6, wallet()), /prior transfer/);
});

test('a user operation that was included but reverted clears the record: nothing moved', async () => {
  const s = memory();
  await assert.rejects(
    submitBurnOnce(s, key, 6, async (before) => {
      before(uop);
      throw new UserOperationFailedError('the call failed');
    }),
    /failed on chain and no USDC moved; you can try again/,
  );
  assert.equal(s.getItem(key), null);

  await assert.rejects(submitBurnOnce(s, key, 6, wallet({ failAfterBroadcast: true })));
  const submitted = parseSubmittedBurn(s.getItem(key), [6])!;
  await assert.rejects(
    resumeSubmitted(s, key, submitted, async () => {
      throw new UserOperationFailedError('the call failed');
    }),
    /failed on chain and no USDC moved; you can start again/,
  );
  assert.equal(s.getItem(key), null);
  assert.deepEqual(await submitBurnOnce(s, key, 6, wallet()), { domain: 6, tx });
});

test('resume re-reads the record: another tab may have resolved or finished it', async () => {
  const s = memory();
  await assert.rejects(submitBurnOnce(s, key, 6, wallet({ failAfterBroadcast: true })));
  const submitted = parseSubmittedBurn(s.getItem(key), [6])!;
  let looked = 0;
  const confirm = async () => {
    looked++;
    return tx;
  };
  s.setItem(key, JSON.stringify({ domain: 6, tx }));
  assert.deepEqual(await resumeSubmitted(s, key, submitted, confirm), { domain: 6, tx });
  s.setItem(key, JSON.stringify({ status: 'completed' }));
  await assert.rejects(resumeSubmitted(s, key, submitted, confirm), /already paid/);
  assert.deepEqual(JSON.parse(s.getItem(key)!), { status: 'completed' }, 'completed is never overwritten');
  s.setItem(key, JSON.stringify({ status: 'submitted', domain: 6, userOpHash: '0x' + 'd'.repeat(64) }));
  await assert.rejects(resumeSubmitted(s, key, submitted, confirm), /changed in another tab/);
  assert.equal(looked, 0);
});

test('case-variant job URLs share one identity and legacy pending keys block a reburn', async () => {
  assert.equal(burnStorageKey('0xABC'), burnStorageKey('0xabc'));
  const s = memory();
  s.setItem('snapfall.cctp.0xABC', 'legacy-pending');
  let called = false;
  await assert.rejects(
    submitBurnOnce(s, burnStorageKey('0xabc'), 6, async () => {
      called = true;
      return tx;
    }),
    /prior transfer/,
  );
  assert.equal(called, false);
});
