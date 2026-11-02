/**
 * Self-hosted x402 facilitator — the permissionless settlement path.
 *
 * ── Why this exists BESIDE the Circle Gateway client, not instead of it ──────────────────────
 *
 * `CircleFacilitator` settles by POSTing the authorization to Circle Gateway
 * (`gateway-api-testnet.circle.com/v1/x402/settle`). That integration is written and
 * stays the documented, default path. But Circle Gateway is access-gated (allowlist, not
 * self-service), so on the submission timeline we could not exercise it live.
 *
 * An EIP-3009 authorization is a bearer instrument: anyone with a USDC-funded Arc wallet can submit
 * `USDC.transferWithAuthorization(...)` directly and the transfer lands. That is the "build your
 * own facilitator" x402 pattern. This module does exactly that and nothing more — it is a relayer,
 * not a signer of payments: it broadcasts the buyer's already-signed authorization and pays gas.
 *
 * What a SETTLED result from THIS path proves, precisely: the x402 handshake, the EIP-3009
 * signature, and on-chain settlement all work end to end on Arc. What it does NOT prove: that the
 * Circle Gateway integration works — that path still never ran. Two different claims; the docs
 * keep both.
 *
 * Honesty, same rule as the Gateway client: the ONLY thing that returns `settled` is a real
 * transaction hash from a successful on-chain receipt. A revert is `rejected` (nothing moved); a
 * dropped/timed-out submit is `unknown` (may still land — reconcile, never release on it).
 *
 * The relayer is a Circle developer-controlled wallet when `CIRCLE_BROADCASTER_WALLET_ID` is set
 * (circle-wallet.ts `loadCircleRelayer`), otherwise the raw `BROADCASTER_PRIVATE_KEY`. A
 * configured Circle relayer never falls back to the raw key. Circle's hash is not its verdict:
 * the receipt is read from our own RPC either way, and only `success` there settles.
 */
import {
  createPublicClient,
  createWalletClient,
  defineChain,
  encodeFunctionData,
  http,
  parseSignature,
  recoverTypedDataAddress,
  type Address,
  type Hex,
} from 'viem';
import { privateKeyToAccount } from 'viem/accounts';

import type { PaymentRequirements, SettleOutcome, VerifyOutcome } from './facilitator.js';
import { TRANSFER_WITH_AUTHORIZATION_TYPES, type Authorization, type PaymentPayload } from './x402.js';
import { USDC_EIP712_DOMAIN } from './usdc-domain.js';
import { resolveArcNetwork } from './arc-network.js';
import {
  ENV_BROADCASTER_WALLET_ID,
  NotBroadcast,
  circleConfigFromEnv,
  loadCircleRelayer,
  type CircleRelayer,
  type CircleWalletConfig,
} from './circle-wallet.js';

const DEFAULT_USDC = '0x3600000000000000000000000000000000000000';

/** The canonical EIP-3009 `transferWithAuthorization` (the v,r,s overload — original EIP-3009, the
 *  most broadly implemented). If a chain exposes only the packed-signature overload, this is where
 *  that shows up as a first-live-call revert, and the ABI swaps here. */
const TRANSFER_WITH_AUTHORIZATION_ABI = [
  {
    type: 'function',
    name: 'transferWithAuthorization',
    stateMutability: 'nonpayable',
    inputs: [
      { name: 'from', type: 'address' },
      { name: 'to', type: 'address' },
      { name: 'value', type: 'uint256' },
      { name: 'validAfter', type: 'uint256' },
      { name: 'validBefore', type: 'uint256' },
      { name: 'nonce', type: 'bytes32' },
      { name: 'v', type: 'uint8' },
      { name: 'r', type: 'bytes32' },
      { name: 's', type: 'bytes32' },
    ],
    outputs: [],
  },
] as const;

/** The result of putting one authorization on chain. A real hash is the only proof of settlement. */
export interface BroadcastResult {
  txHash: Hex;
  status: 'success' | 'reverted';
}

/** Broadcasts one authorization. Production builds a viem client; tests inject a deterministic one
 *  so the settle logic is exercised without a chain — and cannot mint a fake hash into anything. */
export type Broadcast = (auth: Authorization, signature: Hex) => Promise<BroadcastResult>;

export interface SelfFacilitatorConfig {
  privateKey?: string;
  /** A Circle wallet as the relayer. Read from the environment when CIRCLE_BROADCASTER_WALLET_ID is set. */
  circle?: CircleWalletConfig;
  /** Test-only: how the Circle relayer polls Circle for the transaction. */
  circleTiming?: { pollMs?: number; timeoutMs?: number };
  /** Test-only: read a receipt's status without a chain. */
  receiptStatus?: (txHash: Hex) => Promise<'success' | 'reverted'>;
  rpcUrl?: string;
  usdcAddress?: string;
  chainId?: number;
  /** Test-only: replace the real on-chain broadcast. */
  broadcast?: Broadcast;
}

