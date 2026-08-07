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

export const RELEASE_SCRIBE_MANIFEST: WorkerManifest = {
  id: 'release-scribe',
  name: 'Release Scribe',
  category: 'Documentation',
  description: 'Reads committed git history over a revision range and derives release notes for Brain.',
  permissions: ['Read-only repo', 'No payments', 'No shell'],
};

// The committed fallback catalogue, used when no daemon has answered /api/workforce (the public
// deploy). Keep in step with cmd/snapfall/main.go's WorkerCatalog: a manifest here that the
// daemon does not register would claim a worker that cannot be dispatched.
export const CATALOG_MANIFESTS: WorkerManifest[] = [BUILD_MONITOR_MANIFEST, RELEASE_SCRIBE_MANIFEST];

export const COMING_SOON_WORKERS = [
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
