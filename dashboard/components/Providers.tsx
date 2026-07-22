'use client';

import { MotionConfig } from 'framer-motion';

/** App-level client providers. MotionConfig honors the OS reduced-motion setting for
 *  every framer-motion animation (review: PR #10 a11y). */
export default function Providers({ children }: { children: React.ReactNode }) {
  return <MotionConfig reducedMotion="user">{children}</MotionConfig>;
}
