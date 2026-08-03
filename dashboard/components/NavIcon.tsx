/**
 * Navigation glyphs, drawn from the P&ID vocabulary rather than a generic icon set.
 *
 * The sidebar had no icons at all. Adding stock ones (a gear, a clipboard, a wallet) would have
 * imported a different visual language into a surface built as an engineering drawing, so each
 * glyph here is the symbol a P&ID actually uses for the thing the surface shows:
 *
 *   Overview    a board-mounted instrument bubble — a circle split by its horizontal diameter is
 *               the standard symbol for an instrument read at the panel rather than in the field,
 *               which is exactly what the Overview is.
 *   Approvals   a gate valve. The owner's refusal is the valve on the spend line, and the bowtie
 *               is the symbol for the thing a human turns to stop flow. This is the one glyph the
 *               product's mechanism makes literally correct rather than merely evocative.
 *   Float       the vessel with its level line, matching the tank drawn on the Overview.
 *   Audit       interlocking links: the hash chain AuditAnchor commits to.
 *   Jobs        a work order.
 *   Workforce   agents on a common line.
 *   Settings    a hex fitting — a drawn fastener, and not the gear every dashboard reaches for.
 *
 * Stroked at 1.25 to sit in the same weight as `.v-pipe`, on a 16-unit grid, inheriting
 * currentColor so the nav's own hover and active states paint them without extra rules.
 * aria-hidden because every icon sits beside its own text label; announcing them would make a
 * screen reader read each nav item twice.
 */

const PATHS: Record<string, React.ReactNode> = {
  // Board-mounted instrument: circle bisected by its horizontal diameter.
  '/': (
    <>
      <circle cx="8" cy="8" r="5.75" />
      <line x1="2.25" y1="8" x2="13.75" y2="8" />
    </>
  ),
  // Work order: a ticket carrying two ruled lines.
  '/jobs': (
    <>
      <rect x="2.75" y="2.75" width="10.5" height="10.5" rx="0.5" />
      <line x1="5.25" y1="6.25" x2="10.75" y2="6.25" />
      <line x1="5.25" y1="9.75" x2="9" y2="9.75" />
    </>
  ),
  // Agents on a common line.
  '/workforce': (
    <>
      <line x1="1.75" y1="8" x2="14.25" y2="8" />
      <circle cx="3.5" cy="8" r="1.75" />
      <circle cx="8" cy="8" r="1.75" />
      <circle cx="12.5" cy="8" r="1.75" />
    </>
  ),
  // Gate valve: two triangles meeting at the seat, with the stem and handwheel above.
  '/approvals': (
    <>
      <path d="M2.75 4.5 L2.75 11.5 L8 8 Z" />
      <path d="M13.25 4.5 L13.25 11.5 L8 8 Z" />
      <line x1="8" y1="8" x2="8" y2="3.25" />
      <line x1="5.5" y1="3.25" x2="10.5" y2="3.25" />
    </>
  ),
  // The vessel, with the level line the Overview tank also carries.
  '/float': (
    <>
      <rect x="3.25" y="2.25" width="9.5" height="11.5" rx="0.5" />
      <line x1="3.25" y1="9.25" x2="12.75" y2="9.25" />
    </>
  ),
  // Interlocking links: the anchored hash chain.
  '/audit': (
    <>
      <rect x="1.75" y="6.25" width="7.5" height="3.5" rx="1.75" />
      <rect x="6.75" y="6.25" width="7.5" height="3.5" rx="1.75" />
    </>
  ),
  // A hex fitting rather than a gear.
  '/settings': (
    <>
      <path d="M8 2.25 L12.75 5.25 L12.75 10.75 L8 13.75 L3.25 10.75 L3.25 5.25 Z" />
      <circle cx="8" cy="8" r="2" />
    </>
  ),
};

export default function NavIcon({ href }: { href: string }) {
  const glyph = PATHS[href];
  if (!glyph) return null;
  return (
    <svg
      className="nav-icon"
      viewBox="0 0 16 16"
      width="16"
      height="16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.25"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {glyph}
    </svg>
  );
}
