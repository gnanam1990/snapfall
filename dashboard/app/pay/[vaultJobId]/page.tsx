'use client';

/**
 * Funding a job's escrow from the customer's own wallet.
 *
 * No token rides this link, and none is needed: everything shown is public chain state, and the
 * only action, JobVault.fund, is refused by the contract unless the caller IS the job's customer
 * (SC-JV-001). The passkey is the credential. A stranger who opens the link can sign in with
 * their own passkey and will be told, truthfully, that the escrow is not theirs.
 *
 * It is also where a new customer gets their wallet address before a job exists: the operator
 * names that address as the customer when creating the job, and only then can it be funded.
 */

import { Suspense } from 'react';
import { useParams } from 'next/navigation';
import { OwnWalletPanel } from '@/components/OwnWalletPanel';
import { passkeyConfig } from '@/lib/passkeyWallet';

const JOB_ID_RE = /^0x[0-9a-fA-F]{64}$/;

function PayInner() {
  const vaultJobId = String(useParams().vaultJobId ?? '');
  if (!JOB_ID_RE.test(vaultJobId)) {
    return (
      <div className="portal-card">
        <h1 className="portal-title">Invalid link</h1>
        <p className="portal-sub">This payment link does not name a job.</p>
      </div>
    );
  }
  if (!passkeyConfig()) {
    return (
      <div className="portal-card">
        <h1 className="portal-title">Not available</h1>
        <p className="portal-sub">Paying from your own wallet is not enabled for this deployment.</p>
      </div>
    );
  }
  return (
    <div className="portal-card">
      <p className="portal-brand">Snapfall · payment</p>
      <h1 className="portal-title">Fund your job</h1>
      <p className="portal-sub portal-jobid">Job {vaultJobId}</p>
      <p className="portal-body portal-lede">
        Your payment is held in escrow on chain and released to the operator only when you accept the
        delivery.
      </p>
      <OwnWalletPanel vaultJobId={vaultJobId} />
    </div>
  );
}

export default function PayPage() {
  return (
    <div className="portal-shell">
      <Suspense
        fallback={
          <div className="portal-card">
            <p className="portal-sub">Loading…</p>
          </div>
        }
      >
        <PayInner />
      </Suspense>
    </div>
  );
}
