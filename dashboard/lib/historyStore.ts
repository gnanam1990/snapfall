/**
 * Keeps the deployment's event history current without leaning on a rate-limited explorer.
 *
 * The public Arc explorer allows about ten logs-API calls per window per IP, and the public RPC
 * serves eth_getLogs only 10,000 blocks at a time. So the history is built in two parts:
 *
 *   1. Bootstrap once from the explorer (the whole history in two calls), noting the head block
 *      read from the RPC just before. The result is also written to a small file cache, so a
 *      restarted server does not spend the explorer budget again.
 *   2. Every refresh after that reads only the new blocks from the RPC — a tail of a few hundred
 *      blocks, one eth_getLogs call — and merges them in.
 *
 * Networks without an explorer API (mainnet) skip step 1 and scan the RPC from the deployment
 * block, which is small for a recent deployment.
 */

import { constants as fsc, promises as fs } from 'node:fs';
import { randomBytes } from 'node:crypto';
import os from 'node:os';
import path from 'node:path';
import {
  assembleHistory, fetchExplorerLogs, fetchRpcLogs, mergeLogs, rpcHead, type ChainHistory, type RawLog,
} from './chainHistory';
import type { RPCTransport } from './floatChain';

export interface StoreConfig {
  network: string;
  explorerUrl: string;
  explorerApi: string | null;
  addresses: string[];
  startBlock: number;
  rpc: RPCTransport;
  /**
   * Directory for the bootstrap cache. By default a per-user directory under os.tmpdir(), created
   * 0700. Whichever it is, the cache is used only while the directory and file are owned by this
   * process's user and nobody else can write to them. Null disables it.
   */
  cacheDir?: string | null;
  fetchImpl?: typeof fetch;
}

interface State {
  logs: RawLog[];
  /** Every block up to and including this one is in `logs`. */
  through: number;
  source: 'explorer' | 'rpc';
}

/** Explorer indexing can trail the node by a few blocks; re-read this margin from the RPC. */
const EXPLORER_LAG = 200;
const CACHE_MAX_AGE_MS = 24 * 60 * 60 * 1000;

export class HistoryStore {
  private state: State | null = null;

  constructor(private readonly config: StoreConfig) {}

  private cacheName(): string {
    const key = `${this.config.network}-${this.config.addresses.map((a) => a.toLowerCase().slice(2, 10)).join('-')}`;
    return `snapfall-history-${key}.json`;
  }

  /**
   * The cache directory, or null when it cannot be trusted. /tmp is shared on Linux, so a file
   * there could have been planted by another local user; only a directory this user owns, that
   * no one else can write to and that is not a symlink, is used.
   */
  private async cacheDir(): Promise<string | null> {
    if (this.config.cacheDir === null) return null;
    const uid = typeof process.getuid === 'function' ? process.getuid() : null;
    const dir = this.config.cacheDir ?? path.join(os.tmpdir(), `snapfall-history-${uid ?? 'user'}`);
    try {
      await fs.mkdir(dir, { recursive: true, mode: 0o700 });
      const st = await fs.lstat(dir);
      if (!st.isDirectory() || st.isSymbolicLink()) return null;
      if (uid !== null && st.uid !== uid) return null;
      if (st.mode & 0o022) return null;
      return dir;
    } catch {
      return null;
    }
  }

  private async readCache(): Promise<State | null> {
    const dir = await this.cacheDir();
    if (!dir) return null;
    let fh: fs.FileHandle | undefined;
    try {
      // O_NOFOLLOW: a symlink planted in place of the file is refused, not followed.
      fh = await fs.open(path.join(dir, this.cacheName()), fsc.O_RDONLY | (fsc.O_NOFOLLOW ?? 0));
      const st = await fh.stat();
      const uid = typeof process.getuid === 'function' ? process.getuid() : null;
      if (!st.isFile() || (uid !== null && st.uid !== uid) || st.mode & 0o022) return null;
      return this.validCache(JSON.parse(await fh.readFile('utf8')));
    } catch {
      return null;
    } finally {
      await fh?.close().catch(() => undefined);
    }
  }

