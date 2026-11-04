/**
 * Which Arc network this process talks to, and through which RPC. Every script and service in the
 * sidecar resolves its chain here and nowhere else.
 *
 * The network is chosen by ARC_CHAIN_ID (5042002 testnet, the default; 5042 mainnet). The RPC is
 * read from the env var named for THAT network — ARC_TESTNET_RPC or ARC_MAINNET_RPC, the same
 * names deployments/arc-testnet.json and arc-mainnet.json give the daemon — falling back to
 * Circle's public endpoint.
 *
 * The RPC variable is per network on purpose. One shared variable is how a mainnet URL ends up
 * under a testnet chain id (or the reverse): the signature is made for one chain and broadcast to
 * the other, and the first anyone hears of it is a revert with real money behind it. With split
 * names, a mainnet process never reads ARC_TESTNET_RPC at all.
 *
 * Anything this cannot place on a known network throws, rather than defaulting to testnet: an
 * unrecognised chain id is a misconfiguration, and the safe answer to it is to not start.
 *
 * Endpoints: docs.arc.io/arc/references/rpc-endpoints (verified 29 Oct 2026).
 */

export interface ArcNetwork {
  /** Matches the daemon's deployment artifact `network.name`. */
  name: 'arc-testnet' | 'arc-mainnet';
  /** Human label for logs and chain definitions. */
  label: string;
  chainId: number;
  /** CAIP-2, as x402 carries it. */
  caip2: string;
  rpcUrl: string;
  /** The env var `rpcUrl` was (or would have been) read from. */
  rpcUrlEnv: 'ARC_TESTNET_RPC' | 'ARC_MAINNET_RPC';
  testnet: boolean;
}

export const ARC_TESTNET_CHAIN_ID = 5042002;
export const ARC_MAINNET_CHAIN_ID = 5042;

const KNOWN = {
  [ARC_TESTNET_CHAIN_ID]: {
    name: 'arc-testnet',
    label: 'Arc testnet',
    rpcUrlEnv: 'ARC_TESTNET_RPC',
    // Kept on the host deployments/arc-testnet.json names; docs.arc.io now also lists
    // rpc.testnet.arc.io, and both answered 5042002 on 29 Oct 2026.
    publicRpc: 'https://rpc.testnet.arc.network',
    testnet: true,
  },
  [ARC_MAINNET_CHAIN_ID]: {
    name: 'arc-mainnet',
    label: 'Arc mainnet',
    rpcUrlEnv: 'ARC_MAINNET_RPC',
    publicRpc: 'https://rpc.mainnet.arc.io',
    testnet: false,
  },
} as const;

type Env = Record<string, string | undefined>;

const nonBlank = (v: string | undefined): string | undefined => {
  const t = v?.trim();
  return t ? t : undefined;
};

function parseChainId(name: string, raw: string): number {
  // Number('') is 0 and Number('5042abc') is NaN; both must fail, not slip through as a default.
  if (!/^[0-9]+$/.test(raw)) throw new Error(`${name}=${JSON.stringify(raw)} is not a chain id`);
  return Number(raw);
}

export function resolveArcNetwork(env: Env = process.env): ArcNetwork {
  const primary = nonBlank(env.ARC_CHAIN_ID);
  // ARC_TESTNET_CHAIN_ID is the older name a few scripts read first. It is still honoured, but it
  // may not quietly outvote ARC_CHAIN_ID: a stale testnet value left in a shell would otherwise
  // pin a process meant for mainnet to testnet.
  const legacy = nonBlank(env.ARC_TESTNET_CHAIN_ID);
  if (primary && legacy && primary !== legacy) {
    throw new Error(`ARC_CHAIN_ID=${primary} and ARC_TESTNET_CHAIN_ID=${legacy} disagree; unset one`);
  }
  const chosen = primary ?? legacy;
  const chainId = chosen ? parseChainId(primary ? 'ARC_CHAIN_ID' : 'ARC_TESTNET_CHAIN_ID', chosen) : ARC_TESTNET_CHAIN_ID;

  const known = KNOWN[chainId as keyof typeof KNOWN];
  if (!known) {
    throw new Error(
      `chain id ${chainId} is not an Arc network this sidecar knows ` +
        `(${ARC_TESTNET_CHAIN_ID} testnet, ${ARC_MAINNET_CHAIN_ID} mainnet)`,
    );
  }

  return {
    name: known.name,
    label: known.label,
    chainId,
    caip2: `eip155:${chainId}`,
    rpcUrl: nonBlank(env[known.rpcUrlEnv]) ?? known.publicRpc,
    rpcUrlEnv: known.rpcUrlEnv,
    testnet: known.testnet,
  };
}
