/**
 * Pins the signing domain against the DEPLOYED contract, not against a constant.
 *
 * A test asserting `name === 'USDC'` would just move the hardcoded string into the test file. This
 * reads the real USDC precompile's `DOMAIN_SEPARATOR` off Arc and recomputes it from the domain we
 * sign (USDC_EIP712_DOMAIN name/version + chain id + asset). If they disagree — whether from a typo
 * on our side OR a chain that uses a different name/version than we think — the test fails. It is
 * the same derivation used to FIND the "USD Coin" bug, turned into a guard.
 *
 * It reads the chain, so it can only run where the RPC is reachable; it skips (loudly) otherwise
 * rather than passing on no evidence.
 */
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createPublicClient, encodeAbiParameters, http, keccak256, toHex, type Hex } from 'viem';

import { USDC_EIP712_DOMAIN } from './usdc-domain.js';

const RPC = process.env.ARC_TESTNET_RPC ?? 'https://rpc.testnet.arc.network';
const USDC = (process.env.ARC_USDC_ADDRESS ?? '0x3600000000000000000000000000000000000000') as `0x${string}`;
const CHAIN_ID = Number(process.env.ARC_TESTNET_CHAIN_ID ?? process.env.ARC_CHAIN_ID ?? 5042002);

const EIP712_DOMAIN_TYPEHASH = keccak256(toHex('EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)'));

function domainSeparator(name: string, version: string): Hex {
  return keccak256(
    encodeAbiParameters(
      [{ type: 'bytes32' }, { type: 'bytes32' }, { type: 'bytes32' }, { type: 'uint256' }, { type: 'address' }],
      [EIP712_DOMAIN_TYPEHASH, keccak256(toHex(name)), keccak256(toHex(version)), BigInt(CHAIN_ID), USDC],
    ),
  );
}

test('the domain we sign matches the deployed USDC DOMAIN_SEPARATOR (derived from chain)', async (t) => {
  const client = createPublicClient({ transport: http(RPC) });
  let onchain: Hex;
  try {
    onchain = (await client.readContract({
      address: USDC,
      abi: [{ type: 'function', name: 'DOMAIN_SEPARATOR', stateMutability: 'view', inputs: [], outputs: [{ type: 'bytes32' }] }] as const,
      functionName: 'DOMAIN_SEPARATOR',
    })) as Hex;
  } catch (e) {
    t.skip(`Arc RPC unreachable (${(e as Error).message}) — cannot derive the domain from chain here`);
    return;
  }

  const ours = domainSeparator(USDC_EIP712_DOMAIN.name, USDC_EIP712_DOMAIN.version);
  assert.equal(
    ours,
    onchain,
    `the domain we sign (name="${USDC_EIP712_DOMAIN.name}", version="${USDC_EIP712_DOMAIN.version}") does not ` +
      `reproduce the deployed USDC DOMAIN_SEPARATOR — transferWithAuthorization would revert on settle`,
  );
  // And the mainnet name we used to sign must NOT reproduce it: a regression back to "USD Coin"
  // fails here, on chain evidence, not on a string comparison.
  assert.notEqual(domainSeparator('USD Coin', '2'), onchain, 'mainnet "USD Coin" must not match Arc USDC');
});
