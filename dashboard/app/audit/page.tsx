export default function AuditPage() {
  return (
    <>
      <div className="topbar">
        <div>
          <h1 className="page-title">Audit</h1>
          <p className="page-sub">The receipt: revenue, advance, fee, expenses, margin, hashes, explorer links.</p>
        </div>
      </div>
      <div className="card">
        <p className="stat-sub">The receipt view is comprehensible without reading raw transactions (FR-UI-005).</p>
      </div>
    </>
  );
}
