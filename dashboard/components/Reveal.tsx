'use client';

import { useEffect, useRef, useState } from 'react';

/** Scroll-driven reveal: the element slides in from the right the first time it enters
 *  the viewport (IntersectionObserver, threshold 0.15) and then stays. */
export default function Reveal({
  children,
  delay = 0,
}: {
  children: React.ReactNode;
  delay?: number;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [shown, setShown] = useState(false);

  const [reduced, setReduced] = useState(false);

  useEffect(() => {
    setReduced(window.matchMedia('(prefers-reduced-motion: reduce)').matches);
    const el = ref.current;
    if (!el) return;
    const io = new IntersectionObserver(
      ([entry]) => {
        if (entry?.isIntersecting) {
          setShown(true);
          io.disconnect();
        }
      },
      { threshold: 0.15 },
    );
    io.observe(el);
    return () => io.disconnect();
  }, []);

  // Reduced motion: content is simply visible; no slide, no fade (review: PR #10 a11y).
  if (reduced) return <div ref={ref}>{children}</div>;

  return (
    <div
      ref={ref}
      className={`transition-all duration-700 ease-out ${shown ? 'translate-x-0 opacity-100' : 'translate-x-16 opacity-0'}`}
      style={{ transitionDelay: `${delay}ms` }}
    >
      {children}
    </div>
  );
}
