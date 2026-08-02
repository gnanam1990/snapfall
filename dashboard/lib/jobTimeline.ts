import type { ActivityMessage } from './activity';

/**
 * Which streamed events belong on ONE job's timeline.
 *
 * This was an inline predicate on the page and it was wrong in both directions:
 *
 *   if (item.jobId && item.jobId !== jobId) return;   // the old filter
 *
 * An event carrying no jobId at all fell through the `item.jobId &&` guard and was RENDERED, so
 * global events landed on a specific job's timeline claiming to be about it. And an event whose
 * jobId is the daemon's business id ("job_spine_2026…") never equals the bytes32 vault id in the
 * route, so every daemon-side event — every approval, every policy decision, every payment — was
 * dropped from the one page whose job is to explain them.
 *
 * The net effect read as "quiet job": unrelated noise shown, this job's own history hidden.
 */

/**
 * The identities one job answers to.
 *
 * A job has two, and they are not derivable from each other: the daemon's business id (its
 * `jobs.id`) and the on-chain bytes32 vault id (`jobs.vault_job_id`), which is generated rather
 * than hashed from the business id. Chain events are keyed by the second, daemon events by the
 * first, and both belong on this page.
 */
export interface JobIdentities {
  /** The bytes32 vault job id — the route parameter, and what chain events carry. */
  vaultJobId: string;
  /** The daemon's business job id, when the page has been able to learn it. */
  daemonJobId?: string | null;
}

/** Hex ids differ in case between sources, so compare them case-insensitively. */
function sameId(a: string, b: string): boolean {
  return a === b || a.toLowerCase() === b.toLowerCase();
}

/**
 * True when this event is about this job.
 *
 * Fails CLOSED: an event that names no job is not shown. A job timeline is a claim that
 * everything on it concerns that job, and an event which cannot be attributed does not support
 * that claim. Showing it anyway is how a global rate change ends up looking like part of one
 * job's story.
 */
export function belongsToJob(item: Pick<ActivityMessage, 'jobId' | 'chainRef'>, ids: JobIdentities): boolean {
  // chainRef first: it is the daemon stating which on-chain job its event concerns, which is
  // strictly better evidence than any id the page can pattern-match. It is what lets a daemon
  // event reach this page at all without the page having to learn the business id.
  if (item.chainRef && sameId(item.chainRef, ids.vaultJobId)) return true;

  const eventJob = item.jobId;
  if (!eventJob) return false;
  if (sameId(eventJob, ids.vaultJobId)) return true;
  if (ids.daemonJobId && sameId(eventJob, ids.daemonJobId)) return true;
  return false;
}

/**
 * One on-chain expense, as the job page shows it.
 *
 * The page previously displayed only a SUM of expenses read from the contract, so "0.10 spent"
 * had nothing behind it: no merchant, no time, no transaction to open. These rows come from the
 * ExpenseRecorded chain events the H2 relay now delivers, which is what makes per-row provenance
 * possible at all — before the relay these events never reached the browser.
 */
export interface ExpenseRow {
  id: string;
  at: string;
  amountUsdc?: string;
  /** Present when the event carried one; the row renders as text rather than a link without it. */
  explorerUrl?: string;
  text: string;
}

/**
 * Pulls the expense rows out of a job's timeline, newest first.
 *
 * Deliberately derived from the SAME filtered timeline the page renders rather than from a second
 * subscription: two lists built from two sources drift, and a per-row list that disagrees with the
 * total above it is worse than no rows at all.
 */
export function expenseRows(timeline: ActivityMessage[]): ExpenseRow[] {
  return timeline
    .filter((item) => item.kind === 'ExpenseRecorded')
    .map((item) => ({
      id: item.id,
      at: item.at,
      amountUsdc: item.amountUsdc,
      explorerUrl: item.explorerUrl,
      text: item.text,
    }));
}
