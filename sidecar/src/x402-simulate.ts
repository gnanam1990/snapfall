/**
 * PRE-FLIGHT ONLY. Signs a real operator→seller EIP-3009 authorization and runs
 * `transferWithAuthorization` as an `eth_call` (a read — no transaction, no money moves). It NEVER
 * broadcasts. If the call reverts it decodes the revert selector rather than guessing at a cause.
 *
 * Run: `source ~/snapfall-env.sh && SELLER_ADDRESS=0x… npm run x402:simulate`
 */
import {
  createPublicClient,
  decodeAbiParameters,
  encodeFunctionData,
  http,
  type Address,
  type Hex,
} from 'viem';
import { privateKeyToAccount } from 'viem/accounts';

import { TRANSFER_WITH_AUTHORIZATION_TYPES } from './x402.js';
import { USDC_EIP712_DOMAIN } from './usdc-domain.js';

const RPC = process.env.ARC_TESTNET_RPC ?? 'https://rpc.testnet.arc.network';
const USDC = (process.env.ARC_USDC_ADDRESS ?? '0x3600000000000000000000000000000000000000') as Address;
const CHAIN_ID = Number(process.env.ARC_TESTNET_CHAIN_ID ?? process.env.ARC_CHAIN_ID ?? 5042002);
const SELLER = process.env.SELLER_ADDRESS as Address | undefined;
const VALUE = BigInt(process.env.X402_SIM_VALUE ?? '40000'); // 0.04 USDC, 6dp

const BALANCE_OF = [{ type: 'function', name: 'balanceOf', stateMutability: 'view', inputs: [{ type: 'address' }], outputs: [{ type: 'uint256' }] }] as const;
const TWA_ABI = [
  {
    type: 'function',
    name: 'transferWithAuthorization',
    stateMutability: 'nonpayable',
    inputs: [
      { name: 'from', type: 'address' }, { name: 'to', type: 'address' }, { name: 'value', type: 'uint256' },
      { name: 'validAfter', type: 'uint256' }, { name: 'validBefore', type: 'uint256' }, { name: 'nonce', type: 'bytes32' },
      { name: 'v', type: 'uint8' }, { name: 'r', type: 'bytes32' }, { name: 's', type: 'bytes32' },
    ],
    outputs: [],
  },
] as const;

/** Walk a viem error chain for the raw revert bytes (the 0x… `data` a node carries). */
function revertData(err: unknown): Hex | null {
  let node: any = err;
  for (let i = 0; i < 12 && node; i++) {
    const d = node.data;
    if (typeof d === 'string' && d.startsWith('0x') && d.length >= 10) return d as Hex;
    if (typeof d === 'object' && typeof d?.data === 'string' && d.data.startsWith('0x')) return d.data as Hex;
    node = node.cause;
  }
  return null;
}

async function main() {
  const key = process.env.TREASURY_PRIVATE_KEY;
  if (!key) throw new Error('TREASURY_PRIVATE_KEY is not set (source ~/snapfall-env.sh)');
  if (!SELLER) throw new Error('SELLER_ADDRESS is not set');

  const account = privateKeyToAccount(key as Hex);
  const client = createPublicClient({ transport: http(RPC) });

  const authorization = {
    from: account.address,
    to: SELLER,
    value: VALUE,
    validAfter: 0n,
    validBefore: BigInt(Math.floor(Date.now() / 1000) + 3600),
    // Deterministic-but-unique per run window without Math.random at import time.
    nonce: (`0x${Date.now().toString(16).padStart(64, '0')}`) as Hex,
  };

  console.log(`[sim] operator (payer/broadcaster): ${account.address}`);
  console.log(`[sim] seller (payee):               ${SELLER}`);
  console.log(`[sim] value:                        ${VALUE} atomic (${Number(VALUE) / 1e6} USDC)`);
  console.log(`[sim] usdc: ${USDC}  chainId: ${CHAIN_ID}`);

  const sellerBefore = await client.readContract({ address: USDC, abi: BALANCE_OF, functionName: 'balanceOf', args: [SELLER] });
  const opBefore = await client.readContract({ address: USDC, abi: BALANCE_OF, functionName: 'balanceOf', args: [account.address] });
  console.log(`[sim] seller balance BEFORE: ${sellerBefore} atomic`);
  console.log(`[sim] operator balance:      ${opBefore} atomic`);

  const signature = await account.signTypedData({
    domain: { name: USDC_EIP712_DOMAIN.name, version: USDC_EIP712_DOMAIN.version, chainId: CHAIN_ID, verifyingContract: USDC },
    types: TRANSFER_WITH_AUTHORIZATION_TYPES,
    primaryType: 'TransferWithAuthorization',
    message: authorization,
  });
  // v,r,s split (canonical EIP-3009 overload).
  const r = `0x${signature.slice(2, 66)}` as Hex;
  const s = `0x${signature.slice(66, 130)}` as Hex;
  const v = parseInt(signature.slice(130, 132), 16);

  const data = encodeFunctionData({
    abi: TWA_ABI,
    functionName: 'transferWithAuthorization',
    args: [authorization.from, authorization.to, authorization.value, authorization.validAfter, authorization.validBefore, authorization.nonce, v, r, s],
  });

  console.log('[sim] eth_call transferWithAuthorization (v,r,s overload) …');
  try {
    await client.call({ account: account.address, to: USDC, data });
    console.log('[sim] RESULT: SUCCESS — transferWithAuthorization would NOT revert. Safe to broadcast.');
  } catch (err) {
    const raw = revertData(err);
    if (!raw) {
      console.log(`[sim] RESULT: REVERT (no revert data surfaced): ${(err as Error).message}`);
      return;
    }
    const selector = raw.slice(0, 10);
    if (selector === '0x08c379a0') {
      const [reason] = decodeAbiParameters([{ type: 'string' }], (`0x${raw.slice(10)}`) as Hex);
      console.log(`[sim] RESULT: REVERT Error(string): "${reason}"  (selector ${selector})`);
    } else if (selector === '0x4e487b71') {
      console.log(`[sim] RESULT: REVERT Panic  (selector ${selector}, data ${raw})`);
    } else {
      console.log(`[sim] RESULT: REVERT custom-error selector ${selector}  (full data ${raw})`);
      console.log('[sim] NOT swapping the ABI or retrying — reporting the selector as instructed.');
    }
  }
}

main().catch((e) => {
  console.error(`[sim] failed: ${(e as Error).message}`);
  process.exit(1);
});
