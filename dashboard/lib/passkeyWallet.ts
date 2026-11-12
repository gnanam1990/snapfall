/**
 * The browser half of the customer's own wallet: a passkey on their device that owns a Circle
 * modular smart account on Arc, with Circle's paymaster sponsoring gas.
 *
 * CUSTODY, stated: the passkey never leaves the customer's device and neither Snapfall nor
 * Circle can sign with it. The client key below is a PUBLIC key by design (Circle restricts it
 * to the configured web domain); it identifies the app to Circle's bundler, it authorizes
 * nothing.
 *
 * MINED IS NOT SUCCEEDED, again: a user operation's bundle transaction can succeed while the
 * call inside it reverted. Only the user-operation receipt's `success` says whether the call
 * happened, so that is what `send` checks, and a false is an error, never a "done".
 *
 * Everything here is loaded lazily from a click handler: the SDK touches `window` and
 * WebAuthn, so it must never run during server rendering.
 */

'use client';

import type { Address, Hex } from 'viem';
import type { WalletCall } from './customerWallet';
import { ERC20_BALANCE_ABI, MESSAGE_TRANSMITTER_ABI, type CctpNetwork } from './cctp';
import { networkName, type NetworkName } from './network';
import { UserOperationFailedError } from './cctpBurnGuard';

/** Circle's documented modular-wallet backend; the same for every app. */
const DEFAULT_CLIENT_URL = 'https://modular-sdk.circle.com/v1/rpc/w3s/buidl';

/** Circle's path segment for each chain the deployment can name (Arc), keyed by chain id. */
const CHAIN_PATH: Record<number, string> = { 5042002: 'arcTestnet', 5042: 'arc' };

/** Circle's path segment for each CCTP source chain the portal offers, keyed by viem chain key. */
const SOURCE_PATH: Record<CctpNetwork['chainKey'], string> = {
  baseSepolia: 'baseSepolia',
  sepolia: 'sepolia',
  arbitrumSepolia: 'arbitrumSepolia',
  base: 'base',
  mainnet: 'mainnet',
  arbitrum: 'arbitrum',
};

const CREDENTIAL_STORE = 'snapfall.passkeyCredential';

/** One level of Circle's `circle_getUserOperationGasPrice` answer (decimal wei strings). */
export interface BundlerGasLevel {
  maxFeePerGas: string;
  maxPriorityFeePerGas: string;
}

/**
 * The fees for a user operation, from Circle's bundler rather than the chain's RPC. Arc's
 * `eth_maxPriorityFeePerGas` can sit below the bundler's floor (0.25 gwei against a 1 gwei
 * minimum on 4 Nov 2026), and the bundler then refuses the operation in precheck. The medium
 * level is the bundler's own price. A malformed answer is an error, never a guess.
 */
export function userOpFees(price: { medium?: BundlerGasLevel } | null | undefined): {
  maxFeePerGas: bigint;
  maxPriorityFeePerGas: bigint;
} {
  const m = price?.medium;
  const dec = /^[0-9]{1,30}$/;
  // Strings only: a JSON number (or a one-element array) would pass the decimal test via
  // String() and then reach BigInt as a rounded float, not the value that was checked.
  if (
    !m ||
    typeof m.maxFeePerGas !== 'string' ||
    typeof m.maxPriorityFeePerGas !== 'string' ||
    !dec.test(m.maxFeePerGas) ||
    !dec.test(m.maxPriorityFeePerGas)
  ) {
    throw new Error('Circle did not return a usable gas price; try again.');
  }
  const maxFeePerGas = BigInt(m.maxFeePerGas);
  const maxPriorityFeePerGas = BigInt(m.maxPriorityFeePerGas);
  if (maxFeePerGas === 0n || maxPriorityFeePerGas > maxFeePerGas) {
    throw new Error('Circle returned an inconsistent gas price; try again.');
  }
  return { maxFeePerGas, maxPriorityFeePerGas };
}

