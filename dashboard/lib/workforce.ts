export interface WorkerManifest {
  id: string;
  name: string;
  category: string;
  description: string;
  permissions: string[];
  checklistPath?: string;
}

export interface HireWorkerResult {
  jobId: string;
  vaultJobId: string;
  state: 'scoped' | 'confirmed' | 'assigned' | 'complete' | 'failed' | 'rejected' | 'escalated';
}

export interface WorkerActivation extends HireWorkerResult {
  manifestId: string;
  repository: string;
  quoteUsdc: string;
}

export const BUILD_MONITOR_MANIFEST: WorkerManifest = {
  id: 'build-monitor',
  name: 'Build Monitor',
  category: 'Engineering operations',
  description: 'Watches committed repository milestones and reports completion evidence to Brain.',
  permissions: ['Read-only repo', 'No payments', 'No shell'],
  checklistPath: '.snapfall/milestone.json',
};

export const COMING_SOON_WORKERS = [
  {
    id: 'release-scribe',
    name: 'Release Scribe',
    category: 'Documentation',
    description: 'Produces release notes and change summaries from verified milestones.',
  },
  {
    id: 'compliance-scout',
    name: 'Compliance Scout',
    category: 'Security & compliance',
    description: 'Scans artifacts and configs for policy alignment and reports findings.',
  },
  {
    id: 'incident-watch',
    name: 'Incident Watch',
    category: 'Reliability',
    description: 'Monitors systems and alerts Brain on significant incidents with evidence.',
  },
] as const;

export function validHireInput(repository: string, quoteUsdc: string): boolean {
  if (!repository.trim()) return false;
  return /^(?:0|[1-9]\d*)(?:\.\d{1,2})?$/.test(quoteUsdc.trim()) && Number(quoteUsdc) > 0;
}

export function activationLabel(state: HireWorkerResult['state']): string {
  switch (state) {
    case 'scoped': return 'Awaiting confirmation';
    case 'confirmed':
    case 'assigned': return 'Check running';
    case 'complete': return 'Check complete';
    case 'failed': return 'Check failed';
    case 'rejected': return 'Activation rejected';
    case 'escalated': return 'Owner attention needed';
  }
}
