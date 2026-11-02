/**
 * CCTP V2: moving the customer's USDC from another chain to their wallet on Arc, so they can
 * fund a job without first finding a bridge, an Arc faucet or an exchange.
 *
 * The pure half: addresses, the burn sizing, calldata, and reading Circle's attestation
 * answer. No network, so it is tested directly (cctp.test.ts). lib/passkeyWallet.ts sends the
 * calls; app/api/cctp/message proxies the attestation read.
 *
 * ── The one number that must be right ────────────────────────────────────────────────────────
 *
 * JobVault.fund pulls EXACTLY customerPayment. A Fast Transfer deducts its fee from the burned
 * amount on mint, so burning the price would mint slightly less and the fund call would revert.
 * `burnFor` sizes the burn so that even at the maximum fee the burn permits (maxFee), what
 * arrives is at least the price. Anything above the price simply stays in the customer's wallet.
 *
 * ── Who completes the mint ───────────────────────────────────────────────────────────────────
 *
 * destinationCaller is zero, so anyone may submit receiveMessage. The customer's own Arc user
 * operation does it, batched with approve and fund, so the mint and the funding land together.
 * If the customer leaves halfway, the burn is not lost: the attestation stays valid and the
 * page resumes from the saved burn (or anyone can relay it).
 *
 * Addresses: Circle's CCTP V2 contract table. V2 only; V1 is being retired.
 */

import { encodeFunctionData, getAddress, pad, parseAbi, type Address, type Hex } from 'viem';
import type { WalletCall } from './customerWallet';

export const FAST_FINALITY = 1000;
export const STANDARD_FINALITY = 2000;

export interface CctpNetwork {
  /** viem chain key the passkey wallet uses for this chain. */
  chainKey: 'baseSepolia' | 'sepolia' | 'arbitrumSepolia' | 'base' | 'mainnet' | 'arbitrum';
  label: string;
  chainId: number;
  domain: number;
  usdc: Address;
}

export interface CctpEnv {
  irisBase: string;
  tokenMessenger: Address;
  messageTransmitter: Address;
  arcDomain: number;
  sources: CctpNetwork[];
}

/** Keyed by the ARC deployment's chain id: testnet sources for Arc Testnet, mainnet for Arc. */
export const CCTP: Record<number, CctpEnv> = {
  5042002: {
    irisBase: 'https://iris-api-sandbox.circle.com',
    tokenMessenger: '0x8FE6B999Dc680CcFDD5Bf7EB0974218be2542DAA',
    messageTransmitter: '0xE737e5cEBEEBa77EFE34D4aa090756590b1CE275',
    arcDomain: 26,
    sources: [
      { chainKey: 'baseSepolia', label: 'Base Sepolia', chainId: 84532, domain: 6, usdc: '0x036CbD53842c5426634e7929541eC2318f3dCF7e' },
    ],
  },
  5042: {
    irisBase: 'https://iris-api.circle.com',
    tokenMessenger: '0x28b5a0e9C621a5BadaA536219b3a228C8168cf5d',
    messageTransmitter: '0x81D40F21F12A8F0E3252Bccb954D722d4c464B64',
    arcDomain: 26,
    sources: [
      { chainKey: 'base', label: 'Base', chainId: 8453, domain: 6, usdc: '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913' },
    ],
  },
};

const TOKEN_MESSENGER = parseAbi([
  'function depositForBurn(uint256 amount, uint32 destinationDomain, bytes32 mintRecipient, address burnToken, bytes32 destinationCaller, uint256 maxFee, uint32 minFinalityThreshold)',
]);
const MESSAGE_TRANSMITTER = parseAbi([
  'function receiveMessage(bytes message, bytes attestation) returns (bool)',
  'function usedNonces(bytes32 nonce) view returns (uint256)',
]);
export const MESSAGE_TRANSMITTER_ABI = MESSAGE_TRANSMITTER;
export const ERC20_BALANCE_ABI = parseAbi(['function balanceOf(address owner) view returns (uint256)']);
const ERC20 = parseAbi(['function approve(address spender, uint256 amount) returns (bool)']);

const ceilDiv = (a: bigint, b: bigint) => (a + b - 1n) / b;

