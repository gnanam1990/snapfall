/**
 * The treasury signer, held by a Circle Developer-Controlled Wallet instead of a raw key.
 *
 * `buyer.ts` needs exactly one thing from a signer: an EIP-712 signature over an EIP-3009
 * `TransferWithAuthorization`, plus the address it signs as. Until now that came from
 * `TREASURY_PRIVATE_KEY` in a shell file. This module asks Circle to sign instead, through
 * `POST /v1/w3s/developer/sign/typedData`, and wraps the result as a viem `LocalAccount` so the
 * buyer's guards and signing path do not change.
 *
 * ── What this is ─────────────────────────────────────────────────────────────────────────────
 *
 * DEVELOPER custody. Whoever holds `CIRCLE_API_KEY` and `CIRCLE_ENTITY_SECRET` can sign
 * anything this wallet can, including a payment authorization. The raw key leaves the file; a
 * signing authority on a server does not. It is the operator's treasury, never customer money,
 * and the daemon's treasury lane uses the same wallet (docs/CIRCLE-TREASURY-WALLET.md).
 *
 * ── Every signature is checked before it leaves ──────────────────────────────────────────────
 *
 * The returned signature is recovered locally and must recover to the wallet's address. That
 * catches a smart-contract account (whose signature only verifies through ERC-1271, which
 * USDC's `transferWithAuthorization` does not call for an EOA-shaped authorization), a wrong
 * wallet, or a malformed answer, and it catches them before a seller is handed an authorization
 * that can never settle. The wallet must also be an EOA at load time, for the same reason.
 *
 * ── Secrets ──────────────────────────────────────────────────────────────────────────────────
 *
 * The entity secret is encrypted afresh (RSA-OAEP, SHA-256) for every request; Circle rejects a
 * reused ciphertext. Neither secret appears in an error. The API base URL is not configurable
 * from the environment, for the reason `facilitator.ts` gives: an env var that redirects the
 * client also redirects the bearer key.
 */

import { constants, publicEncrypt, randomUUID } from 'node:crypto';
import { toAccount } from 'viem/accounts';
import {
  getAddress,
  isAddress,
  recoverTypedDataAddress,
  type Address,
  type Hex,
  type LocalAccount,
  type TypedDataDefinition,
} from 'viem';

export const ENV_API_KEY = 'CIRCLE_API_KEY';
export const ENV_ENTITY_SECRET = 'CIRCLE_ENTITY_SECRET';
export const ENV_WALLET_ID = 'CIRCLE_TREASURY_WALLET_ID';
/** The self-facilitator's relayer (self-facilitator.ts): pays gas to submit authorizations. */
export const ENV_BROADCASTER_WALLET_ID = 'CIRCLE_BROADCASTER_WALLET_ID';

const CIRCLE_API = 'https://api.circle.com';

/** Circle's blockchain name for each Arc chain id. Only Arc: the payments this signs are on Arc. */
const BLOCKCHAIN_BY_CHAIN: Record<number, string> = {
  5042: 'ARC',
  5042002: 'ARC-TESTNET',
};

type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

export interface CircleWalletConfig {
  apiKey: string;
  entitySecretHex: string;
  walletId: string;
  /** Injected only by tests. */
  baseUrl?: string;
  fetchImpl?: FetchLike;
}

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const SIG_RE = /^0x[0-9a-fA-F]{130}$/;

/** Read the three Circle variables, refusing (by NAME, never value) when one is missing or malformed. */
export function circleConfigFromEnv(
  env: NodeJS.ProcessEnv = process.env,
  walletVar: string = ENV_WALLET_ID,
): CircleWalletConfig {
  const get = (name: string): string => {
    const v = env[name]?.trim();
    if (!v) throw new Error(`${name} is not set; refusing to construct the Circle wallet for ${walletVar}`);
    return v;
  };
  return validate(
    {
      apiKey: get(ENV_API_KEY),
      entitySecretHex: get(ENV_ENTITY_SECRET),
      walletId: get(walletVar),
    },
    walletVar,
  );
}

function validate(cfg: CircleWalletConfig, walletVar: string = ENV_WALLET_ID): CircleWalletConfig {
  if (cfg.apiKey.split(':').length !== 3) {
    throw new Error(`${ENV_API_KEY} is not in Circle's PREFIX:ID:SECRET form`);
  }
  if (!/^(0x)?[0-9a-fA-F]{64}$/.test(cfg.entitySecretHex)) {
    throw new Error(`${ENV_ENTITY_SECRET} is not a 32-byte hex entity secret`);
  }
  if (!UUID_RE.test(cfg.walletId)) {
    throw new Error(`${walletVar} is not a wallet id (UUID)`);
  }
  return cfg;
}

class CircleHttpError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
  }
}

class CircleApi {
  private readonly base: string;
  private readonly doFetch: FetchLike;
  private publicKeyPem?: string;

