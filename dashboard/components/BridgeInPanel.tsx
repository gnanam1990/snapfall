'use client';

/**
 * Paying for a job with USDC that sits on another chain (CCTP V2, lib/cctp.ts).
 *
 * Shown by OwnWalletPanel only when the customer's Arc wallet holds less than the price. Two
 * signatures with the same passkey: a burn on the source chain, then ONE Arc user operation
 * that mints the attested burn and funds the escrow together (receiveMessage, approve, fund).
 *
 * The burn is the point of no return, so it is saved in this browser before anything else
 * happens. A customer who closes the tab while Circle attests comes back to "finish moving your
 * USDC", not to a lost payment; and if the mint was already relayed by someone else in the
 * meantime (destinationCaller is open), the nonce check skips it instead of reverting.
 */

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  CCTP,
  burnCalls,
  burnFor,
  fastFeeBps,
  finishPlan,
  parsePendingBurn,
  readAttestation,
  receiveCall,
  type CctpEnv,
  type CctpNetwork,
  type PendingBurn,
} from '@/lib/cctp';
import { fundCalls, type WalletCall } from '@/lib/customerWallet';
import type { ChainWallet, PasskeySession } from '@/lib/passkeyWallet';
import { formatUsdcExact } from '@/lib/format';
import { submitBurnOnce, burnStorageKey } from '@/lib/cctpBurnGuard';

type Pending = PendingBurn;

const storeKey = burnStorageKey;

function loadPending(vaultJobId: string, env: CctpEnv): Pending | null {
  try {
    return parsePendingBurn(window.localStorage.getItem(storeKey(vaultJobId)), env);
  } catch {
    return null;
  }
}
function savePending(vaultJobId: string, p: Pending | null) {
  try {
    if (p) window.localStorage.setItem(storeKey(vaultJobId), JSON.stringify(p));
    else window.localStorage.setItem(storeKey(vaultJobId), JSON.stringify({ status: 'completed' }));
  } catch {
    // Keep any prior pending record on write failure: never silently authorize another burn.
  }
}

