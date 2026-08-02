import { formatUsdc } from './format';
import type { FinancialEvent, StreamEvent, StreamMessage } from './types';

export type ActivityFilter = 'all' | 'work' | 'money' | 'approvals';
export type ActivityTone = 'brain' | 'worker' | 'funding' | 'owner' | 'qa';

export interface ApprovalMoment {
  requestId: string;
  intentHash: string;
}

/** Settlement split as the chain reports it, when the event carries both legs.
 *  JobSettled is the only event that moves money two ways at once, so the waterfall
 *  visual (F1) reads the exact figures instead of inferring them. */
export interface SettlementSplit {
  advanceRepaidUsdc: string;
  operatorNetUsdc: string;
}

export interface ActivityMessage {
  id: string;
  actor: string;
  role: string;
  initials: string;
  tone: ActivityTone;
  text: string;
  at: string;
  kind: string;
  jobId?: string;
  /** Daemon events: the bytes32 vault job id this event's job maps to on chain, when it has one. */
  chainRef?: string;
  amountUsdc?: string;
  explorerUrl?: string;
  filter: Exclude<ActivityFilter, 'all'>;
  approval?: ApprovalMoment;
  /**
   * Lowercased state of an `approval.requested` event, e.g. 'pending' or 'approved'.
   *
   * The stream normalizes BOTH a pending escalation and an already-approved purchase to the same
   * `approval.requested` kind, so the kind alone cannot tell "someone is being asked" from
   * "money left the treasury". Consumers that care about the difference need this. The `approval`
   * field above is not a substitute: it is absent for every non-pending state AND for every
   * non-approval event, so its absence does not mean "approved".
   */
  approvalState?: string;
  /** Stable request id joining rejection/request-alternative to its replacement. */
  threadKey?: string;
  /** Present on settlement events that report both waterfall legs. */
  settlement?: SettlementSplit;
  /**
   * True when this message comes from the local scripted demo replay. Its `explorerUrl` (if
   * any) points at a fabricated tx hash, so the UI must render it as a visible placeholder,
   * never as a live "verify on explorer" link. A fake verification link is worse than a
   * labelled fake event: the event is captioned, the link masquerades as the real thing.
   */
  placeholder?: boolean;
}

type Dict = Record<string, unknown>;

function record(value: unknown): Dict {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? (value as Dict) : {};
}

function pickString(value: unknown, ...keys: string[]): string {
  const obj = record(value);
  for (const key of keys) {
    const candidate = obj[key];
    if (typeof candidate === 'string' && candidate.trim()) return candidate.trim();
    if (typeof candidate === 'number' && Number.isFinite(candidate)) return String(candidate);
  }
  return '';
}

function nestedPayload(payload: unknown): Dict {
  const outer = record(payload);
  return record(outer.payload ?? outer.Payload);
}

function actorFor(event: StreamEvent): Pick<ActivityMessage, 'actor' | 'role' | 'initials' | 'tone'> {
  const actor = (event.actor ?? '').toLowerCase();
  const kind = event.kind.toLowerCase();

  if (kind.includes('qa_verdict') || actor.includes('quality') || actor.endsWith(':qa')) {
    return { actor: 'QA Worker', role: 'Quality assurance', initials: 'QA', tone: 'qa' };
  }
  if (kind.startsWith('approval.') && kind !== 'approval.requested') {
    return { actor: 'Owner', role: 'Human decision', initials: 'OW', tone: 'owner' };
  }
  if (actor.startsWith('worker:')) {
    const worker = actor.slice('worker:'.length);
    const name = worker
      .split(/[-_]/)
      .filter(Boolean)
      .map((part) => part[0]?.toUpperCase() + part.slice(1))
      .join(' ');
    return {
      actor: `${name || 'Task'} Worker`,
      role: 'Specialist worker',
      initials: (name || 'Worker').split(' ').map((part) => part[0]).join('').slice(0, 2).toUpperCase(),
      tone: 'worker',
    };
  }
  if (
    kind.startsWith('payment.') ||
    kind.startsWith('purchase.') ||
    [
      'jobfunded',
      'advanceissued',
      'expenserecorded',
      'deliverysubmitted',
      'jobsettled',
      'advancerepaid',
      'advancewrittenoff',
      'ratechanged',
    ].includes(kind)
  ) {
    return { actor: 'Funding', role: 'Treasury agent', initials: 'FN', tone: 'funding' };
  }
  if (actor === 'owner' || kind === 'brain.msg.owner.request' || kind === 'brain.msg.owner.confirm' || kind === 'brain.msg.owner.reject') {
    return { actor: 'Owner', role: 'Human operator', initials: 'OW', tone: 'owner' };
  }
  return { actor: 'Brain', role: 'Orchestrator', initials: 'BR', tone: 'brain' };
}