  constructor(private readonly cfg: CircleWalletConfig) {
    this.base = (cfg.baseUrl ?? CIRCLE_API).replace(/\/$/, '');
    this.doFetch = cfg.fetchImpl ?? ((input, init) => fetch(input, init));
  }

  async call<T>(method: 'GET' | 'POST', path: string, body?: unknown): Promise<T> {
    const res = await this.doFetch(this.base + path, {
      method,
      headers: {
        Authorization: `Bearer ${this.cfg.apiKey}`,
        ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await res.text();
    if (!res.ok) {
      let code = 0;
      let message = '';
      try {
        const e = JSON.parse(text) as { code?: number; message?: string };
        code = e.code ?? 0;
        message = e.message ?? '';
      } catch {
        // An unparseable error body is reported by status alone.
      }
      throw new CircleHttpError(`Circle ${method} ${path}: HTTP ${res.status}: code ${code}: ${message}`, res.status);
    }
    return JSON.parse(text) as T;
  }

  /** A fresh ciphertext for one request. */
  async ciphertext(): Promise<string> {
    if (!this.publicKeyPem) {
      const r = await this.call<{ data: { publicKey: string } }>('GET', '/v1/w3s/config/entity/publicKey');
      this.publicKeyPem = r.data.publicKey;
    }
    const secret = Buffer.from(this.cfg.entitySecretHex.replace(/^0x/, ''), 'hex');
    return publicEncrypt(
      { key: this.publicKeyPem, padding: constants.RSA_PKCS1_OAEP_PADDING, oaepHash: 'sha256' },
      secret,
    ).toString('base64');
  }
}

/** JSON for Circle's `data` field: bigints as decimal strings, and the domain's type spelled out. */
function typedDataJson(td: TypedDataDefinition): string {
  const domain = (td.domain ?? {}) as Record<string, unknown>;
  const domainFields: { name: string; type: string }[] = [];
  if (domain.name !== undefined) domainFields.push({ name: 'name', type: 'string' });
  if (domain.version !== undefined) domainFields.push({ name: 'version', type: 'string' });
  if (domain.chainId !== undefined) domainFields.push({ name: 'chainId', type: 'uint256' });
  if (domain.verifyingContract !== undefined) domainFields.push({ name: 'verifyingContract', type: 'address' });
  if (domain.salt !== undefined) domainFields.push({ name: 'salt', type: 'bytes32' });
  const types = { EIP712Domain: domainFields, ...(td.types as Record<string, unknown>) };
  return JSON.stringify({ types, domain, primaryType: td.primaryType, message: td.message }, (_k, v) =>
    typeof v === 'bigint' ? v.toString() : v,
  );
}

/**
 * Fetch the wallet and refuse one that is not LIVE, not developer custody, not an EOA, or not on
 * Arc. EOA for both uses: an EIP-3009 authorization needs an EOA signature, and a smart account's
 * reported hash is its bundle transaction, which can succeed while the call inside reverted.
 */
async function openWallet(api: CircleApi, cfg: CircleWalletConfig): Promise<{ address: Address; blockchain: string }> {
  const { data } = await api.call<{
    data: {
      wallet: {
        address: string;
        blockchain: string;
        state: string;
        custodyType: string;
        accountType: string;
      };
    };
  }>('GET', `/v1/w3s/wallets/${cfg.walletId}`);
  const w = data.wallet;
  if (!Object.values(BLOCKCHAIN_BY_CHAIN).includes(w.blockchain)) {
    throw new Error(`Circle wallet ${cfg.walletId} is on ${w.blockchain}, not an Arc network`);
  }
  if (w.state !== 'LIVE') throw new Error(`Circle wallet ${cfg.walletId} is ${w.state}, not LIVE`);
  if (w.custodyType !== 'DEVELOPER') {
    throw new Error(`Circle wallet ${cfg.walletId} is ${w.custodyType} custody; the treasury signer must be developer-controlled`);
  }
  if (w.accountType !== 'EOA') {
    throw new Error(
      `Circle wallet ${cfg.walletId} is a ${w.accountType} account; it must be an EOA`,
    );
  }
  if (!isAddress(w.address)) throw new Error(`Circle wallet ${cfg.walletId} reported address ${w.address}`);
  return { address: getAddress(w.address), blockchain: w.blockchain };
}

/**
 * Load the Circle wallet as a viem account. The account can only sign typed data: message and
 * transaction signing throw, because nothing in the sidecar needs them and a signer that can do
 * less is a signer that can be misused less.
 */
export async function loadCircleAccount(cfg: CircleWalletConfig): Promise<LocalAccount> {
  validate(cfg);
  const api = new CircleApi(cfg);
  const { address, blockchain } = await openWallet(api, cfg);

  return toAccount({
    address,
    async signMessage() {
      throw new Error('the Circle treasury signer signs EIP-712 typed data only');
    },
    async signTransaction() {
      throw new Error('the Circle treasury signer signs EIP-712 typed data only');
    },
    async signTypedData(td) {
      const def = td as TypedDataDefinition;
      const chainId = Number((def.domain as { chainId?: number | bigint } | undefined)?.chainId);
      if (BLOCKCHAIN_BY_CHAIN[chainId] !== blockchain) {
        throw new Error(`refusing to sign for chain ${chainId}: the Circle wallet is on ${blockchain}`);
      }
      const r = await api.call<{ data: { signature: string } }>('POST', '/v1/w3s/developer/sign/typedData', {
        walletId: cfg.walletId,
        data: typedDataJson(def),
        memo: 'snapfall x402 payment authorization',
        entitySecretCiphertext: await api.ciphertext(),
      });
      const sig = r.data.signature;
      if (!SIG_RE.test(sig)) throw new Error('Circle returned a signature that is not 65-byte hex');
      const recovered = await recoverTypedDataAddress({ ...def, signature: sig as Hex });
      if (recovered !== address) {
        throw new Error(`Circle's signature recovers to ${recovered}, not the wallet ${address}; refusing to use it`);
      }
      return sig as Hex;
    },
  });
}

/**
 * Circle said it did not broadcast: the request was refused, or the transaction was denied,
 * cancelled, or failed before it had an on-chain hash. Nothing moved, so the caller may treat it
 * as terminal. Anything else that goes wrong after the request leaves is NOT this error: the
 * transaction may still land.
 */
export class NotBroadcast extends Error {}

/** A Circle wallet that submits contract calls and pays their gas. */
export interface CircleRelayer {
  address: Address;
  /**
   * Submit one call and return its on-chain hash once Circle reports it final. Whether the call
   * succeeded is the CHAIN's answer: the caller reads the receipt from its own RPC.
   */
  submit(contractAddress: Address, callData: Hex): Promise<Hex>;
}

export interface RelayerOptions {
  chainId: number;
  pollMs?: number;
  timeoutMs?: number;
}

/**
 * Load a Circle wallet as a relayer (`POST /v1/w3s/developer/transactions/contractExecution`).
 * Same refusals as the signer, plus the wallet must be on the chain it will submit to.
 */
export async function loadCircleRelayer(cfg: CircleWalletConfig, opts: RelayerOptions): Promise<CircleRelayer> {
  validate(cfg, ENV_BROADCASTER_WALLET_ID);
  const api = new CircleApi(cfg);
  const { address, blockchain } = await openWallet(api, cfg);
  if (BLOCKCHAIN_BY_CHAIN[opts.chainId] !== blockchain) {
    throw new Error(`Circle wallet ${cfg.walletId} is on ${blockchain}; it cannot submit to chain ${opts.chainId}`);
  }
  const pollMs = opts.pollMs ?? 2_000;
  const timeoutMs = opts.timeoutMs ?? 90_000;

  return {
    address,
    async submit(contractAddress, callData) {
      let id: string;
      try {
        const r = await api.call<{ data: { id: string } }>('POST', '/v1/w3s/developer/transactions/contractExecution', {
          idempotencyKey: randomUUID(),
          walletId: cfg.walletId,
          contractAddress,
          callData,
          feeLevel: 'MEDIUM',
          entitySecretCiphertext: await api.ciphertext(),
        });
        id = r.data.id;
      } catch (e) {
        // A 4xx is Circle refusing the request. A 5xx or a dropped connection may have been
        // accepted, so it is not NotBroadcast.
        if (e instanceof CircleHttpError && e.status >= 400 && e.status < 500) throw new NotBroadcast(e.message);
        throw e;
      }

      const deadline = Date.now() + timeoutMs;
      for (;;) {
        const { data } = await api.call<{
          data: { transaction: { state: string; txHash?: string; errorReason?: string } };
        }>('GET', `/v1/w3s/transactions/${id}`);
        const tx = data.transaction;
        const hash = tx.txHash && /^0x[0-9a-fA-F]{64}$/.test(tx.txHash) ? (tx.txHash as Hex) : undefined;
        switch (tx.state) {
          case 'CONFIRMED':
          case 'COMPLETE':
            if (hash) return hash;
            break;
          case 'FAILED':
            // With a hash it reached the chain, and the receipt says how. Without one it never did.
            if (hash) return hash;
            throw new NotBroadcast(`Circle transaction ${id} failed before broadcast: ${tx.errorReason ?? 'no reason given'}`);
          case 'CANCELLED':
          case 'DENIED':
            throw new NotBroadcast(`Circle transaction ${id} was ${tx.state}: ${tx.errorReason ?? 'no reason given'}`);
        }
        if (Date.now() > deadline) {
          throw new Error(`Circle transaction ${id} is still ${tx.state} after ${timeoutMs} ms; it may still land`);
        }
        await new Promise((r) => setTimeout(r, pollMs));
      }
    },
  };
}

/** Exposed for tests only. */
export const _test = { typedDataJson };
