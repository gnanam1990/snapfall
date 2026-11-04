/**
 * The customer's own wallet: what it may do to one job, and the exact calls it sends.
 *
 * The demo settles through a daemon-held customer key (SC-JV-005 is satisfied by whoever holds
 * the customer address, and for the demo that is the daemon, stated openly). A real customer
 * holds their own key. Here that key is a passkey on their device, behind a Circle modular
 * smart account (lib/passkeyWallet.ts), so the operator never holds it and the customer needs no
 * extension, seed phrase or gas token: Circle's paymaster sponsors the user operation.
 *
 * This file is the pure half: no network, no browser, so the gating and the calldata are
 * tested directly (customerWallet.test.ts). The rules it encodes are the contract's own:
 *
 *   fund(jobId)            msg.sender == customer, status Created, pulls exactly
 *                          customerPayment via transferFrom, so it is preceded by an approve
 *                          for exactly that amount (never an unlimited allowance)
 *   acceptDelivery(jobId)  msg.sender == customer, status Delivered
 *
 * The gate refuses rather than lets the chain revert: a sponsored user operation that is
 * certain to revert still spends the sponsor's gas and shows the customer a failure for
 * something the page could have told them up front.
 */

import { encodeFunctionData, getAddress, isAddress, parseAbi, type Address, type Hex } from 'viem';

const JOB_ID_RE = /^0x[0-9a-fA-F]{64}$/;
const ATOMIC_RE = /^[0-9]+$/;

const ERC20 = parseAbi(['function approve(address spender, uint256 amount) returns (bool)']);
const JOB_VAULT = parseAbi(['function fund(bytes32 jobId)', 'function acceptDelivery(bytes32 jobId)']);

export interface WalletCall {
  to: Address;
  data: Hex;
}

/** The slice of a JobSnapshot (lib/jobChain.ts) the gate reads. */
export interface JobView {
  exists: boolean;
  status: string | null;
  customer: string | null;
  /** Atomic USDC (6dp ERC-20 surface), as a decimal string. */
  customerPaymentUsdc: string | null;
}

export type WalletAction =
  | { kind: 'fund'; amountAtomic: string }
  | { kind: 'accept' }
  | { kind: 'not-customer'; customer: string }
  | { kind: 'nothing'; reason: string };

/** What `wallet` may do to `job` right now. Addresses compare checksum-insensitively. */
export function walletAction(job: JobView, wallet: string): WalletAction {
  if (!job.exists) return { kind: 'nothing', reason: 'This job is not on chain yet.' };
  if (!job.customer || !isAddress(job.customer) || !isAddress(wallet)) {
    return { kind: 'nothing', reason: 'The job’s customer could not be read.' };
  }
  if (getAddress(job.customer) !== getAddress(wallet)) {
    return { kind: 'not-customer', customer: getAddress(job.customer) };
  }
  switch (job.status) {
    case 'Created':
      if (!job.customerPaymentUsdc || !ATOMIC_RE.test(job.customerPaymentUsdc) || BigInt(job.customerPaymentUsdc) === 0n) {
        return { kind: 'nothing', reason: 'The job’s price could not be read.' };
      }
      return { kind: 'fund', amountAtomic: job.customerPaymentUsdc };
    case 'Delivered':
      return { kind: 'accept' };
    case 'Funded':
    case 'InProgress':
      return { kind: 'nothing', reason: 'Your payment is in escrow. The work is under way.' };
    case 'Accepted':
      return { kind: 'nothing', reason: 'This job is accepted and settled.' };
    case 'Refunded':
    case 'Cancelled':
      return { kind: 'nothing', reason: `This job was ${job.status.toLowerCase()}.` };
    default:
      return { kind: 'nothing', reason: 'The job’s status could not be read.' };
  }
}

function jobId32(id: string): Hex {
  if (!JOB_ID_RE.test(id)) throw new Error(`not a bytes32 job id: ${id}`);
  return id as Hex;
}

/** approve(JobVault, exactly the price) then fund(jobId), as ONE user operation. */
export function fundCalls(p: { usdc: string; jobVault: string; jobId: string; amountAtomic: string }): WalletCall[] {
  if (!ATOMIC_RE.test(p.amountAtomic) || BigInt(p.amountAtomic) === 0n) {
    throw new Error(`not a positive atomic amount: ${p.amountAtomic}`);
  }
  const jobVault = getAddress(p.jobVault);
  return [
    {
      to: getAddress(p.usdc),
      data: encodeFunctionData({ abi: ERC20, functionName: 'approve', args: [jobVault, BigInt(p.amountAtomic)] }),
    },
    { to: jobVault, data: encodeFunctionData({ abi: JOB_VAULT, functionName: 'fund', args: [jobId32(p.jobId)] }) },
  ];
}

export function acceptCalls(p: { jobVault: string; jobId: string }): WalletCall[] {
  return [
    {
      to: getAddress(p.jobVault),
      data: encodeFunctionData({ abi: JOB_VAULT, functionName: 'acceptDelivery', args: [jobId32(p.jobId)] }),
    },
  ];
}