/**
 * Both waterfall legs, when the settlement event reports them.
 *
 * BOTH are required: a split with one leg missing would satisfy the type while handing the
 * consumer an empty string where an amount belongs, and the money graph would then treat a
 * partial report as authoritative. A single leg is not a split (review: PR #40).
 */
function settlementSplit(payload: unknown): SettlementSplit | undefined {
  const outer = record(payload);
  const repaid = pickString(outer, 'advanceRepaidAtomic', 'advance_repaid_atomic', 'advanceRepaidUsdc');
  const net = pickString(outer, 'operatorNetAtomic', 'operator_net_atomic', 'operatorNetUsdc');
  if (!repaid || !net) return undefined;
  return { advanceRepaidUsdc: repaid, operatorNetUsdc: net };
}

function eventAmount(payload: unknown): string {
  const outer = record(payload);
  const intent = record(outer.intent ?? outer.Intent);
  return (
    pickString(outer, 'amountUsdc', 'amountAtomic', 'amount_atomic', 'amount') ||
    pickString(outer, 'advanceRepaidAtomic', 'advance_repaid_atomic') ||
    pickString(intent, 'AmountMicros', 'amountMicros', 'amount_usdc', 'amountUsdc')
  );
}

function approvalRequest(event: StreamEvent): {
  requestId: string;
  intentHash: string;
  merchant: string;
  resource: string;
  purpose: string;
  amount: string;
  alternativeTo: string;
  state: string;
} {
  const payload = record(event.payload);
  const intent = record(payload.intent ?? payload.Intent);
  return {
    requestId: pickString(payload, 'request_id', 'requestId'),
    intentHash: pickString(payload, 'intent_hash', 'intentHash'),
    state: pickString(payload, 'state'),
    merchant: pickString(intent, 'Merchant', 'merchant'),
    resource: pickString(intent, 'Resource', 'resource'),
    purpose: pickString(intent, 'Purpose', 'purpose'),
    amount: pickString(intent, 'AmountMicros', 'amountMicros', 'amountUsdc'),
    alternativeTo: pickString(intent, 'AlternativeTo', 'alternativeTo'),
  };
}

