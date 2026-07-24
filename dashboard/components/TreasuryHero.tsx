'use client';

import { useEffect, useRef, useState } from 'react';
import { formatUsdc, formatBps } from '@/lib/format';

export default function TreasuryHero({
  treasuryUsdc,
  orgRateBps,
}: {
  treasuryUsdc: string | null;
  orgRateBps: number | null;
}) {
  const [flash, setFlash] = useState(false);
  const prev = useRef(treasuryUsdc);

  useEffect(() => {
    if (prev.current === treasuryUsdc) return;
    prev.current = treasuryUsdc;
    setFlash(true);
    const t = setTimeout(() => setFlash(false), 650);
    return () => clearTimeout(t);
  }, [treasuryUsdc]);

  return (
    <div className="hero">
      <div>
        <div className="stat-label">Org treasury</div>
        <div className={`hero-num${flash ? ' flash' : ''}`}>
          {treasuryUsdc === null ? '—' : formatUsdc(treasuryUsdc)}{' '}
          {treasuryUsdc === null ? null : <span className="u">USDC</span>}
        </div>
        <div className="stat-sub">
          {treasuryUsdc === null ? 'awaiting chain indexer' : 'working capital drawn against escrowed receivables'}
        </div>
      </div>
      <div className="hero-right">
        <div className="chip-arc">
          ◆ {orgRateBps === null ? 'Advance rate unavailable' : `Advance rate ${formatBps(orgRateBps)}`}
        </div>
        <div className="stat-sub" style={{ marginTop: 10 }}>self-improving on-chain credit</div>
      </div>
    </div>
  );
}