/**
 * The burn that delivers at least `priceAtomic` at a fee of `feeBps`, and the maxFee that caps
 * it. Invariant (tested): burn - maxFee >= price, and maxFee >= ceil(burn * feeBps / 10000), so
 * the protocol may charge its full fee and the customer still receives the price.
 */
export function burnFor(priceAtomic: bigint, feeBps: number): { burn: bigint; maxFee: bigint } {
  if (priceAtomic <= 0n) throw new Error('price must be positive');
  if (!Number.isInteger(feeBps) || feeBps < 0 || feeBps >= 10_000) throw new Error(`fee ${feeBps} bps is not a fee`);
  const bps = BigInt(feeBps);
  let burn = priceAtomic + ceilDiv(priceAtomic * bps, 10_000n - bps);
  let maxFee = ceilDiv(burn * bps, 10_000n);
  while (burn - maxFee < priceAtomic) {
    burn += 1n;
    maxFee = ceilDiv(burn * bps, 10_000n);
  }
  return { burn, maxFee };
}

/** The Fast-transfer fee for `source` -> Arc from Circle's fee table (bps). */
export function fastFeeBps(table: unknown): number {
  if (!Array.isArray(table)) throw new Error('fee table is not a list');
  const fast = table.find((r) => (r as { finalityThreshold?: number }).finalityThreshold === FAST_FINALITY) as
    | { minimumFee?: number }
    | undefined;
  if (!fast || typeof fast.minimumFee !== 'number') throw new Error('no Fast Transfer fee for this route');
  // Circle quotes bps that may be fractional; round UP so the burn is never undersized.
  return Math.ceil(fast.minimumFee);
}

/** On the SOURCE chain: approve TokenMessengerV2 for the burn, then burn to the Arc wallet. */
export function burnCalls(p: {
  env: CctpEnv;
  source: CctpNetwork;
  arcRecipient: string;
  burn: bigint;
  maxFee: bigint;
}): WalletCall[] {
  const messenger = getAddress(p.env.tokenMessenger);
  return [
    {
      to: getAddress(p.source.usdc),
      data: encodeFunctionData({ abi: ERC20, functionName: 'approve', args: [messenger, p.burn] }),
    },
    {
      to: messenger,
      data: encodeFunctionData({
        abi: TOKEN_MESSENGER,
        functionName: 'depositForBurn',
        args: [
          p.burn,
          p.env.arcDomain,
          pad(getAddress(p.arcRecipient), { size: 32 }),
          getAddress(p.source.usdc),
          pad('0x', { size: 32 }),
          p.maxFee,
          FAST_FINALITY,
        ],
      }),
    },
  ];
}

/** On ARC: mint the attested burn. Batched before approve + fund by the caller. */
export function receiveCall(env: CctpEnv, message: Hex, attestation: Hex): WalletCall {
  return {
    to: getAddress(env.messageTransmitter),
    data: encodeFunctionData({ abi: MESSAGE_TRANSMITTER, functionName: 'receiveMessage', args: [message, attestation] }),
  };
}

export type Attested =
  | {
      ready: true;
      message: Hex;
      attestation: Hex;
      /** bytes32 nonce; MessageTransmitterV2.usedNonces says whether it was already received. */
      nonce: Hex;
      amount: bigint;
      feeExecuted: bigint | null;
      /** The burn's fee cap: what to assume was charged when feeExecuted is not reported. Null
       *  when Circle reports neither, and then the fee is unknown (see finishPlan). */
      maxFee: bigint | null;
      recipient: string;
    }
  | { ready: false; reason: string };

/**
 * Read Circle's /v2/messages answer for one burn. Ready only when the attestation is signed AND
 * the message is the one we expect: minting to OUR wallet on ARC. A message for a different
 * recipient or domain is refused, not relayed.
 */
