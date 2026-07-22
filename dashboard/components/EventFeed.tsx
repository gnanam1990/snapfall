import type { FinancialEvent, EventCategory } from '@/lib/types';
import { formatUsdc, relativeTime } from '@/lib/format';

const CAT_COLOR: Record<EventCategory, string> = {
  Float: 'var(--accent)',
  Finance: 'var(--sky)',
  Approval: 'var(--warn)',
  Job: 'var(--pos)',
  Intake: 'var(--muted-2)',
  Task: 'var(--violet)',
  Agent: 'var(--violet)',
  Action: 'var(--sky)',
  Audit: 'var(--muted)',
};

export default function EventFeed({ events }: { events: FinancialEvent[] }) {
  if (!events.length) return <div className="empty">No events yet.</div>;
  return (
    <div className="feed">
      {events.map((e) => (
        <div className="feed-row" key={e.seq}>
          <span className="feed-dot" style={{ background: CAT_COLOR[e.category] }} />
          <div className="feed-body">
            <div className="feed-line">
              <span className="feed-summary">{e.summary}</span>
              {e.amountUsdc ? <span className="feed-amt">{formatUsdc(e.amountUsdc)}</span> : null}
            </div>
            <div className="feed-meta">
              <span className="feed-type">{e.type}</span>
              <span>·</span>
              <span>{relativeTime(e.ts)}</span>
              {e.explorerUrl ? (
                <>
                  <span>·</span>
                  <a className="feed-link" href={e.explorerUrl} target="_blank" rel="noreferrer">
                    explorer ↗
                  </a>
                </>
              ) : null}
            </div>
          </div>
        </div>
      ))}
    </div>
  );
}
