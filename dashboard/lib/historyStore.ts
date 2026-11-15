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

import { promises as fs } from 'node:fs';
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
  /** Directory for the bootstrap cache; os.tmpdir() by default. Null disables it. */
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

  private cacheFile(): string | null {
    if (this.config.cacheDir === null) return null;
    const key = `${this.config.network}-${this.config.addresses.map((a) => a.toLowerCase().slice(2, 10)).join('-')}`;
    return path.join(this.config.cacheDir ?? os.tmpdir(), `snapfall-history-${key}.json`);
  }

  private async readCache(): Promise<State | null> {
    const file = this.cacheFile();
    if (!file) return null;
    try {
      const raw = JSON.parse(await fs.readFile(file, 'utf8')) as State & { savedAt: number };
      if (Date.now() - raw.savedAt > CACHE_MAX_AGE_MS || !Array.isArray(raw.logs)) return null;
      return { logs: raw.logs, through: raw.through, source: raw.source };
    } catch {
      return null;
    }
  }

  private async writeCache(state: State): Promise<void> {
    const file = this.cacheFile();
    if (!file) return;
    try {
      await fs.writeFile(file, JSON.stringify({ ...state, savedAt: Date.now() }));
    } catch {
      /* a read-only filesystem only costs the next cold start an explorer call */
    }
  }

  private async bootstrap(signal?: AbortSignal): Promise<State> {
    const cached = await this.readCache();
    if (cached) return cached;
    const { explorerApi, addresses, startBlock, rpc, fetchImpl } = this.config;
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
