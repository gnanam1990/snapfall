/** Canonical atomic USDC: a base-10 unsigned integer string. Anything else is a
 *  malformed frame and must not throw on the render path (review: PR #8 HIGH). */
const ATOMIC_RE = /^\d+$/;

/** The shared safe atomic parser: canonical atomic USDC in, bigint out, null for
 *  anything malformed (decimal, hex, garbage). useEventStream validates JSON, not
 *  shape, so render-path callers must treat null as "omit" - never feed a raw stream
 *  string to BigInt(), which throws (review: PR #9). */
export function parseAtomicUsdc(atomic: string): bigint | null {
  return ATOMIC_RE.test(atomic) ? BigInt(atomic) : null;
}

/** Format atomic USDC (6dp) for humans: "12500000" -> "12.50".
 *  Tolerant by design: a malformed amount renders as-is instead of crashing the page;
 *  the H2 wire invariant (amounts are atomic integer strings) is validated, not assumed. */
export function formatUsdc(atomic: string | bigint): string {
  let v: bigint;
  if (typeof atomic === 'bigint') {
    v = atomic;
  } else if (atomic === '') {
    v = 0n;
  } else {
    const parsed = parseAtomicUsdc(atomic);
    if (parsed === null) {
      // Visible-but-safe fallback for a bad frame (e.g. "12.50", hex, garbage).
      return String(atomic);
    }
    v = parsed;
  }
  const whole = v / 1_000_000n;
  const frac = (v % 1_000_000n).toString().padStart(6, '0').slice(0, 2);
  return `${whole.toLocaleString('en-US')}.${frac}`;
}

/** Basis points -> percent string: 5000 -> "50%". */
export function formatBps(bps: number): string {
  if (!Number.isFinite(bps)) return '–';
  return `${(bps / 100).toFixed(bps % 100 === 0 ? 0 : 1)}%`;
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