export class SelfFacilitator {
  private readonly usdcAddress: Address;
  private readonly chainId: number;
  private readonly chainLabel: string;
  private readonly rpcUrl: string;
  private readonly privateKey?: string;
  private readonly circle?: CircleWalletConfig;
  private readonly circleTiming?: { pollMs?: number; timeoutMs?: number };
  private readonly receiptStatus?: (txHash: Hex) => Promise<'success' | 'reverted'>;
  private relayer?: Promise<CircleRelayer>;
  private readonly injectedBroadcast?: Broadcast;

  constructor(cfg: SelfFacilitatorConfig = {}) {
    this.usdcAddress = (cfg.usdcAddress ?? process.env.ARC_USDC_ADDRESS ?? DEFAULT_USDC) as Address;
    // An explicit chainId still wins (tests), but the RPC is only ever defaulted from the network
    // that chainId names, so an injected mainnet id cannot inherit a testnet endpoint.
    const network = resolveArcNetwork(
      cfg.chainId === undefined
        ? process.env
        : { ...process.env, ARC_CHAIN_ID: String(cfg.chainId), ARC_TESTNET_CHAIN_ID: undefined },
    );
    this.chainId = network.chainId;
    this.chainLabel = network.label;
    this.rpcUrl = cfg.rpcUrl ?? network.rpcUrl;
    // The relayer key. Never logged, never returned.
    this.privateKey = cfg.privateKey ?? process.env.BROADCASTER_PRIVATE_KEY ?? undefined;
    // A malformed Circle configuration throws here, at startup, not at the first payment.
    this.circle =
      cfg.circle ??
      (process.env[ENV_BROADCASTER_WALLET_ID]?.trim()
        ? circleConfigFromEnv(process.env, ENV_BROADCASTER_WALLET_ID)
        : undefined);
    this.circleTiming = cfg.circleTiming;
    this.receiptStatus = cfg.receiptStatus;
    this.injectedBroadcast = cfg.broadcast;
  }

  /** Configured when it can actually broadcast: a Circle relayer, a relayer key, or a test-injected broadcaster. */
  isConfigured(): boolean {
    return (
      Boolean(this.injectedBroadcast) || Boolean(this.circle) || Boolean(this.privateKey && this.privateKey.length > 0)
    );
  }

  /** Which relayer submits. Logged at startup; never a key or a secret. */
  relayerKind(): 'circle' | 'key' | 'none' {
    if (this.circle) return 'circle';
    return this.privateKey ? 'key' : 'none';
  }

  /** Self-markers, NOT Circle URLs. On the receipt this reads as a self-relayed on-chain
   *  broadcast, so it can never masquerade as a Circle Gateway settlement. */
  endpoints(): { verify: string; settle: string } {
    return {
      verify: 'self:eip3009-local-recover',
      settle: `self:transferWithAuthorization@${this.usdcAddress}`,
    };
  }

  private domain() {
    return {
      name: USDC_EIP712_DOMAIN.name,
      version: USDC_EIP712_DOMAIN.version,
      chainId: this.chainId,
      verifyingContract: this.usdcAddress,
    } as const;
  }

  /** Local verification: recover the signer against the USDC precompile's own EIP-712 domain and
   *  confirm it is `from`, and that the authorization pays what OUR requirements demand. No network,
   *  and — unlike the handshake's local check — against the domain the CHAIN will use at settle. */
  async verify(payment: PaymentPayload, requirements: PaymentRequirements): Promise<VerifyOutcome> {
    const auth = payment?.payload?.authorization;
    const signature = payment?.payload?.signature as Hex | undefined;
    if (!auth || !signature) return { valid: false, reason: 'payment carries no authorization/signature' };

    let recovered: Address;
    try {
      recovered = await recoverTypedDataAddress({
        domain: this.domain(),
        types: TRANSFER_WITH_AUTHORIZATION_TYPES,
        primaryType: 'TransferWithAuthorization',
        message: {
          from: auth.from as Address,
          to: auth.to as Address,
          value: BigInt(auth.value),
          validAfter: BigInt(auth.validAfter),
          validBefore: BigInt(auth.validBefore),
          nonce: auth.nonce as Hex,
        },
        signature,
      });
    } catch (e) {
      return { valid: false, reason: `signature did not recover: ${(e as Error).message}` };
    }

    if (recovered.toLowerCase() !== auth.from.toLowerCase()) {
      return { valid: false, reason: `signature recovers to ${recovered}, not the authorizer ${auth.from}` };
    }
    if (auth.to.toLowerCase() !== requirements.payTo.toLowerCase()) {
      return { valid: false, reason: `authorization pays ${auth.to}, not our payee ${requirements.payTo}` };
    }
    if (BigInt(auth.value) < BigInt(requirements.maxAmountRequired)) {
      return { valid: false, reason: `authorization value ${auth.value} is below the required ${requirements.maxAmountRequired}` };
    }
    return { valid: true, payer: auth.from };
  }

