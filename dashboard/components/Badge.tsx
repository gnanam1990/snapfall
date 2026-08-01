/**
 * Status as dot + label, the quiet register, rather than a filled pill.
 *
 * Ported from feat/ui-rework, which mapped each state to a tint in a TypeScript record and set the
 * colour inline. The tints live in CSS here instead (the .status-badge block in app/globals.css):
 * one declaration per state, the dot inheriting currentcolor, so the dot and the label cannot drift
 * apart and no colour is written in a component. Every tint clears WCAG AA 4.5:1 on every surface a
 * card can paint, in both themes: worst case 4.54:1 dark, 4.56:1 light.
 *
 * The state names come from the frozen contracts, so they are the class names too, matching the
 * .badge.<State> convention app/globals.css already uses for the older filled pills.
 */

/**
 * Whitelisted so an unrecognised state falls back to the neutral --muted tint, which is what the
 * ported record's `?? 'var(--color-muted)'` did, and so a caller cannot push arbitrary text into
 * the class attribute.
 */
const KINDS = new Set([
  'Funded',
  'Accepted',
  'Repaid',
  'InProgress',
  'Delivered',
  'DeliveryReady',
  'Issued',
  'AwaitingFunding',
  'Draft',
  'WrittenOff',
  'Refunded',
  'Cancelled',
  'Failed',
]);

export default function Badge({ kind }: { kind: string }) {
  return <span className={KINDS.has(kind) ? `status-badge ${kind}` : 'status-badge'}>{kind}</span>;
}