export function BridgeInPanel({
  session,
  chain,
  vaultJobId,
  priceAtomic,
  arcBalance,
  onFunded,
}: {
  session: PasskeySession;
  chain: { chainId: number; jobVault: string; usdc: string; explorerUrl: string };
  vaultJobId: string;
  priceAtomic: bigint;
  arcBalance: bigint;
  onFunded: (tx: string) => Promise<void>;
}) {
  const env = CCTP[chain.chainId];
  const source: CctpNetwork | undefined = env?.sources[0];
  const [src, setSrc] = useState<ChainWallet | null>(null);
  const [srcBalance, setSrcBalance] = useState<bigint | null>(null);
  const [plan, setPlan] = useState<{ burn: bigint; maxFee: bigint } | null>(null);
  const [pending, setPending] = useState<Pending | null>(null);
  const [status, setStatus] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const polling = useRef(false);
  const starting = useRef(false);

  useEffect(() => {
    if (!env || !source) return;
    setPending(loadPending(vaultJobId, env));
    void (async () => {
      try {
        const wallet = await session.onSource(source);
        setSrc(wallet);
        setSrcBalance(await wallet.balanceOf(source.usdc));
        const fees = await fetch(`/api/cctp/fee?source=${source.domain}`, { cache: 'no-store' });
        if (!fees.ok) throw new Error(`fee table unavailable (${fees.status})`);
        setPlan(burnFor(priceAtomic, fastFeeBps(await fees.json())));
      } catch (e) {
        setError(`Could not prepare the transfer: ${(e as Error).message}`);
      }
    })();
  }, [env, source, session, vaultJobId, priceAtomic]);

  /** Wait for Circle, then mint and fund in one Arc user operation. */
  const finish = useCallback(
    async (p: Pending) => {
      if (!env || polling.current) return;
      polling.current = true;
      setBusy(true);
      setError(null);
      try {
        for (;;) {
          const res = await fetch(`/api/cctp/message?source=${p.domain}&tx=${p.tx}`, { cache: 'no-store' });
          const att = readAttestation(res.ok ? await res.json() : null, {
            arcDomain: env.arcDomain,
            recipient: session.address,
          });
          if (att.ready) {
            const calls: WalletCall[] = [];
            const alreadyMinted = await session.nonceUsed(env.messageTransmitter, att.nonce);
            let plan = finishPlan({
              alreadyMinted,
              amount: att.amount,
              feeExecuted: att.feeExecuted,
              maxFee: att.maxFee,
              arcBalance: await session.balanceOf(chain.usdc),
              price: priceAtomic,
            });
            if (plan.mint) calls.push(receiveCall(env, att.message, att.attestation));
            if (plan.pay === 'after-mint') {
              // Circle reported no fee: mint alone, then decide on what actually landed.
              setStatus('Receiving your USDC on Arc…');
              await session.send(calls);
              calls.length = 0;
              plan = finishPlan({
                alreadyMinted: true,
                amount: att.amount,
                feeExecuted: att.feeExecuted,
                maxFee: att.maxFee,
                arcBalance: await session.balanceOf(chain.usdc),
                price: priceAtomic,
              });
            }
            if (plan.pay === 'short') {
              if (calls.length > 0) await session.send(calls);
              savePending(vaultJobId, null);
              setPending(null);
              setStatus(null);
              setError('Your USDC arrived on Arc, but it is less than the price, so nothing was paid. Add the difference and pay again.');
              return;
            }
            calls.push(
              ...fundCalls({ usdc: chain.usdc, jobVault: chain.jobVault, jobId: vaultJobId, amountAtomic: priceAtomic.toString() }),
            );
            setStatus('Paying into escrow…');
            const tx = await session.send(calls);
            savePending(vaultJobId, null);
            setPending(null);
            setStatus(null);
            await onFunded(tx);
            return;
          }
          setStatus(att.reason);
          await new Promise((r) => setTimeout(r, 5_000));
        }
      } catch (e) {
        setError((e as Error).message);
      } finally {
        polling.current = false;
        setBusy(false);
      }
    },
    [env, session, chain, vaultJobId, priceAtomic, onFunded],
  );

  const start = useCallback(async () => {
    if (!env || !source || !src || !plan || starting.current || polling.current) return;
    starting.current = true;
    setBusy(true);
    setError(null);
    try {
      setStatus(`Moving USDC from ${source.label}…`);
      if (!navigator.locks) throw new Error("Safe transfer locking is unavailable. Nothing was submitted.");
      const p = await navigator.locks.request(storeKey(vaultJobId), () =>
        submitBurnOnce(window.localStorage, storeKey(vaultJobId), source.domain, () =>
          src.send(burnCalls({ env, source, arcRecipient: session.address, burn: plan.burn, maxFee: plan.maxFee })),
        ),
      );
      setPending(p);
      setBusy(false);
      await finish(p);
    } catch (e) {
      setError((e as Error).message);
      setStatus(null);
      setBusy(false);
    } finally {
      starting.current = false;
    }
  }, [env, source, src, plan, session, vaultJobId, finish]);

  if (!env || !source) return null;

  const shortfall = priceAtomic > arcBalance ? priceAtomic - arcBalance : 0n;
  const enough = plan && srcBalance !== null && srcBalance >= plan.burn;

  return (
    <div className="portal-bridge">
      {pending ? (
        <>
          <p className="portal-body">
            You started moving USDC from {source.label}. It is safe; finish the transfer to pay for this job.
          </p>
          <button type="button" className="portal-accept" disabled={busy} aria-busy={busy || undefined} onClick={() => void finish(pending)}>
            {busy ? 'Finishing…' : 'Finish moving your USDC and pay'}
          </button>
        </>
      ) : (
        <>
          <p className="portal-body">
            Your wallet on Arc holds {formatUsdcExact(arcBalance.toString())} USDC; this job costs{' '}
            {formatUsdcExact(priceAtomic.toString())}. You can bring USDC from {source.label} with the same passkey.
          </p>
          {src ? (
            <p className="portal-proof">
              Your {source.label} wallet <code>{src.address}</code> holds{' '}
              {srcBalance === null ? '…' : `${formatUsdcExact(srcBalance.toString())} USDC`}.
            </p>
          ) : null}
          {plan ? (
            <p className="portal-proof">
              Moving {formatUsdcExact(plan.burn.toString())} USDC covers the price and Circle’s transfer fee (at most{' '}
              {formatUsdcExact(plan.maxFee.toString())}). Anything left over stays in your wallet.
            </p>
          ) : null}
          <button
            type="button"
            className="portal-accept"
            disabled={busy || !enough}
            aria-busy={busy || undefined}
            onClick={() => void start()}
          >
            {busy
              ? 'Working…'
              : enough
                ? `Bring USDC from ${source.label} and pay`
                : `Needs ${plan ? formatUsdcExact(plan.burn.toString()) : '…'} USDC on ${source.label}`}
          </button>
          {shortfall > 0n && !enough && srcBalance !== null ? (
            <p className="portal-proof">Send USDC to your {source.label} wallet address above, then refresh.</p>
          ) : null}
        </>
      )}
      <div aria-live="polite" role="status">
        {status ? <p className="portal-note is-sentence">{status}</p> : null}
        {error ? <p className="portal-note is-sentence is-error">{error}</p> : null}
      </div>
    </div>
  );
}
