/**
 * Signs a fresh operator→seller authorization, SIMULATES it (eth_call), and only if that is clean
 * broadcasts THAT EXACT payload through the real SelfFacilitator.settle(). Records the seller
 * balance before and after and the receipt's facilitator field. On any revert — simulation OR the
 * real send — it STOPS and reports the selector; it never retries.
 *
 * Run: `source ~/snapfall-env.sh && SELLER_ADDRESS=0x… npm run x402:broadcast`
 */
import { createPublicClient, decodeAbiParameters, encodeFunctionData, http, type Address, type Hex } from 'viem';
import { privateKeyToAccount } from 'viem/accounts';

import { TRANSFER_WITH_AUTHORIZATION_TYPES, type PaymentPayload } from './x402.js';
import { USDC_EIP712_DOMAIN } from './usdc-domain.js';
import { SelfFacilitator } from './self-facilitator.js';
import type { PaymentRequirements } from './facilitator.js';

const RPC = process.env.ARC_TESTNET_RPC ?? 'https://rpc.testnet.arc.network';
const USDC = (process.env.ARC_USDC_ADDRESS ?? '0x3600000000000000000000000000000000000000') as Address;
const CHAIN_ID = Number(process.env.ARC_TESTNET_CHAIN_ID ?? process.env.ARC_CHAIN_ID ?? 5042002);
const SELLER = process.env.SELLER_ADDRESS as Address | undefined;
const VALUE = BigInt(process.env.X402_SIM_VALUE ?? '40000');

const BALANCE_OF = [{ type: 'function', name: 'balanceOf', stateMutability: 'view', inputs: [{ type: 'address' }], outputs: [{ type: 'uint256' }] }] as const;
const TWA_ABI = [{
  type: 'function', name: 'transferWithAuthorization', stateMutability: 'nonpayable',
  inputs: [
    { name: 'from', type: 'address' }, { name: 'to', type: 'address' }, { name: 'value', type: 'uint256' },
    { name: 'validAfter', type: 'uint256' }, { name: 'validBefore', type: 'uint256' }, { name: 'nonce', type: 'bytes32' },
    { name: 'v', type: 'uint8' }, { name: 'r', type: 'bytes32' }, { name: 's', type: 'bytes32' },
  ], outputs: [],
}] as const;

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
function reportSelector(prefix: string, err: unknown) {
  const raw = revertData(err);
  if (!raw) { console.log(`${prefix} REVERT (no data): ${(err as Error).message}`); return; }
  const selector = raw.slice(0, 10);
  if (selector === '0x08c379a0') {
    const [reason] = decodeAbiParameters([{ type: 'string' }], (`0x${raw.slice(10)}`) as Hex);
    console.log(`${prefix} REVERT Error(string): "${reason}"  (selector ${selector})`);
  } else {
    console.log(`${prefix} REVERT selector ${selector}  (full data ${raw}) — NOT retrying, as instructed`);
  }
}

async function main() {
  const key = process.env.TREASURY_PRIVATE_KEY;
  if (!key) throw new Error('TREASURY_PRIVATE_KEY not set (source ~/snapfall-env.sh)');
  if (!SELLER) throw new Error('SELLER_ADDRESS not set');

  const account = privateKeyToAccount(key as Hex);
  const client = createPublicClient({ transport: http(RPC) });

  const authorization = {
    from: account.address, to: SELLER, value: VALUE, validAfter: 0n,
    validBefore: BigInt(Math.floor(Date.now() / 1000) + 3600),
    nonce: (`0x${Date.now().toString(16).padStart(64, '0')}`) as Hex,
  };
  const signature = await account.signTypedData({
    domain: { name: USDC_EIP712_DOMAIN.name, version: USDC_EIP712_DOMAIN.version, chainId: CHAIN_ID, verifyingContract: USDC },
    types: TRANSFER_WITH_AUTHORIZATION_TYPES, primaryType: 'TransferWithAuthorization', message: authorization,
  });
  const r = `0x${signature.slice(2, 66)}` as Hex, s = `0x${signature.slice(66, 130)}` as Hex, v = parseInt(signature.slice(130, 132), 16);

  // 1) Simulate this exact payload.
  const data = encodeFunctionData({ abi: TWA_ABI, functionName: 'transferWithAuthorization',
    args: [authorization.from, authorization.to, authorization.value, authorization.validAfter, authorization.validBefore, authorization.nonce, v, r, s] });
  try {
    await client.call({ account: account.address, to: USDC, data });
    console.log('[bcast] pre-broadcast simulation: clean');
  } catch (err) {
    reportSelector('[bcast] pre-broadcast simulation:', err);
    console.log('[bcast] STOPPING — did not broadcast.');
    process.exit(2);
  }

  const sellerBefore = await client.readContract({ address: USDC, abi: BALANCE_OF, functionName: 'balanceOf', args: [SELLER] });
  console.log(`[bcast] seller balance BEFORE: ${sellerBefore} atomic`);

  // 2) Broadcast the SAME payload through the real SelfFacilitator.
  const payment: PaymentPayload = { x402Version: 2, scheme: 'exact', network: `eip155:${CHAIN_ID}`, payload: { signature, authorization: { ...authorization, value: authorization.value.toString(), validAfter: '0', validBefore: authorization.validBefore.toString() } } };
  const requirements: PaymentRequirements = { scheme: 'exact', network: `eip155:${CHAIN_ID}`, maxAmountRequired: VALUE.toString(), resource: '/x402-first-live', payTo: SELLER, asset: USDC };
  const fac = new SelfFacilitator({ privateKey: key, rpcUrl: RPC, usdcAddress: USDC, chainId: CHAIN_ID });

  const outcome = await fac.settle(payment, requirements);
  const sellerAfter = await client.readContract({ address: USDC, abi: BALANCE_OF, functionName: 'balanceOf', args: [SELLER] });

  console.log('');
  console.log(`[bcast] settle outcome:   ${outcome.state}`);
  if (outcome.state === 'settled') console.log(`[bcast] tx hash:          ${outcome.txHash}`);
  if (outcome.state === 'rejected' || outcome.state === 'unknown') console.log(`[bcast] reason:           ${(outcome as any).reason}`);
  console.log(`[bcast] seller BEFORE:    ${sellerBefore} atomic`);
  console.log(`[bcast] seller AFTER:     ${sellerAfter} atomic`);
  console.log(`[bcast] facilitator field: ${JSON.stringify(fac.endpoints())}`);

  if (outcome.state !== 'settled') { console.log('[bcast] NOT settled — stopping, no retry.'); process.exit(3); }
}

main().catch((e) => { console.error(`[bcast] failed: ${(e as Error).message}`); process.exit(1); });
