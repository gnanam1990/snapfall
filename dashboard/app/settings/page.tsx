/**
 * Settings: what this deployment is actually configured to do.
 *
 * This was a 15-line stub, and its one sentence was the most dangerous claim in the product:
 * "Includes the global kill switch: stop new tasks, signatures, and advances."
 *
 * The kill switch is real -- daemon/internal/freeze holds a registry, and it is enforced:
 * ownerapi/accept.go:32 and ownerapi/advance.go:35 both return 423 FROZEN when it is engaged. But
 * there is NO freeze route on the owner API. The routes are health, approvals, stream, invoice,
 * accept-link, advance, workforce and the three customer ones; nothing can engage or read the
 * switch over HTTP, and only cmd/freeze-demo drives it, in process. So this page promised a
 * safety control it cannot operate, to an operator who would go looking for it during exactly
 * the incident where it mattered.
 *
 * No button was added. A control that 404s mid-incident is worse than no control, so the page
 * says where the switch actually lives instead. The section below is deliberately the plainest
 * on the surface: this is a safety statement, not a feature.
 *
 * What the page does instead is answer the question an operator actually has here -- why does
 * the rest of the dashboard look like this? Every absence elsewhere (no advance rate, no daemon,
 * no jobs) traces to a configuration value, so those are listed with what each one enables.
 *
 * A server component, so it can read process.env. It emits PRESENCE ONLY, never a value: the
 * owner token and the RPC URL are secrets, and a settings page that printed them would be a
 * worse leak than anything it documents.
 */

import deployment from '../../../deployments/arc-testnet.json';

export const metadata = { title: 'Settings · Snapfall' };

const isSet = (name: string): boolean => {
  const v = process.env[name];
  return typeof v === 'string' && v.trim() !== '';
};

/**
 * Every environment variable this dashboard reads, and what its absence costs.
 *
 * The "enables" text is the point: an unset variable is not a warning, it is the explanation for
 * something the operator has already noticed somewhere else in the product.
 */
const CONFIG = [
  {
    name: 'SNAPFALL_OWNER_API_URL',
    enables:
      'The daemon connection. Without it there is no event stream, no approvals inbox, no job list and no workforce activation — those surfaces say so rather than showing zeros.',
    secret: false,
  },
  {
    name: 'SNAPFALL_OWNER_TOKEN',
    enables:
      'Bearer credential for the owner API. Only meaningful when the daemon is configured and requires one.',
    secret: true,
  },
  {
    name: 'SNAPFALL_TREASURY_ADDRESS',
    enables:
      'The organisation whose advance rate is read from FloatPool. Unset is why Overview and Float show no advance rate — the eth_call is never issued, rather than issued and failing.',
    secret: false,
  },
  {
    name: 'ARC_TESTNET_RPC',
    enables:
      'A private RPC endpoint. Without it the public endpoint is used, which rate-limits historical log scans, so Float shows no rate history.',
    secret: true,
  },
  {
    name: 'SNAPFALL_FLOAT_SCAN_PUBLIC_RPC',
    enables: 'Opt in to running the historical log scan against the public endpoint anyway.',
    secret: false,
  },
  {
    name: 'SNAPFALL_DEMO_STREAM',
    enables:
      'Replays a scripted fixture instead of a live daemon. Surfaces label it "demo replay" when on, and it is stripped from the deployed artifact.',
    secret: false,
  },
  {
    name: 'SNAPFALL_JOB_VAULT_ADDRESS',
    enables: 'Overrides the JobVault address from the deployment record.',
    secret: false,
  },
  {
    name: 'SNAPFALL_FLOAT_POOL_ADDRESS',
    enables: 'Overrides the FloatPool address from the deployment record.',
    secret: false,
  },
  {
    name: 'ARC_USDC_ADDRESS',
    enables: 'Overrides the USDC token address used for the 6dp treasury balance read.',
    secret: false,
  },
  {
    name: 'SNAPFALL_DEPLOYMENT_BLOCK',
    enables: 'Overrides the block the historical scan starts from.',
    secret: false,
  },
  {
    name: 'SNAPFALL_FLOAT_SCAN_CHUNK',
    enables: 'Per-request eth_getLogs range cap, tuned to the RPC provider.',
    secret: false,
  },
];

export default function SettingsPage() {
  const rows = CONFIG.map((c) => ({ ...c, set: isSet(c.name) }));
  const connected = rows.find((r) => r.name === 'SNAPFALL_OWNER_API_URL')?.set ?? false;

  return (
    <>
      <div className="page-header">
        <div className="page-header-text">
          <h1>Settings</h1>
          <p className="page-header-sub">
            What this deployment is configured to do, and what it cannot do. Values are never
            shown — only whether each is set.
          </p>
        </div>
        <span className="page-header-aside">
          <span className={`status-badge ${connected ? 'Funded' : 'Cancelled'}`}>
            {connected ? 'daemon configured' : 'no daemon configured'}
          </span>
        </span>
      </div>

      <section className="card" aria-labelledby="settings-chain">
        <h2 className="card-title" id="settings-chain">
          Chain
        </h2>
        <dl className="settings-facts">
          <div>
            <dt>network</dt>
            <dd>
              {deployment.network.name} · chain {deployment.network.chainId}
            </dd>
          </div>
          <div>
            <dt>JobVault</dt>
            <dd className="settings-mono">{deployment.contracts.jobVault.address}</dd>
          </div>
          <div>
            <dt>FloatPool</dt>
            <dd className="settings-mono">{deployment.contracts.floatPool.address}</dd>
          </div>
          <div>
            <dt>AuditAnchor</dt>
            <dd className="settings-mono">{deployment.contracts.auditAnchor.address}</dd>
          </div>
        </dl>
        <p className="settings-note">
          Contracts are frozen. These come from the repository’s deployment record, so they are
          correct with nothing else configured.
        </p>
      </section>

      <section className="card mt" aria-labelledby="settings-config">
        <h2 className="card-title" id="settings-config">
          Configuration
        </h2>
        <p className="settings-lede">
          Presence only. No value is read into this page, because two of these are credentials.
        </p>
        <ul className="settings-list">
          {rows.map((r) => (
            <li key={r.name} className="settings-row">
              <div className="settings-row-main">
                <code className="settings-key">{r.name}</code>
                {r.secret ? <span className="settings-secret">credential</span> : null}
                <p className="settings-enables">{r.enables}</p>
              </div>
              <span className={`settings-state${r.set ? ' is-set' : ''}`}>
                {r.set ? 'set' : 'not set'}
              </span>
            </li>
          ))}
        </ul>
      </section>

      <section className="card mt" aria-labelledby="settings-freeze">
        <h2 className="card-title" id="settings-freeze">
          The kill switch
        </h2>
        <p className="settings-body">
          The freeze registry is real and it is enforced: with it engaged, the daemon refuses
          settlement actions and advance proposals with <code>423 FROZEN</code>, and the approvals
          lifecycle checks it at intake before a nonce is claimed.
        </p>
        <p className="settings-body">
          <strong>It cannot be operated from this dashboard.</strong> The owner API exposes no
          freeze route, so there is no button here — one that failed during the incident it exists
          for would be worse than none. It is driven in the daemon process today;{' '}
          <code>daemon/cmd/freeze-demo</code> shows the engaged, restart and released paths against
          the event log.
        </p>
        <p className="settings-note">
          This page previously stated that the global kill switch was included here. It was not,
          and saying so was the kind of claim this product exists to avoid making.
        </p>
      </section>
    </>
  );
}
