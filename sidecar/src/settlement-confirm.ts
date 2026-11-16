/**
 * DELIVERED -> SETTLED, decided by the chain and never by the seller's word.
 *
 * A seller with a facilitator (the self-facilitator, or Circle's) answers 200 with
 * `receipt.settlement` set to a transaction hash. That hash is a claim made by the party being
 * paid. Until 16 Nov 2026 the sidecar stored it and left the record at DELIVERED forever: H3 §5.2
 * allows DELIVERED -> SETTLED, but nothing ever made the move, so a payment that really landed on
 * Arc read exactly like one that never left this machine (spine run 20261116T025542Z, beats 3 and
 * 5: both payments were on chain, both records said DELIVERED).
 *
 * SETTLED now requires all four, each read from our own RPC:
 *
 *   1. the claimed hash has a receipt, and its status is success;
 *   2. that receipt carries USDC's `AuthorizationUsed(authorizer = our signer, nonce = our
 *      deterministic authNonce)`, so it is OUR authorization being consumed, not some other
 *      transfer the seller points at;
 *   3. it carries USDC's `Transfer(from = our signer, to = the payTo we signed, value = the amount
 *      we signed)`, emitted by the USDC contract itself (Arc also emits a native-USDC Transfer
 *      from the 0xff..fe system address, which is not the token contract and does not count);
 *   4. USDC's `authorizationState(signer, authNonce)` reads true at the latest block.
 *
 * Anything short of that leaves the record at DELIVERED, which the daemon already books as spent
 * (h3.Committed). Confirmation can only ADD certainty; a failed confirmation never turns a
 * delivered payment into FAILED, because the goods are in hand and the authorization may still
 * have been used.
 */
import { createPublicClient, http, keccak256, toHex, type Address, type Hex } from 'viem';

export const AUTHORIZATION_USED_TOPIC = keccak256(toHex('AuthorizationUsed(address,bytes32)'));
export const TRANSFER_TOPIC = keccak256(toHex('Transfer(address,address,uint256)'));

const TX_HASH_RE = /^0x[0-9a-fA-F]{64}$/;
const ADDRESS_RE = /^0x[0-9a-fA-F]{40}$/;
const BYTES32_RE = /^0x[0-9a-fA-F]{64}$/;

export interface ReceiptLog {
  address: string;
  topics: readonly string[];
  data: string;
}

export interface TxReceipt {
  status: 'success' | 'reverted';
  logs: readonly ReceiptLog[];
}

/** The two chain reads confirmation needs. Injected so the decision is testable without a node. */
export interface SettlementReader {
  /** The receipt for `txHash`, or null when the node does not (yet) know the transaction. */
  receipt(txHash: Hex): Promise<TxReceipt | null>;
  /** USDC's EIP-3009 `authorizationState(authorizer, nonce)`. */
  authorizationUsed(usdc: Address, authorizer: Address, nonce: Hex): Promise<boolean>;
}

/** What we signed, plus the hash the seller says settled it. */
export interface SettlementClaim {
  txHash: string;
  usdc: string;
  payer: string;
  payee: string;
  /** Atomic USDC, base-10 string. */
  amount: string;
  authNonce: string;
}

export type Confirmation = { confirmed: true; txHash: Hex } | { confirmed: false; reason: string };

const word = (addr: string) => `0x${addr.slice(2).toLowerCase().padStart(64, '0')}`;
const same = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

/** Whether a receipt.settlement value is shaped like a transaction hash at all. */
export function isTxHash(v: unknown): v is Hex {
  return typeof v === 'string' && TX_HASH_RE.test(v);
}

