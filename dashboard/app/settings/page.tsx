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

import { contractAddress, deploymentFor } from '@/lib/network';
import { OwnerSessionPanel } from '@/components/OwnerSignIn';

const deployment = deploymentFor();

export const metadata = { title: 'Settings · Snapfall' };

// Rendered per request: this page reports which runtime env is set, and contract addresses can be
// runtime env too. Prerendered at `next build`, it described the build machine's environment.
export const dynamic = 'force-dynamic';

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
      'The daemon connection: approvals, live events and workforce.',
    secret: false,
  },
  {
    name: 'SNAPFALL_OWNER_TOKEN',
    enables:
      'Bearer credential for the owner API.',
    secret: true,
  },
  {
    name: 'SNAPFALL_DASHBOARD_OWNER_SECRET',
    enables:
      'The owner sign-in secret (32+ characters, not the owner token).',
    secret: true,
  },
  {
    name: 'SNAPFALL_DASHBOARD_HOSTS',
    enables:
      'Optional host allowlist for owner requests.',
    secret: false,
  },
  {
    name: 'SNAPFALL_DASHBOARD_ORIGIN',
    enables:
      'The public origin owner actions must come from, behind a proxy.',
    secret: false,
  },
  {
    name: 'SNAPFALL_DASHBOARD_TRUST_PROXY',
    enables:
      '1 only when your proxy overwrites X-Forwarded-Host.',
    secret: false,
  },
  {
    name: 'SNAPFALL_TREASURY_ADDRESS',
    enables:
      'The operator whose advance rate is shown.',
    secret: false,
  },
  {
    name: 'SNAPFALL_NETWORK',
    enables:
      'arc-testnet (unset) or arc-mainnet, fixed at build time.',
    secret: false,
  },
  {
    // ARC_TESTNET_RPC or ARC_MAINNET_RPC: the variable the selected deployment record names, so a
    // mainnet build never reads the testnet one.
    name: deployment.network.rpcUrlEnv,
    enables:
      'A private RPC for this network (the public one rate-limits history).',
    secret: true,
  },
  {
    name: 'SNAPFALL_FLOAT_SCAN_PUBLIC_RPC',
    enables: 'Opt in to running the historical log scan against the public endpoint anyway.',
    secret: false,
  },
  {
    name: 'SNAPFALL_EXPLORER_API',
    enables:
      'The explorer logs API for job and activity history.',
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
            What is configured. Values are never shown.
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
            <dd className="settings-mono">{contractAddress('jobVault') ?? `not deployed (set ${deployment.contracts.jobVault.addressEnv})`}</dd>
          </div>
          <div>
            <dt>FloatPool</dt>
            <dd className="settings-mono">{contractAddress('floatPool') ?? `not deployed (set ${deployment.contracts.floatPool.addressEnv})`}</dd>
          </div>
          <div>
            <dt>AuditAnchor</dt>
            <dd className="settings-mono">{contractAddress('auditAnchor') ?? `not deployed (set ${deployment.contracts.auditAnchor.addressEnv})`}</dd>
          </div>
        </dl>
      </section>

      <section className="card mt" aria-labelledby="settings-config">
        <h2 className="card-title" id="settings-config">
          Configuration
        </h2>
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

      <section className="card mt" aria-labelledby="settings-owner">
        <h2 className="card-title" id="settings-owner">
          Owner session
        </h2>
        <p className="settings-body">Sessions last 12 hours. Rotating the secret signs every browser out.</p>
        <OwnerSessionPanel />
      </section>

      <section className="card mt" aria-labelledby="settings-freeze">
        <h2 className="card-title" id="settings-freeze">
          The kill switch
        </h2>
        <p className="settings-body">
          Engaged, the daemon refuses settlements and advances with <code>423 FROZEN</code>.{' '}
          <strong>It is operated in the daemon, not from this dashboard.</strong>
        </p>
      </section>
    </>
  );
}