function describeBrainMessage(event: StreamEvent): string {
  const payload = record(event.payload);
  const detail = nestedPayload(event.payload);
  const type = event.kind.replace(/^brain\.msg\./, '');
  const note =
    pickString(detail, 'message', 'status', 'stage', 'summary', 'report', 'escalation', 'reason') ||
    pickString(payload, 'message', 'status', 'stage', 'summary', 'report', 'escalation', 'reason');

  switch (type) {
    case 'owner.request':
      return note || 'I received a new job request.';
    case 'owner.confirm':
      return 'I confirmed the proposed scope. The team can begin.';
    case 'owner.reject':
      return note ? `I rejected the proposed scope: ${note}` : 'I rejected the proposed scope.';
    case 'brain.scope_proposal': {
      const scope = pickString(detail, 'scope') || pickString(payload, 'scope');
      const quote = pickString(detail, 'quote_usdc', 'quoteUsdc') || pickString(payload, 'quote_usdc', 'quoteUsdc');
      return `I prepared the job scope${scope ? `: ${scope}` : ''}${quote ? ` for ${formatUsdc(quote)} USDC` : ''}.`;
    }
    case 'brain.assignment': {
      const worker = pickString(detail, 'worker_kind', 'workerKind') || pickString(payload, 'worker_kind', 'workerKind');
      return `I assigned this job${worker ? ` to the ${worker.replace(/[-_]/g, ' ')} worker` : ' to a specialist worker'}.`;
    }
    case 'worker.progress':
      return note || 'Work is progressing on the assigned task.';
    case 'worker.report':
      return note || 'I finished the assigned work and sent the draft to Brain.';
    case 'worker.failure':
      return note ? `I could not complete the task: ${note}` : 'I could not complete the assigned task.';
    case 'worker.qa_verdict': {
      const passed = detail.passed ?? payload.passed;
      const reasons = (Array.isArray(detail.reasons) ? detail.reasons : payload.reasons) as unknown;
      if (passed === true) return 'Review complete. The evidence is ready for delivery.';
      if (Array.isArray(reasons) && reasons.length) return `I sent the draft back: ${reasons.map(String).join('; ')}.`;
      return 'I found an unsupported claim and sent the draft back for revision.';
    }
    case 'brain.job_report':
      return 'The final report passed review and is ready for the owner.';
    case 'brain.job_update':
      return note || 'I updated the job status.';
    default:
      return note || `I recorded ${type.replace(/[._]/g, ' ')}.`;
  }
}

function humanText(event: StreamEvent): {
  text: string;
  filter: Exclude<ActivityFilter, 'all'>;
  amount?: string;
  approval?: ApprovalMoment;
  approvalState?: string;
  threadKey?: string;
} {
  const payload = record(event.payload);
  const amount = eventAmount(payload);
  const reason = pickString(payload, 'reason', 'error');
  const requestId = pickString(payload, 'request_id', 'requestId');

  if (event.kind === 'approval.requested') {
    const req = approvalRequest(event);
    const subject = req.purpose || req.resource || 'this purchase';
    const seller = req.merchant ? ` from ${req.merchant}` : '';
    const price = req.amount ? ` for ${formatUsdc(req.amount)} USDC` : '';
    if (req.alternativeTo) {
      return {
        text: `I found a replacement for the rejected option: ${subject}${seller}${price}.`,
        filter: 'approvals',
        amount: req.amount,
        approval: req.state.toLowerCase() === 'pending' ? { requestId: req.requestId, intentHash: req.intentHash } : undefined,
        approvalState: req.state.toLowerCase(),
        threadKey: req.alternativeTo,
      };
    }
    const pending = req.state.toLowerCase() === 'pending';
    return {
      text: pending
        ? `I need your approval to buy ${subject}${seller}${price}.`
        : `Policy cleared ${subject}${seller}${price}.`,
      filter: 'approvals',
      amount: req.amount,
      approval: pending ? { requestId: req.requestId, intentHash: req.intentHash } : undefined,
      approvalState: req.state.toLowerCase(),
      threadKey: req.requestId,
    };
  }

  switch (event.kind) {
    case 'approval.approve':
      return { text: `I approved this request${reason ? `: ${reason}` : '.'}`, filter: 'approvals', threadKey: requestId };
    case 'approval.reject':
      return { text: `I rejected this request${reason ? `: ${reason}` : '.'}`, filter: 'approvals', threadKey: requestId };
    case 'approval.request_alternative':
      return {
        text: `I asked the team to find a cheaper alternative${reason ? `: ${reason}` : '.'}`,
        filter: 'approvals',
        threadKey: requestId,
      };
    case 'approval.expired':
      return { text: 'This approval expired before a decision was made.', filter: 'approvals', threadKey: requestId };
    case 'policy.evaluated': {
      const outcome = pickString(payload, 'outcome');
      return {
        text: outcome ? `Policy evaluated the purchase as ${outcome.replace(/_/g, ' ')}${reason ? `: ${reason}` : '.'}` : 'Policy evaluated the purchase.',
        filter: 'approvals',
      };
    }
    case 'payment.executing':
      return { text: 'The approved payment is being executed.', filter: 'money', amount, threadKey: requestId };
    case 'payment.executed':
      return { text: `Payment completed${amount ? ` for ${formatUsdc(amount)} USDC` : ''}.`, filter: 'money', amount, threadKey: requestId };
    case 'payment.failed':
      return { text: `Payment failed${reason ? `: ${reason}` : '.'}`, filter: 'money', amount, threadKey: requestId };
    case 'purchase.pending_settlement':
      return { text: 'The purchase succeeded and is waiting for its on-chain expense record.', filter: 'money', amount };
    case 'task.withheld':
      return { text: `I withheld this task${reason ? `: ${reason}` : '.'}`, filter: 'work' };
    case 'freeze.engaged':
      return { text: `I paused new work and payments${reason ? `: ${reason}` : '.'}`, filter: 'work' };
    case 'freeze.lifted':
      return { text: 'The freeze was lifted. Work can resume.', filter: 'work' };
  }

  if (event.kind.startsWith('brain.msg.')) {
    return { text: describeBrainMessage(event), filter: 'work', amount };
  }

  const chain: Record<string, string> = {
    JobFunded: 'The customer funded the job escrow.',
    AdvanceIssued: 'Working capital arrived before the job was paid out.',
    ExpenseRecorded: 'The purchase receipt was recorded on-chain.',
    DeliverySubmitted: 'The delivery proof was submitted on-chain.',
    JobSettled: 'The job settled and the waterfall distributed the proceeds.',
    AdvanceRepaid: 'The working-capital advance was repaid first.',
    AdvanceWrittenOff: 'The unrecovered advance was written off.',
    RateChanged: 'The organization’s advance rate changed based on its record.',
  };
  if (chain[event.kind]) return { text: chain[event.kind], filter: 'money', amount };

  return {
    text: `Recorded ${event.kind.replace(/[._]/g, ' ')}.`,
    filter: event.kind.toLowerCase().includes('approval') ? 'approvals' : 'work',
    amount,
  };
}

