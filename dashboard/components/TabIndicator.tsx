'use client';

import { useLayoutEffect, useRef } from 'react';

/**
 * The ink pill behind the selected tab, which slides to the tab you pick.
 *
 * Adapted from taste-skill's motion layer (assets/motion/motion.js `tabs()`, MIT, © 2026 Leonxlnx):
 * the pill travels by transform only. It takes the new tab's size at once, is drawn scaled to the old
 * tab's size at the old place, then settles to scale 1 at the new place (globals.css, .m-tab-ind).
 * Rendered by React as the tablist's first child, so React keeps owning the DOM; it reads the
 * selected tab from aria-selected, which the tabs already set. Under reduced motion it jumps.
 */
export default function TabIndicator() {
  const ref = useRef<HTMLSpanElement>(null);

  useLayoutEffect(() => {
    const ind = ref.current;
    const list = ind?.parentElement;
    if (!ind || !list) return;
    list.classList.add('m-has-ind');
    const reduce = window.matchMedia('(prefers-reduced-motion: reduce)');
    let at: { x: number; y: number; w: number; h: number } | null = null;
    let instant = true;
    let alive = true;

    const set = (b: { x: number; y: number; w: number; h: number }, sx = 1, sy = 1) => {
      ind.style.setProperty('--x', `${b.x}px`);
      ind.style.setProperty('--y', `${b.y}px`);
      ind.style.setProperty('--w', `${b.w}px`);
      ind.style.setProperty('--h', `${b.h}px`);
      ind.style.setProperty('--sx', String(sx));
      ind.style.setProperty('--sy', String(sy));
    };
    const jump = (on: boolean) => ind.classList.toggle('is-jump', on);

    const place = () => {
      const sel = list.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]');
      if (!sel) {
        ind.style.opacity = '0';
        return;
      }
      ind.style.opacity = '';
      const to = { x: sel.offsetLeft, y: sel.offsetTop, w: sel.offsetWidth, h: sel.offsetHeight };
      if (instant || reduce.matches || !at || !to.w || !to.h) {
        jump(true);
        set(to);
        ind.getBoundingClientRect();
        jump(false);
      } else {
        jump(true);
        set({ ...to, x: at.x, y: at.y }, at.w / to.w, at.h / to.h);
        ind.getBoundingClientRect();
        jump(false);
        set(to);
      }
      at = to;
      instant = false;
    };

    place();
    const mo = new MutationObserver(place);
    mo.observe(list, { subtree: true, attributes: true, attributeFilter: ['aria-selected'] });
    const ro = new ResizeObserver(() => {
      instant = true;
      place();
    });
    ro.observe(list);
    void document.fonts?.ready.then(() => {
      if (!alive) return;
      instant = true;
      place();
    });
    return () => {
      alive = false;
      mo.disconnect();
      ro.disconnect();
      list.classList.remove('m-has-ind');
    };
  }, []);

  return <span ref={ref} className="m-tab-ind" aria-hidden="true" />;
}
