/**
 * Self-hosted x402 facilitator — the permissionless settlement path.
 *
 * ── Why this exists BESIDE the Circle Gateway client, not instead of it ──────────────────────
 *
 * `CircleFacilitator` settles by POSTing the authorization to Circle Gateway
 * (`gateway-api-testnet.circle.com/gateway/v1/x402/settle`). That integration is written and
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
 */
import {
  createPublicClient,
  createWalletClient,
  defineChain,
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

const DEFAULT_USDC = '0x3600000000000000000000000000000000000000';
const DEFAULT_RPC = 'https://rpc.testnet.arc.network';
const DEFAULT_CHAIN_ID = 5042002;

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
  rpcUrl?: string;
  usdcAddress?: string;
  chainId?: number;
  /** Test-only: replace the real on-chain broadcast. */
  broadcast?: Broadcast;
}

export class SelfFacilitator {
  private readonly usdcAddress: Address;
  private readonly chainId: number;
  private readonly rpcUrl: string;
  private readonly privateKey?: string;
  private readonly injectedBroadcast?: Broadcast;

  constructor(cfg: SelfFacilitatorConfig = {}) {
    this.usdcAddress = (cfg.usdcAddress ?? process.env.ARC_USDC_ADDRESS ?? DEFAULT_USDC) as Address;
    this.chainId = cfg.chainId ?? Number(process.env.ARC_TESTNET_CHAIN_ID ?? process.env.ARC_CHAIN_ID ?? DEFAULT_CHAIN_ID);
    this.rpcUrl = cfg.rpcUrl ?? process.env.ARC_TESTNET_RPC ?? DEFAULT_RPC;
    // The relayer key. Never logged, never returned.
    this.privateKey = cfg.privateKey ?? process.env.BROADCASTER_PRIVATE_KEY ?? undefined;
    this.injectedBroadcast = cfg.broadcast;
  }

  /** Configured when it can actually broadcast: a relayer key, or a test-injected broadcaster. */
  isConfigured(): boolean {
    return Boolean(this.injectedBroadcast) || Boolean(this.privateKey && this.privateKey.length > 0);
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
    if (!this.privateKey) throw new Error('no broadcaster key configured');
    const chain = defineChain({
      id: this.chainId,
      name: 'Arc testnet',
      nativeCurrency: { name: 'USDC', symbol: 'USDC', decimals: 18 },
      rpcUrls: { default: { http: [this.rpcUrl] } },
    });
    const account = privateKeyToAccount(this.privateKey as Hex);
    const wallet = createWalletClient({ account, chain, transport: http(this.rpcUrl) });
    const publicClient = createPublicClient({ chain, transport: http(this.rpcUrl) });

    const { r, s, v, yParity } = parseSignature(signature);
    const vByte = v ?? BigInt(27 + yParity);

    const txHash = await wallet.writeContract({
      address: this.usdcAddress,
      abi: TRANSFER_WITH_AUTHORIZATION_ABI,
      functionName: 'transferWithAuthorization',
      args: [
        auth.from as Address,
        auth.to as Address,
        BigInt(auth.value),
        BigInt(auth.validAfter),
        BigInt(auth.validBefore),
        auth.nonce as Hex,
        Number(vByte),
        r,
        s,
      ],
    });
    const receipt = await publicClient.waitForTransactionReceipt({ hash: txHash });
    return { txHash, status: receipt.status === 'success' ? 'success' : 'reverted' };
  }
}