export function readAttestation(body: unknown, expect: { arcDomain: number; recipient: string }): Attested {
  const msgs = (body as { messages?: unknown[] } | null)?.messages;
  if (!Array.isArray(msgs) || msgs.length === 0) return { ready: false, reason: 'Circle has not seen the burn yet.' };
  const m = msgs[0] as {
    status?: string;
    message?: string;
    eventNonce?: string;
    attestation?: string | null;
    delayReason?: string | null;
    decodedMessage?: {
      destinationDomain?: string | number;
      decodedMessageBody?: { mintRecipient?: string; amount?: string; feeExecuted?: string | null; maxFee?: string };
    } | null;
  };
  if (m.status !== 'complete' || !m.attestation || m.attestation === 'PENDING' || !m.message || m.message === '0x') {
    return {
      ready: false,
      reason: m.delayReason ? `Waiting for Circle (${m.delayReason.replace(/_/g, ' ')}).` : 'Waiting for Circle to attest the burn.',
    };
  }
  const d = m.decodedMessage;
  const body2 = d?.decodedMessageBody;
  if (!d || !body2?.mintRecipient || !body2.amount || !/^0x[0-9a-fA-F]{64}$/.test(m.eventNonce ?? '')) {
    return { ready: false, reason: 'Circle’s answer could not be decoded; not relaying it.' };
  }
  if (Number(d.destinationDomain) !== expect.arcDomain) {
    return { ready: false, reason: `This burn is for domain ${d.destinationDomain}, not Arc; not relaying it.` };
  }
  const recipient = getAddress(`0x${body2.mintRecipient.replace(/^0x/, '').slice(-40)}`);
  if (recipient !== getAddress(expect.recipient)) {
    return { ready: false, reason: `This burn mints to ${recipient}, not your wallet; not relaying it.` };
  }
  return {
    ready: true,
    message: m.message as Hex,
    attestation: m.attestation as Hex,
    nonce: m.eventNonce as Hex,
    amount: BigInt(body2.amount),
    feeExecuted: body2.feeExecuted ? BigInt(body2.feeExecuted) : null,
    maxFee: body2.maxFee ? BigInt(body2.maxFee) : null,
    recipient,
  };
}

/**
 * How to finish a bridged payment once the burn is attested.
 *
 * `mint`: whether this operation must include receiveMessage (false when someone already relayed
 * it). `pay`:
 *   'now'        approve + fund go in the same operation: the wallet will hold the price.
 *   'short'      it will not; mint (if needed) and tell the customer, but do not fund.
 *   'after-mint' Circle reported no fee at all, so what lands is unknown: mint alone, read the
 *                balance, and decide again.
 * Understating what lands only asks the customer to top up; overstating it would put a fund call
 * that reverts into a sponsored operation. So an unreported fee is assumed to be the burn's cap,
 * and an unknown cap is never assumed to be zero.
 */
export function finishPlan(p: {
  alreadyMinted: boolean;
  amount: bigint;
  feeExecuted: bigint | null;
  maxFee: bigint | null;
  arcBalance: bigint;
  price: bigint;
}): { mint: boolean; pay: 'now' | 'short' | 'after-mint' } {
  if (p.alreadyMinted) return { mint: false, pay: p.arcBalance >= p.price ? 'now' : 'short' };
  const fee = p.feeExecuted ?? p.maxFee;
  if (fee === null) return { mint: true, pay: 'after-mint' };
  const minted = p.amount > fee ? p.amount - fee : 0n;
  return { mint: true, pay: p.arcBalance + minted >= p.price ? 'now' : 'short' };
}

/** A burn this browser started and has not finished: where it was burned, and the burn tx. */
export interface PendingBurn {
  domain: number;
  tx: Hex;
}

/**
 * Read a saved PendingBurn back. Storage is the customer's own, but it is still input: anything
 * that is not a listed source domain and a 32-byte hash is dropped, not sent to the proxy.
 */
export function parsePendingBurn(raw: string | null, env: CctpEnv): PendingBurn | null {
  if (!raw) return null;
  try {
    const v = JSON.parse(raw) as { domain?: unknown; tx?: unknown };
    if (typeof v.domain !== 'number' || !env.sources.some((s) => s.domain === v.domain)) return null;
    if (typeof v.tx !== 'string' || !/^0x[0-9a-fA-F]{64}$/.test(v.tx)) return null;
    return { domain: v.domain, tx: v.tx as Hex };
  } catch {
    return null;
  }
}
