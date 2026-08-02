import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import type { OpenAdvance, JobSummary, PoolStats } from './types';

// The dashboard half of the H2 Overview aggregate wire contract (docs/handshakes/
// h2-overview-aggregates.contract.json). The daemon's contract_test.go pins its marshaled keys
// to `emits`; this pins the contract to the dashboard DTOs. Together they stop the mock and the
// daemon from drifting from what the Overview reads — the class of defect that let the aggregate
// send principalAtomic while the dashboard read principalUsdc, invisibly, until a live daemon.

const contract = JSON.parse(
  readFileSync(new URL('../../docs/handshakes/h2-overview-aggregates.contract.json', import.meta.url), 'utf8'),
) as {
  emits: { pool: string[]; openAdvance: string[]; activeJob: string[] };
  daemonOmits: { PoolStats: string[]; OpenAdvance: string[]; JobSummary: string[] };
};

// keyof-bound key manifests. `Record<keyof T, true>` forces every field of the DTO to appear
// here at COMPILE time — add or rename a field in types.ts and this file stops compiling until
// it is reflected, which then forces the contract update. This is the binding that makes the
// contract track the dashboard rather than drift from it.
const OPEN_ADVANCE_KEYS: Record<keyof OpenAdvance, true> = {
  jobId: true,
  org: true,
  principalUsdc: true,
  feeUsdc: true,
  rateBps: true,
  status: true,
};
const JOB_SUMMARY_KEYS: Record<keyof JobSummary, true> = {
  jobId: true,
  vaultJobId: true,
  customer: true,
  title: true,
  state: true,
  priceUsdc: true,
};
const POOL_STATS_KEYS: Record<keyof PoolStats, true> = {
  tvlUsdc: true,
  utilizationBps: true,
  feesAccruedUsdc: true,
  reserveUsdc: true,
  orgRateBps: true,
};

/** Every DTO field is either emitted by the daemon or explicitly documented as omitted (and
 *  rendered as a dash). Nothing may be unaccounted for, and nothing may be both. */
function assertPartition(dtoKeys: string[], emits: string[], omits: string[], label: string) {
  const overlap = emits.filter((k) => omits.includes(k));
  assert.deepEqual(overlap, [], `${label}: ${overlap.join(', ')} is both emitted and omitted`);
  const union = [...emits, ...omits].sort();
  const declared = [...dtoKeys].sort();
  assert.deepEqual(
    union,
    declared,
    `${label}: emits ∪ daemonOmits (${union.join(', ')}) must equal the DTO's keys (${declared.join(', ')}). ` +
      'A field the daemon must send but the contract omits would blank a column; an unknown key would never render.',
  );
}

test('OpenAdvance: the wire contract accounts for every DTO field', () => {
  assertPartition(Object.keys(OPEN_ADVANCE_KEYS), contract.emits.openAdvance, contract.daemonOmits.OpenAdvance, 'OpenAdvance');
});

test('JobSummary (activeJob): the wire contract accounts for every DTO field', () => {
  assertPartition(Object.keys(JOB_SUMMARY_KEYS), contract.emits.activeJob, contract.daemonOmits.JobSummary, 'JobSummary');
});

test('PoolStats (pool): the wire contract accounts for every DTO field', () => {
  assertPartition(Object.keys(POOL_STATS_KEYS), contract.emits.pool, contract.daemonOmits.PoolStats, 'PoolStats');
});