  /** The cached state only if every field has the shape this store writes and every log is ours. */
  private validCache(raw: unknown): State | null {
    if (!raw || typeof raw !== 'object') return null;
    const r = raw as Record<string, unknown>;
    if (typeof r.savedAt !== 'number' || !Number.isFinite(r.savedAt)) return null;
    const age = Date.now() - r.savedAt;
    if (age < 0 || age > CACHE_MAX_AGE_MS) return null;
    if (r.source !== 'explorer' && r.source !== 'rpc') return null;
    const through = r.through;
    if (!Number.isSafeInteger(through) || (through as number) < this.config.startBlock) return null;
    if (!Array.isArray(r.logs)) return null;
    const ours = new Set(this.config.addresses.map((a) => a.toLowerCase()));
    const hex = /^0x[0-9a-fA-F]*$/;
    const hash = /^0x[0-9a-fA-F]{64}$/;
    for (const l of r.logs as unknown[]) {
      if (!l || typeof l !== 'object') return null;
      const g = l as Record<string, unknown>;
      if (typeof g.address !== 'string' || !ours.has(g.address.toLowerCase())) return null;
      if (!Array.isArray(g.topics) || !g.topics.every((t) => typeof t === 'string' && hash.test(t))) return null;
      if (typeof g.data !== 'string' || !hex.test(g.data)) return null;
      if (typeof g.txHash !== 'string' || !hash.test(g.txHash)) return null;
      if (!Number.isSafeInteger(g.blockNumber) || (g.blockNumber as number) < this.config.startBlock || (g.blockNumber as number) > (through as number)) return null;
      if (!Number.isSafeInteger(g.logIndex) || (g.logIndex as number) < 0) return null;
      if (g.timestamp !== null && (typeof g.timestamp !== 'number' || !Number.isFinite(g.timestamp))) return null;
    }
    return { logs: r.logs as RawLog[], through: through as number, source: r.source };
  }

  private async writeCache(state: State): Promise<void> {
    const dir = await this.cacheDir();
    if (!dir) return;
    const file = path.join(dir, this.cacheName());
    const tmp = `${file}.${randomBytes(6).toString('hex')}.tmp`;
    try {
      // Private to this user, written whole then renamed into place.
      await fs.writeFile(tmp, JSON.stringify({ ...state, savedAt: Date.now() }), { mode: 0o600, flag: 'wx' });
      await fs.rename(tmp, file);
    } catch {
      await fs.rm(tmp, { force: true }).catch(() => undefined);
      /* a read-only filesystem only costs the next cold start an explorer call */
    }
  }

  private async bootstrap(signal?: AbortSignal): Promise<State> {
    const { explorerApi, addresses, startBlock, rpc, fetchImpl } = this.config;
    const cached = await this.readCache();
    // A cache that claims blocks the chain has not reached yet would stop the tail; drop it.
    if (cached && cached.through <= (await rpcHead(rpc))) return cached;
    if (explorerApi) {
      const head = await rpcHead(rpc);
      let logs: RawLog[] = [];
      // One after the other: the explorer rate-limits bursts.
      for (const a of addresses) logs = mergeLogs(logs, await fetchExplorerLogs(explorerApi, a, startBlock, fetchImpl, signal));
      const state: State = { logs, through: Math.max(startBlock, head - EXPLORER_LAG), source: 'explorer' };
      await this.writeCache(state);
      return state;
    }
    const { logs, head } = await fetchRpcLogs(rpc, addresses, startBlock);
    const state: State = { logs, through: head, source: 'rpc' };
    await this.writeCache(state);
    return state;
  }

  /** The current history: bootstrapped on first use, then extended by an RPC tail. */
  async refresh(signal?: AbortSignal): Promise<ChainHistory> {
    if (!this.state) this.state = await this.bootstrap(signal);
    const { rpc, addresses } = this.config;
    const { logs, head } = await fetchRpcLogs(rpc, addresses, this.state.through + 1, { maxSpan: 400_000 });
    if (head >= this.state.through) {
      this.state = { ...this.state, logs: mergeLogs(this.state.logs, logs), through: head };
    }
    return this.snapshot();
  }

  /** What is held now, without touching the network; null before the first bootstrap. */
  snapshot(): ChainHistory {
    if (!this.state) throw new Error('history not loaded');
    return assembleHistory(this.state.logs, {
      network: this.config.network,
      explorerUrl: this.config.explorerUrl,
      source: this.state.source,
    });
  }

  get loaded(): boolean {
    return this.state !== null;
  }
}