export interface PasskeyConfig {
  clientKey: string;
  clientUrl: string;
}

/**
 * Circle names a key's environment in its prefix (`TEST_CLIENT_KEY:…`, `LIVE_CLIENT_KEY:…`). A
 * key for the other environment talks to the other network's bundler and paymaster, so a
 * mainnet build never uses a testnet key, and a testnet build never uses a live one.
 */
export function clientKeyMatchesNetwork(clientKey: string, network: NetworkName): boolean {
  const prefix = clientKey.split(':', 1)[0];
  return network === 'arc-mainnet' ? prefix.startsWith('LIVE_') : prefix.startsWith('TEST_');
}

/**
 * Null when the portal has no Circle client key for this build's network: the self-custody path
 * is then not offered.
 */
export function passkeyConfig(
  network: NetworkName = networkName(),
  clientKey: string = process.env.NEXT_PUBLIC_CIRCLE_CLIENT_KEY ?? '',
): PasskeyConfig | null {
  if (!clientKey || !clientKeyMatchesNetwork(clientKey, network)) return null;
  return { clientKey, clientUrl: process.env.NEXT_PUBLIC_CIRCLE_CLIENT_URL || DEFAULT_CLIENT_URL };
}

export interface SendHooks {
  /**
   * Called with the signed user operation's hash after the passkey signed and BEFORE anything
   * is broadcast. If it throws, nothing is sent. A caller that must survive a lost receipt (a
   * CCTP burn) persists the hash here, so the outcome can be looked up later instead of guessed.
   */
  beforeBroadcast?(userOpHash: Hex): void;
}

export interface ChainWallet {
  address: string;
  /** Sends the calls as one sponsored user operation and returns its transaction hash. */
  send(calls: WalletCall[], hooks?: SendHooks): Promise<Hex>;
  /** Waits for an already-broadcast user operation; its transaction hash, or an error if the call failed. */
  confirm(userOpHash: Hex): Promise<Hex>;
  /** ERC-20 balance (6dp surface on Arc, never the 18dp native one). */
  balanceOf(token: string): Promise<bigint>;
}

export interface PasskeySession extends ChainWallet {
  /** The same passkey's smart account on a CCTP source chain. No new passkey is created. */
  onSource(net: CctpNetwork): Promise<ChainWallet>;
  /** Whether MessageTransmitterV2 on Arc has already received the message with this nonce. */
  nonceUsed(transmitter: string, nonce: Hex): Promise<boolean>;
}

/**
 * Sign in with an existing passkey ('login') or create one ('register'). The credential's
 * public part is remembered in this browser so a returning customer is offered the same
 * account; it is not a secret (the private key stays in the authenticator).
 */
