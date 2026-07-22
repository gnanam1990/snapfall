export default function SettingsPage() {
  return (
    <>
      <div className="topbar">
        <div>
          <h1 className="page-title">Settings</h1>
          <p className="page-sub">Models, integrations, policies, wallet/chain, global freeze.</p>
        </div>
      </div>
      <div className="card">
        <p className="stat-sub">Includes the global kill switch: stop new tasks, signatures, and advances.</p>
      </div>
    </>
  );
}
