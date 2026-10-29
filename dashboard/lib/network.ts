/**
 * Which Arc deployment this dashboard reads, chosen once by SNAPFALL_NETWORK.
 *
 * `arc-testnet` (the default) or `arc-mainnet`, matching the `network.name` of the two committed
 * deployment records. next.config inlines SNAPFALL_NETWORK at BUILD time, like
 * SNAPFALL_DEMO_STREAM, so the static pages, the client chrome and the API routes of one build
 * can never disagree about which chain they show. A mainnet dashboard is a mainnet build.
 *
 * The RPC comes from the env var the deployment record names (`network.rpcUrlEnv`:
 * ARC_TESTNET_RPC or ARC_MAINNET_RPC), the same lookup the daemon's chaincfg does. A mainnet
 * build therefore never reads ARC_TESTNET_RPC, which is how a testnet URL would otherwise end up
 * under a mainnet chain id. floatChain also checks eth_chainId against the record, so an RPC that
 * answers for the wrong chain is refused rather than read.
 *
 * Contract addresses: the env override (`addressEnv`) wins, then the committed address. The
 * mainnet record commits none until its deploy lands, so a mainnet build with no address env
 * fails each read loudly instead of pointing at a testnet contract.
 */

import testnet from '../../deployments/arc-testnet.json';
import mainnet from '../../deployments/arc-mainnet.json';

export type NetworkName = 'arc-testnet' | 'arc-mainnet';

interface ContractRecord {
  addressEnv: string;
  address?: string;
}

export interface DeploymentRecord {
  network: {
    name: string;
    chainId: number;
    rpcUrl: string;
    rpcUrlEnv: string;
    explorerUrl: string;
    startBlock: number;
  };
  contracts: {
    jobVault: ContractRecord;
    floatPool: ContractRecord;
    auditAnchor: ContractRecord;
    usdc: ContractRecord;
  };
}

export type ContractName = keyof DeploymentRecord['contracts'];

const RECORDS: Record<NetworkName, DeploymentRecord> = {
  'arc-testnet': testnet,
  'arc-mainnet': mainnet,
};

const LABELS: Record<NetworkName, string> = {
  'arc-testnet': 'Arc testnet',
  'arc-mainnet': 'Arc mainnet',
};

type Env = Record<string, string | undefined>;

const nonBlank = (v: string | undefined): string | undefined => {
  const t = v?.trim();
  return t ? t : undefined;
};

/** Unset is testnet. Anything else unrecognised throws: a typo must not quietly mean testnet. */
export function networkName(raw: string | undefined = process.env.SNAPFALL_NETWORK): NetworkName {
  const v = nonBlank(raw) ?? 'arc-testnet';
  if (v === 'arc-testnet' || v === 'arc-mainnet') return v;
  throw new Error(`SNAPFALL_NETWORK=${JSON.stringify(v)} is not arc-testnet or arc-mainnet`);
}

export function deploymentFor(name: NetworkName = networkName()): DeploymentRecord {
  return RECORDS[name];
}

export function networkLabel(name: NetworkName = networkName()): string {
  return LABELS[name];
}

/** The address a read should use, or undefined when the deployment has none and no env sets one. */
export function contractAddress(contract: ContractName, env: Env = process.env, name: NetworkName = networkName()): string | undefined {
  const c = RECORDS[name].contracts[contract];
  return nonBlank(env[c.addressEnv]) ?? c.address;
}

/** Server-side: the address a chain read is about to use, or a named error saying which env to set. */
export function requireAddress(contract: ContractName, env: Env = process.env, name: NetworkName = networkName()): string {
  const address = contractAddress(contract, env, name);
  if (!address) {
    throw new Error(`${contract} has no address on ${name}: set ${RECORDS[name].contracts[contract].addressEnv}`);
  }
  return address;
}

/** The private RPC override for this network, if one is set. */
export function privateRpcUrl(env: Env = process.env, name: NetworkName = networkName()): string | undefined {
  return nonBlank(env[RECORDS[name].network.rpcUrlEnv]);
}

/**
 * Server-side: an error from a chain read, safe to hand to a client.
 *
 * A private RPC URL usually embeds its API key (arc-mainnet.g.alchemy.com/v2/<key>), and a
 * provider or fetch error may echo the URL it called. The routes and the Audit page show these
 * messages to whoever loads them, so the configured RPC URL and anything shaped like a
 * provider key path are replaced before the message leaves the server. The rest of the message
 * stays, because "set SNAPFALL_FLOAT_POOL_ADDRESS" or "chain ID does not match" is the point.
 */
export function clientSafeError(error: unknown, fallback: string, env: Env = process.env, name: NetworkName = networkName()): string {
  let message = error instanceof Error ? error.message : fallback;
  const rpc = privateRpcUrl(env, name);
  if (rpc) message = message.split(rpc).join('[rpc]');
  // Provider key paths and query keys, wherever they appear (also covers a URL we did not configure).
  message = message.replace(/\/v[23]\/[A-Za-z0-9_-]{8,}/g, '/v2/[redacted]');
  message = message.replace(/([?&](?:api[-_]?key|key|token)=)[^&\s]+/gi, '$1[redacted]');
  return message;
}

/** Server-side: the network half of a chain read. Addresses are resolved per route with
 *  requireAddress, so a route only fails on a contract it actually reads. */
export function chainNetwork(env: Env = process.env, name: NetworkName = networkName()) {
  const d = RECORDS[name];
  const privateRpc = privateRpcUrl(env, name);
  const startBlock = nonBlank(env.SNAPFALL_DEPLOYMENT_BLOCK);
  return {
    network: name,
    chainId: d.network.chainId,
    rpcUrl: privateRpc ?? d.network.rpcUrl,
    hasPrivateRpc: privateRpc !== undefined,
    explorerUrl: d.network.explorerUrl,
    startBlock: startBlock === undefined ? d.network.startBlock : Number(startBlock),
  };
}
