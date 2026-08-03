/**
 * A valve on the spend line, drawn in the state the owner's decision put it in.
 *
 * This is the Approvals surface's one piece of drawing, and it earns its place because the
 * product's mechanism is literally this: an agent has reached a purchase it may not make alone,
 * so the line is shut and waiting for a hand. The nav already uses the gate-valve bowtie for this
 * surface (NavIcon.tsx); this is the same symbol carrying state.
 *
 *   pending    shut, with the stem still upright and the handwheel unturned. Flow is stopped.
 *   open       the owner approved: the gate is lifted clear and the line runs through.
 *   shut       the owner refused: the gate is seated and the line is dead downstream.
 *   diverted   the owner asked for a cheaper source: flow leaves on a branch instead.
 *
 * Colour is not set here. The Approvals surface is the one place the direction contract allows
 * colour, and it is applied by the row's own state class so the glyph and its lettering can never
 * disagree about what happened.
 */

export type ValveStateName = 'pending' | 'open' | 'shut' | 'diverted';

export default function ValveState({ state }: { state: ValveStateName }) {
  return (
    <svg
      className={`valve valve-${state}`}
      viewBox="0 0 24 24"
      width="24"
      height="24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.25"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {/* The run of pipe. Downstream of a shut gate it is drawn dead, so a refusal reads as a
          line that stops rather than as a button that changed colour. */}
      <line x1="0.5" y1="12" x2="6" y2="12" />
      <line
        x1="18"
        y1="12"
        x2="23.5"
        y2="12"
        className={state === 'shut' || state === 'pending' ? 'valve-dead' : undefined}
      />

      {/* The body: two triangles meeting at the seat. */}
      <path d="M6 7.5 L6 16.5 L12 12 Z" />
      <path d="M18 7.5 L18 16.5 L12 12 Z" />

      {/* Stem and handwheel. Lifted clear when open, seated when shut. */}
      <line x1="12" y1="12" x2="12" y2={state === 'open' ? 4.5 : 6.5} />
      <line
        x1="8.5"
        y1={state === 'open' ? 4.5 : 6.5}
        x2="15.5"
        y2={state === 'open' ? 4.5 : 6.5}
      />

      {/* The branch a request-for-cheaper sends flow down instead. */}
      {state === 'diverted' ? <path d="M12 12 L12 19 L19.5 19" /> : null}
    </svg>
  );
}
