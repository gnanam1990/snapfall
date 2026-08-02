/** Canonical atomic USDC: a base-10 unsigned integer string. Anything else is a
 *  malformed frame and must not throw on the render path (review: PR #8 HIGH). */
const ATOMIC_RE = /^\d+$/;

/** The single "no value" glyph, matching the em dash the stat cards, TreasuryHero and the
 *  Float page already render for an absent field. A money formatter must never surface the
 *  literal "undefined"/"null" for an absent aggregate field (the daemon legitimately omits
 *  fields it cannot source), so absent renders as this instead. */
const NO_VALUE = '—';

type ParsedAtomic =
  | { kind: 'value'; v: bigint }
  | { kind: 'absent' }
  | { kind: 'malformed'; raw: string };

/** One parse for both formatters. Absent (null/undefined) is distinguished from malformed
 *  (a non-empty non-integer wire value) so the caller can dash the former while keeping the
 *  latter visible. An empty string is treated as zero, the established meaning on the wire. */
function parseAtomic(atomic: string | bigint | null | undefined): ParsedAtomic {
  if (typeof atomic === 'bigint') return { kind: 'value', v: atomic };
  if (atomic === null || atomic === undefined) return { kind: 'absent' };
  if (atomic === '') return { kind: 'value', v: 0n };
  if (ATOMIC_RE.test(atomic)) return { kind: 'value', v: BigInt(atomic) };
  return { kind: 'malformed', raw: atomic };
}

/** Format atomic USDC (6dp) for humans: "12500000" -> "12.50".
 *  Absent (null/undefined) renders as a dash — never the string "undefined". A malformed
 *  non-empty frame stays visible (review: PR #8 HIGH), and nothing on the render path throws. */
export function formatUsdc(atomic: string | bigint | null | undefined): string {
  const p = parseAtomic(atomic);
  if (p.kind === 'absent') return NO_VALUE;
  if (p.kind === 'malformed') return p.raw;
  const whole = p.v / 1_000_000n;
  const frac = (p.v % 1_000_000n).toString().padStart(6, '0').slice(0, 2);
  return `${whole.toLocaleString('en-US')}.${frac}`;
}

/** Format atomic USDC without hiding sub-cent amounts. The Float dashboard is an
 *  accounting surface, so it keeps up to all six on-chain decimal places while
 *  retaining two decimals for whole-cent values. Same absent/malformed contract as formatUsdc. */
export function formatUsdcExact(atomic: string | bigint | null | undefined): string {
  const p = parseAtomic(atomic);
  if (p.kind === 'absent') return NO_VALUE;
  if (p.kind === 'malformed') return p.raw;
  const whole = p.v / 1_000_000n;
  let frac = (p.v % 1_000_000n).toString().padStart(6, '0');
  while (frac.length > 2 && frac.endsWith('0')) frac = frac.slice(0, -1);
  return `${whole.toLocaleString('en-US')}.${frac}`;
}

/** Basis points -> percent string: 5000 -> "50%". Absent or non-finite renders as a dash. */
export function formatBps(bps: number | null | undefined): string {
  if (bps === null || bps === undefined || !Number.isFinite(bps)) return NO_VALUE;
  return `${(bps / 100).toFixed(bps % 100 === 0 ? 0 : 1)}%`;
}

/** A deadline, decomposed so the caller can style urgency without re-deriving it. */
export type Countdown = {
  /** Human text: "4m left", "expired", or "" when the timestamp is unreadable. */
  text: string;
  /** True once the window has closed. The daemon rejects a decision at this point. */
  expired: boolean;
  /** Whole seconds remaining; 0 once expired, NaN when the timestamp is unreadable. */
  secondsLeft: number;
};

/**
 * Time remaining until a future instant.
 *
 * relativeTime cannot express this: it clamps with `Math.max(0, now - t)`, so every future
 * timestamp collapses to "just now". The approvals inbox printed "Expires just now" on every
 * unexpired request because of that -- the only pressure signal on a server-enforced window,
 * carrying no information at all.
 *
 * Seconds are kept below a minute because the last minute of an approval window is exactly when
 * the owner needs to know whether it is worth typing a reason.
 */
export function timeUntil(iso: string, now: number = Date.now()): Countdown {
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t)) return { text: '', expired: false, secondsLeft: Number.NaN };

  const secondsLeft = Math.floor((t - now) / 1000);
  if (secondsLeft <= 0) return { text: 'expired', expired: true, secondsLeft: 0 };
  if (secondsLeft < 60) return { text: `${secondsLeft}s left`, expired: false, secondsLeft };

  const minutes = Math.floor(secondsLeft / 60);
  if (minutes < 60) return { text: `${minutes}m left`, expired: false, secondsLeft };

  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  return {
    text: rest === 0 ? `${hours}h left` : `${hours}h ${rest}m left`,
    expired: false,
    secondsLeft,
  };
}

export function relativeTime(iso: string, now: number = Date.now()): string {
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t)) return '';
  const diff = Math.max(0, now - t);
  const s = Math.floor(diff / 1000);
  if (s < 5) return 'just now';
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  return `${h}h ago`;
}

/** Only link out to the Arc explorer over https - stream data is untrusted for hrefs
 *  (javascript:/data: URLs must never reach an <a>). */
export function isSafeExplorerUrl(u: string): boolean {
  try {
    const p = new URL(u);
    return (
      p.protocol === 'https:' &&
      (p.hostname === 'arcscan.app' || p.hostname === 'testnet.arcscan.app' || p.hostname.endsWith('.arcscan.app'))
    );
  } catch {
    return false;
  }
}
