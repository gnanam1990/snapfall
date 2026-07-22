import type { Variants } from 'framer-motion';

export const EASE_OUT: [number, number, number, number] = [0.22, 1, 0.36, 1];
export const EASE_IN: [number, number, number, number] = [0.55, 0, 1, 0.45];

/** Shared entrance system (from the design spec): fade-up with a custom ease and
 *  index-staggered delay. Usage: variants={fadeUp} custom={i} initial="hidden" animate="visible". */
export const fadeUp: Variants = {
  hidden: { opacity: 0, y: 28 },
  visible: (i: number = 0) => ({
    opacity: 1,
    y: 0,
    transition: { delay: i * 0.15, duration: 0.6, ease: EASE_OUT },
  }),
};

/** Softer, quicker variant for list rows and cards inside already-revealed sections. */
export const fadeIn: Variants = {
  hidden: { opacity: 0, y: 12 },
  visible: (i: number = 0) => ({
    opacity: 1,
    y: 0,
    transition: { delay: i * 0.06, duration: 0.4, ease: EASE_OUT },
  }),
};