export function humanizeStreamEvent(message: Extract<StreamMessage, { kind: 'event' }>): ActivityMessage {
  const event = message.event;
  const description = humanText(event);
  const { amount, ...copy } = description;
  return {
    id: `${message.source}:${message.seq}`,
    ...actorFor(event),
    ...copy,
    amountUsdc: amount,
    at: event.at,
    kind: event.kind,
    jobId: event.jobId || (event.kind === 'RateChanged' ? undefined : event.entityId),
    // Carried through so a consumer can join a daemon event to its on-chain job without
    // having to know the business id. Only the daemon holds that pair.
    chainRef: event.chainRef,
    explorerUrl: pickString(event.payload, 'explorerUrl', 'explorer_url'),
    settlement: settlementSplit(event.payload),
    placeholder: message.demo === true,
  };
}

/** Compatibility for the local V5 snapshot while the daemon snapshot has no history.
 *  `demo` marks the seed events as scripted so their explorer links render as placeholders. */
export function humanizeLegacyEvent(event: FinancialEvent, demo = false): ActivityMessage {
  const tone: ActivityTone =
    event.category === 'Approval' ? 'owner' : event.category === 'Finance' || event.category === 'Float' ? 'funding' : 'brain';
  const identities = {
    owner: { actor: 'Owner', role: 'Human decision', initials: 'OW' },
    funding: { actor: 'Funding', role: 'Treasury agent', initials: 'FN' },
    brain: { actor: 'Brain', role: 'Orchestrator', initials: 'BR' },
    worker: { actor: 'Worker', role: 'Specialist worker', initials: 'WK' },
    qa: { actor: 'QA Worker', role: 'Quality assurance', initials: 'QA' },
  } as const;
  return {
    id: `legacy:${event.seq}`,
    ...identities[tone],
    tone,
    text: event.summary,
    at: event.ts,
    kind: event.type,
    jobId: event.jobId,
    amountUsdc: event.amountUsdc,
    explorerUrl: event.explorerUrl,
    filter: event.category === 'Approval' ? 'approvals' : event.category === 'Finance' || event.category === 'Float' ? 'money' : 'work',
    placeholder: demo,
  };
}
