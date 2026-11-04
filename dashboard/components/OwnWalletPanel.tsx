'use client';

/**
 * The customer acting from their OWN wallet: a passkey on their device owning a Circle modular
 * smart account (lib/passkeyWallet.ts). Used twice: on /pay to fund a job's escrow, and on the
 * portal to accept a delivery. What the wallet may do is decided by lib/customerWallet.ts from
 * the chain's own view of the job, so this panel only ever offers the one action the contract
 * would accept from this address right now.
 *
 * The plain register of the portal applies: one wallet, one action, one sentence of
 * explanation. No meters, no operator vocabulary.
 */

import { useCallback, useEffect, useState } from 'react';
import { acceptCalls, fundCalls, walletAction, type JobView } from '@/lib/customerWallet';
import {
  connectPasskey,
  hasRememberedPasskey,
  passkeyConfig,
  type PasskeySession,
} from '@/lib/passkeyWallet';
import { formatUsdcExact } from '@/lib/format';
import { BridgeInPanel } from './BridgeInPanel';

interface ChainConfig {
  chainId: number;
  jobVault: string;
  usdc: string;
  explorerUrl: string;
}

export function OwnWalletPanel({
  vaultJobId,
  onAccepted,
}: {
  vaultJobId: string;
  /** Called after acceptDelivery succeeds on chain, so the portal can record the Accept. */
  onAccepted?: () => Promise<void>;
}) {
  const cfg = passkeyConfig();
  const [chain, setChain] = useState<ChainConfig | null>(null);
  const [job, setJob] = useState<JobView | null>(null);
  const [session, setSession] = useState<PasskeySession | null>(null);
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState<{ text: string; tone: 'ok' | 'error'; tx?: string } | null>(null);
  const [returning, setReturning] = useState(false);
  /** The Arc wallet's USDC (6dp ERC-20 surface). Null until read. */
  const [arcBalance, setArcBalance] = useState<bigint | null>(null);

  const loadJob = useCallback(async () => {
    const res = await fetch(`/api/jobs/${encodeURIComponent(vaultJobId)}`, { cache: 'no-store' });
    if (res.status === 404) {
      setJob({ exists: false, status: null, customer: null, customerPaymentUsdc: null });
      return;
    }
    if (!res.ok) throw new Error(`job read failed (${res.status})`);
    setJob((await res.json()) as JobView);
  }, [vaultJobId]);

  useEffect(() => {
    setReturning(hasRememberedPasskey());
    void (async () => {
      try {
        const res = await fetch('/api/chain/config', { cache: 'no-store' });
        if (res.ok) setChain((await res.json()) as ChainConfig);
        await loadJob();
      } catch {
        setNote({ text: 'The job could not be read from the chain just now. Refresh to try again.', tone: 'error' });
      }
    })();
  }, [loadJob]);

  const connect = useCallback(
    async (mode: 'login' | 'register') => {
      if (!cfg || !chain) return;
      setBusy(true);
      setNote(null);
      try {
        const s = await connectPasskey(cfg, chain.chainId, mode, name);
        setSession(s);
        setArcBalance(await s.balanceOf(chain.usdc).catch(() => null));
      } catch (e) {
        setNote({ text: `Passkey sign-in did not complete: ${(e as Error).message}`, tone: 'error' });
      } finally {
        setBusy(false);
      }
    },
    [cfg, chain, name],
  );

  const act = useCallback(async () => {
    if (!session || !chain || !job) return;
    const action = walletAction(job, session.address);
    setBusy(true);
    setNote(null);
    try {
      if (action.kind === 'fund') {
        const tx = await session.send(
          fundCalls({ usdc: chain.usdc, jobVault: chain.jobVault, jobId: vaultJobId, amountAtomic: action.amountAtomic }),
        );
        setNote({ text: 'Your payment is in escrow.', tone: 'ok', tx });
      } else if (action.kind === 'accept') {
        const tx = await session.send(acceptCalls({ jobVault: chain.jobVault, jobId: vaultJobId }));
        setNote({ text: 'Accepted and settled from your wallet.', tone: 'ok', tx });
        await onAccepted?.();
      }
      await loadJob();
    } catch (e) {
      setNote({ text: (e as Error).message, tone: 'error' });
    } finally {
      setBusy(false);
    }
  }, [session, chain, job, vaultJobId, onAccepted, loadJob]);

  if (!cfg) return null;

  const action = session && job ? walletAction(job, session.address) : null;
  const explorer = chain?.explorerUrl.replace(/\/$/, '');

  return (
    <section className="portal-wallet" aria-labelledby="own-wallet-title">
      <p id="own-wallet-title" className="portal-receipt-title">
        Your own wallet
      </p>

      {!session ? (
        <>
          <p className="portal-body">
            Sign with a passkey on this device. Only you can use it, and network fees are covered.
          </p>
          {!returning ? (
            <label className="portal-wallet-name">
              <span>Name for a new passkey (optional)</span>
              <input value={name} onChange={(e) => setName(e.target.value)} maxLength={50} autoComplete="username webauthn" />
            </label>
          ) : null}
          <div className="portal-wallet-actions">
            <button
              type="button"
              className={returning ? 'portal-accept' : 'portal-secondary'}
              disabled={busy || !chain}
              onClick={() => void connect('login')}
            >
              Sign in with passkey
            </button>
            <button
              type="button"
              className={returning ? 'portal-secondary' : 'portal-accept'}
              disabled={busy || !chain}
              onClick={() => void connect('register')}
            >
              Create a passkey wallet
            </button>
          </div>
        </>
      ) : (
        <>
          <p className="portal-proof">
            Wallet <code>{session.address}</code>
          </p>
          {action?.kind === 'fund' && arcBalance !== null && arcBalance < BigInt(action.amountAtomic) && chain ? (
            <BridgeInPanel
              session={session}
              chain={chain}
              vaultJobId={vaultJobId}
              priceAtomic={BigInt(action.amountAtomic)}
              arcBalance={arcBalance}
              onFunded={async (tx) => {
                setNote({ text: 'Your payment is in escrow.', tone: 'ok', tx });
                await loadJob();
              }}
            />
          ) : action?.kind === 'fund' ? (
            <button type="button" className="portal-accept" disabled={busy} aria-busy={busy || undefined} onClick={() => void act()}>
              {busy ? 'Paying…' : `Pay ${formatUsdcExact(action.amountAtomic)} USDC into escrow`}
            </button>
          ) : action?.kind === 'accept' ? (
            <>
              <p className="portal-body">
                Accepting releases your escrowed payment and settles the job on chain. This cannot be undone.
              </p>
              <button type="button" className="portal-accept" disabled={busy} aria-busy={busy || undefined} onClick={() => void act()}>
                {busy ? 'Settling…' : 'Accept & settle from my wallet'}
              </button>
            </>
          ) : action?.kind === 'not-customer' ? (
            <p className="portal-body">
              This job’s escrow belongs to <code>{action.customer}</code>, not this wallet. Sign in with that
              wallet’s passkey, or send this wallet’s address to the operator before the job is created.
            </p>
          ) : action ? (
            <p className="portal-body">{action.reason}</p>
          ) : null}
        </>
      )}

      <div aria-live="polite" role="status">
        {note ? (
          <p className={`portal-note is-sentence${note.tone === 'error' ? ' is-error' : ''}`}>
            {note.text}
            {note.tx && explorer ? (
              <>
                {' '}
                <a href={`${explorer}/tx/${note.tx}`} target="_blank" rel="noreferrer">
                  View transaction
                </a>
              </>
            ) : null}
          </p>
        ) : null}
      </div>
    </section>
  );
}