export async function confirmSettlement(reader: SettlementReader, c: SettlementClaim): Promise<Confirmation> {
  if (!isTxHash(c.txHash)) return { confirmed: false, reason: `settlement ${JSON.stringify(c.txHash)} is not a transaction hash` };
  for (const [name, v] of [['usdc', c.usdc], ['payer', c.payer], ['payee', c.payee]] as const) {
    if (!ADDRESS_RE.test(v)) return { confirmed: false, reason: `${name} ${JSON.stringify(v)} is not an address` };
  }
  if (!BYTES32_RE.test(c.authNonce)) return { confirmed: false, reason: 'authNonce is not 32 bytes' };
  if (!/^(0|[1-9][0-9]*)$/.test(c.amount)) return { confirmed: false, reason: `amount ${JSON.stringify(c.amount)} is not atomic USDC` };

  let receipt: TxReceipt | null;
  try {
    receipt = await reader.receipt(c.txHash);
  } catch (e) {
    return { confirmed: false, reason: `receipt read failed: ${(e as Error).message}` };
  }
  if (!receipt) return { confirmed: false, reason: `no receipt for ${c.txHash} on this chain` };
  if (receipt.status !== 'success') return { confirmed: false, reason: `transaction ${c.txHash} reverted` };

  const fromUsdc = receipt.logs.filter((l) => same(l.address, c.usdc));
  // [event, indexed arg 1, indexed arg 2]; both events this checks have exactly two indexed args.
  const topics3 = (l: ReceiptLog): [string, string, string] | null => {
    const [t0, t1, t2] = l.topics;
    return l.topics.length === 3 && t0 && t1 && t2 ? [t0, t1, t2] : null;
  };
  const used = fromUsdc.some((l) => {
    const t = topics3(l);
    return !!t && same(t[0], AUTHORIZATION_USED_TOPIC) && same(t[1], word(c.payer)) && same(t[2], c.authNonce);
  });
  if (!used) return { confirmed: false, reason: `transaction ${c.txHash} does not consume this payment's authorization` };

  const moved = fromUsdc.some((l) => {
    const t = topics3(l);
    if (!t || !same(t[0], TRANSFER_TOPIC)) return false;
    if (!same(t[1], word(c.payer)) || !same(t[2], word(c.payee))) return false;
    try {
      return BigInt(l.data) === BigInt(c.amount);
    } catch {
      return false;
    }
  });
  if (!moved) return { confirmed: false, reason: `transaction ${c.txHash} has no USDC transfer of ${c.amount} to the signed payee` };

  let state: boolean;
  try {
    state = await reader.authorizationUsed(c.usdc as Address, c.payer as Address, c.authNonce as Hex);
  } catch (e) {
    return { confirmed: false, reason: `authorizationState read failed: ${(e as Error).message}` };
  }
  if (!state) return { confirmed: false, reason: 'USDC reports this authorization as unused' };

  return { confirmed: true, txHash: c.txHash };
}

/**
 * Retries a confirmation that failed only for want of an answer (no receipt yet, an RPC error),
 * never one the chain contradicted. Bounded: a payment that cannot be confirmed stays DELIVERED.
 */
export async function confirmWithRetry(
  reader: SettlementReader,
  claim: SettlementClaim,
  opts: { tries?: number; delayMs?: number } = {},
): Promise<Confirmation> {
  const tries = Math.max(1, opts.tries ?? 5);
  const delayMs = opts.delayMs ?? 2000;
  let last: Confirmation = { confirmed: false, reason: 'not attempted' };
  for (let i = 0; i < tries; i++) {
    last = await confirmSettlement(reader, claim);
    if (last.confirmed) return last;
    const transient = /^(no receipt|receipt read failed|authorizationState read failed)/.test(last.reason);
    if (!transient) return last;
    if (i + 1 < tries) await new Promise((r) => setTimeout(r, delayMs));
  }
  return last;
}

const AUTHORIZATION_STATE_ABI = [
  {
    type: 'function',
    name: 'authorizationState',
    stateMutability: 'view',
    inputs: [
      { name: 'authorizer', type: 'address' },
      { name: 'nonce', type: 'bytes32' },
    ],
    outputs: [{ type: 'bool' }],
  },
] as const;

/** A reader over a JSON-RPC endpoint. The URL is never put into an error message. */
export function rpcSettlementReader(rpcUrl: string): SettlementReader {
  const client = createPublicClient({ transport: http(rpcUrl, { timeout: 10_000, retryCount: 0 }) });
  const scrub = (e: unknown) => new Error(((e as { shortMessage?: string }).shortMessage ?? 'RPC request failed').split('\n')[0]);
  return {
    async receipt(txHash) {
      try {
        const r = await client.getTransactionReceipt({ hash: txHash });
        return { status: r.status, logs: r.logs.map((l) => ({ address: l.address, topics: l.topics, data: l.data })) };
      } catch (e) {
        if ((e as { name?: string }).name === 'TransactionReceiptNotFoundError') return null;
        throw scrub(e);
      }
    },
    async authorizationUsed(usdc, authorizer, nonce) {
      try {
        return await client.readContract({ address: usdc, abi: AUTHORIZATION_STATE_ABI, functionName: 'authorizationState', args: [authorizer, nonce] });
      } catch (e) {
        throw scrub(e);
      }
    },
  };
}