export async function connectPasskey(
  cfg: PasskeyConfig,
  chainId: number,
  mode: 'login' | 'register',
  username?: string,
): Promise<PasskeySession> {
  const chainPath = CHAIN_PATH[chainId];
  if (!chainPath) throw new Error(`Circle modular wallets are not configured for chain ${chainId}`);

  const [sdk, viem, chains, aa] = await Promise.all([
    import('@circle-fin/modular-wallets-core'),
    import('viem'),
    import('viem/chains'),
    import('viem/account-abstraction'),
  ]);
  const chain = chainId === 5042 ? chains.arc : chains.arcTestnet;

  const passkeyTransport = sdk.toPasskeyTransport(cfg.clientUrl, cfg.clientKey);
  const credential = await sdk.toWebAuthnCredential({
    transport: passkeyTransport,
    mode: mode === 'register' ? sdk.WebAuthnMode.Register : sdk.WebAuthnMode.Login,
    ...(mode === 'register' ? { username: username?.trim() || 'snapfall-customer' } : {}),
  });
  try {
    window.localStorage.setItem(CREDENTIAL_STORE, JSON.stringify({ id: credential.id }));
  } catch {
    // Storage blocked: the customer simply signs in with their passkey again next time.
  }

  const owner = aa.toWebAuthnAccount({ credential });

  // One smart account per chain, all owned by the same passkey. Writes go through Circle's
  // bundler (with its paymaster); reads use the chain's public RPC, which needs no client key.
  async function walletOn(chain: import('viem').Chain, path: string): Promise<ChainWallet & { reader: import('viem').PublicClient }> {
    const transport = sdk.toModularTransport(`${cfg.clientUrl}/${path}`, cfg.clientKey);
    const client = viem.createPublicClient({ chain, transport });
    const account = await sdk.toCircleSmartAccount({ client, owner });
    const bundler = aa.createBundlerClient({
      account,
      chain,
      transport,
      userOperation: {
        estimateFeesPerGas: async () => userOpFees(await sdk.getUserOperationGasPrice(client)),
      },
    });
    const reader = viem.createPublicClient({ chain, transport: viem.http() }) as import('viem').PublicClient;
    async function confirm(hash: Hex): Promise<Hex> {
      const receipt = await bundler.waitForUserOperationReceipt({ hash });
      if (!receipt.success) {
        throw new UserOperationFailedError(
          `The transaction was included but the call failed (user operation ${hash}). Nothing changed on chain.`,
        );
      }
      return receipt.receipt.transactionHash;
    }
    return {
      address: account.address,
      reader,
      // viem's sendUserOperation, in its three steps (prepare, sign, broadcast), so the hash is
      // known and handed to the caller before the operation can exist anywhere but this tab.
      async send(calls, hooks) {
        // Callers that need no hash before broadcast keep viem's own single call.
        if (!hooks?.beforeBroadcast) return confirm(await bundler.sendUserOperation({ calls, paymaster: true }));
        const request = await bundler.prepareUserOperation({ account, calls, paymaster: true });
        const signature = await account.signUserOperation(request);
        const signed = { ...request, signature };
        const hash = aa.getUserOperationHash({
          chainId: chain.id,
          entryPointAddress: account.entryPoint.address,
          entryPointVersion: account.entryPoint.version,
          userOperation: signed,
        });
        hooks?.beforeBroadcast?.(hash);
        const sent = await bundler.request(
          {
            method: 'eth_sendUserOperation',
            params: [aa.formatUserOperationRequest(signed), account.entryPoint.address],
          },
          { retryCount: 0 },
        );
        if (typeof sent !== 'string' || sent.toLowerCase() !== hash.toLowerCase()) {
          throw new Error(`Circle's bundler answered with user operation ${String(sent)}, not ${hash}.`);
        }
        return confirm(hash);
      },
      confirm,
      balanceOf(token) {
        return reader.readContract({
          address: token as Address,
          abi: ERC20_BALANCE_ABI,
          functionName: 'balanceOf',
          args: [account.address],
        });
      },
    };
  }

  const arcWallet = await walletOn(chain, chainPath);
  return {
    address: arcWallet.address,
    send: arcWallet.send,
    confirm: arcWallet.confirm,
    balanceOf: arcWallet.balanceOf,
    async onSource(net) {
      const src = (chains as unknown as Record<string, import('viem').Chain>)[net.chainKey];
      if (!src || src.id !== net.chainId) throw new Error(`${net.label} is not a known chain`);
      return walletOn(src, SOURCE_PATH[net.chainKey]);
    },
    async nonceUsed(transmitter, nonce) {
      const used = await arcWallet.reader.readContract({
        address: transmitter as Address,
        abi: MESSAGE_TRANSMITTER_ABI,
        functionName: 'usedNonces',
        args: [nonce],
      });
      return used !== 0n;
    },
  };
}

/** Whether this browser has signed in with a passkey before (so "Sign in" is the likely path). */
export function hasRememberedPasskey(): boolean {
  try {
    return window.localStorage.getItem(CREDENTIAL_STORE) !== null;
  } catch {
    return false;
  }
}