  /** Broadcast the authorization. Returns `settled` ONLY on a successful on-chain receipt. */
  async settle(payment: PaymentPayload, requirements: PaymentRequirements): Promise<SettleOutcome> {
    if (!this.isConfigured()) return { state: 'not-configured' };

    const pre = await this.verify(payment, requirements);
    if (!pre.valid) return { state: 'rejected', reason: pre.reason ?? 'authorization failed verification' };

    const auth = payment.payload.authorization;
    const signature = payment.payload.signature as Hex;

    let result: BroadcastResult;
    try {
      result = await (this.injectedBroadcast ?? this.broadcastOnChain).call(this, auth, signature);
    } catch (e) {
      // Circle said it never reached the chain (or the relayer could not even load): nothing moved.
      if (e instanceof NotBroadcast) return { state: 'rejected', reason: `not broadcast: ${e.message}` };
      // The submit left this process. It may have been mined even if we never saw the receipt.
      return { state: 'unknown', reason: `settle did not return a receipt: ${(e as Error).message}` };
    }

    if (result.status === 'success') {
      return { state: 'settled', txHash: result.txHash, payer: auth.from };
    }
    // A reverted transferWithAuthorization moved nothing — safe, terminal.
    return { state: 'rejected', reason: `transferWithAuthorization reverted (tx ${result.txHash})` };
  }

  /** The real broadcast: submit the v,r,s authorization from the relayer wallet and wait for the
   *  receipt. Never invents a hash — the hash comes from the chain. */
  private async broadcastOnChain(auth: Authorization, signature: Hex): Promise<BroadcastResult> {
    const chain = defineChain({
      id: this.chainId,
      name: this.chainLabel,
      nativeCurrency: { name: 'USDC', symbol: 'USDC', decimals: 18 },
      rpcUrls: { default: { http: [this.rpcUrl] } },
    });
    const publicClient = createPublicClient({ chain, transport: http(this.rpcUrl) });

    const { r, s, v, yParity } = parseSignature(signature);
    const vByte = v ?? BigInt(27 + yParity);
    const args = [
      auth.from as Address,
      auth.to as Address,
      BigInt(auth.value),
      BigInt(auth.validAfter),
      BigInt(auth.validBefore),
      auth.nonce as Hex,
      Number(vByte),
      r,
      s,
    ] as const;

    let txHash: Hex;
    if (this.circle) {
      const callData = encodeFunctionData({ abi: TRANSFER_WITH_AUTHORIZATION_ABI, functionName: 'transferWithAuthorization', args });
      txHash = await (await this.circleRelayer()).submit(this.usdcAddress, callData);
    } else {
      if (!this.privateKey) throw new Error('no broadcaster key configured');
      const account = privateKeyToAccount(this.privateKey as Hex);
      const wallet = createWalletClient({ account, chain, transport: http(this.rpcUrl) });
      txHash = await wallet.writeContract({
        address: this.usdcAddress,
        abi: TRANSFER_WITH_AUTHORIZATION_ABI,
        functionName: 'transferWithAuthorization',
        args,
      });
    }
    if (this.receiptStatus) return { txHash, status: await this.receiptStatus(txHash) };
    const receipt = await publicClient.waitForTransactionReceipt({ hash: txHash });
    return { txHash, status: receipt.status === 'success' ? 'success' : 'reverted' };
  }

  /** The Circle relayer, loaded once. A load that fails is retried on the next payment, and is
   *  NotBroadcast: nothing was submitted. */
  private circleRelayer(): Promise<CircleRelayer> {
    if (!this.relayer) {
      this.relayer = loadCircleRelayer(this.circle!, { chainId: this.chainId, ...this.circleTiming }).catch((e) => {
        this.relayer = undefined;
        throw new NotBroadcast(`the Circle relayer did not load: ${(e as Error).message}`);
      });
    }
    return this.relayer;
  }
}
