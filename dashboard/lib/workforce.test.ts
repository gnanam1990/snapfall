import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import test from 'node:test';

import { GET } from '../app/api/workforce/route';
import { POST } from '../app/api/workforce/[id]/hire/route';
import {
  BUILD_MONITOR_MANIFEST,
  CATALOG_MANIFESTS,
  COMING_SOON_WORKERS,
  COMPLIANCE_SCOUT_MANIFEST,
  INCIDENT_WATCH_MANIFEST,
  RELEASE_SCRIBE_MANIFEST,
  activationLabel,
  validHireInput,
} from './workforce';

test('build-monitor catalog projection keeps the bounded permissions visible', () => {
  assert.equal(BUILD_MONITOR_MANIFEST.id, 'build-monitor');
  assert.deepEqual(BUILD_MONITOR_MANIFEST.permissions, ['Read-only repo', 'No payments', 'No shell']);
  assert.equal(BUILD_MONITOR_MANIFEST.checklistPath, '.snapfall/milestone.json');
});

test('release-scribe is a real registered manifest, not a coming-soon stub', () => {
  // It moved out of COMING_SOON_WORKERS into the real catalogue when the worker landed.
  assert.equal(RELEASE_SCRIBE_MANIFEST.id, 'release-scribe');
  assert.deepEqual(RELEASE_SCRIBE_MANIFEST.permissions, ['Read-only repo', 'No payments', 'No shell']);
  // Read-only worker: no checklistPath (that is Build Monitor's milestone input, not a range).
  assert.equal(RELEASE_SCRIBE_MANIFEST.checklistPath, undefined);
  assert.ok(CATALOG_MANIFESTS.some((m) => m.id === 'release-scribe'), 'must be in the committed fallback catalogue');
  // String() widens the literal id union so this stays a real runtime check rather than a
  // type error — the union no longer containing 'release-scribe' is itself proof of the move.
  assert.ok(
    !COMING_SOON_WORKERS.map((w) => String(w.id)).includes('release-scribe'),
    'release-scribe must no longer be advertised as coming soon',
  );
});

test('compliance-scout is a real registered manifest, not a coming-soon stub', () => {
  assert.equal(COMPLIANCE_SCOUT_MANIFEST.id, 'compliance-scout');
  assert.deepEqual(COMPLIANCE_SCOUT_MANIFEST.permissions, ['Read-only repo', 'No payments', 'No shell']);
  assert.equal(COMPLIANCE_SCOUT_MANIFEST.checklistPath, undefined);
  assert.ok(CATALOG_MANIFESTS.some((m) => m.id === 'compliance-scout'), 'must be in the committed fallback catalogue');
  assert.ok(
    !COMING_SOON_WORKERS.map((w) => String(w.id)).includes('compliance-scout'),
    'compliance-scout must no longer be advertised as coming soon',
  );
});

test('incident-watch is a real registered manifest, not a coming-soon stub', () => {
  assert.equal(INCIDENT_WATCH_MANIFEST.id, 'incident-watch');
  assert.deepEqual(INCIDENT_WATCH_MANIFEST.permissions, ['Read-only stream', 'No payments', 'No shell']);
  assert.equal(INCIDENT_WATCH_MANIFEST.checklistPath, undefined);
  // The description says what it does — liveness + recorded incidents — not "significant incidents".
  assert.match(INCIDENT_WATCH_MANIFEST.description, /recorded incidents/);
  assert.ok(CATALOG_MANIFESTS.some((m) => m.id === 'incident-watch'), 'must be in the committed fallback catalogue');
});

test('nothing is pending — all three former Coming-soon workers have shipped', () => {
  assert.equal(COMING_SOON_WORKERS.length, 0);
  for (const id of ['release-scribe', 'compliance-scout', 'incident-watch']) {
    assert.ok(CATALOG_MANIFESTS.some((m) => m.id === id), `${id} must be a real registered manifest`);
  }
});

// The bug this fixes: the no-daemon route hardcoded [build-monitor], overwriting the page's
// four-worker catalogue, so the public deploy showed one worker instead of four. The route must
// serve the SAME committed catalogue the page initialises from.
test('the no-daemon route serves the full committed catalogue, not just build-monitor', async () => {
  const prev = process.env.SNAPFALL_OWNER_API_URL;
  delete process.env.SNAPFALL_OWNER_API_URL; // force the local-catalogue branch (public deploy)
  try {
    const res = await GET();
    const body = (await res.json()) as { manifests: Array<{ id: string }>; source?: string };
    assert.equal(body.source, 'local-catalog');
    const ids = body.manifests.map((m) => m.id).sort();
    assert.deepEqual(ids, CATALOG_MANIFESTS.map((m) => m.id).sort(), 'route must serve the committed catalogue verbatim');
    for (const id of ['release-scribe', 'compliance-scout', 'incident-watch']) {
      assert.ok(ids.includes(id), `${id} must render on the public deploy with no daemon`);
    }
  } finally {
    if (prev !== undefined) process.env.SNAPFALL_OWNER_API_URL = prev;
  }
});

// Cross-language drift guard (the compliance-scout-class defect, applied to ourselves): the
// dashboard's committed catalogue and the daemon's WorkerCatalog are two definitions in two
// languages and WILL drift. This reads the Go source and fails when they disagree — the same idiom
// moneyGraphBeats.test.ts uses to pin the beat mapping to the daemon's real event kinds.
test('the dashboard catalogue matches the daemon WorkerCatalog (no cross-language drift)', () => {
  const daemonRoot = path.resolve(process.cwd(), '..', 'daemon');
  const mainGo = readFileSync(path.join(daemonRoot, 'cmd', 'snapfall', 'main.go'), 'utf8');

  // Bound the WorkerCatalog literal: from its assignment to the HireWorker assignment that follows.
  const start = mainGo.indexOf('api.WorkerCatalog = []ownerapi.WorkerManifest{');
  assert.ok(start >= 0, 'WorkerCatalog literal not found in main.go — the guard itself is broken');
  const end = mainGo.indexOf('api.HireWorker', start);
  assert.ok(end > start, 'could not bound the WorkerCatalog literal');
  const block = mainGo.slice(start, end);

  // Each entry names its id as `ID: worker.XxxKind`; resolve each constant to its string value.
  const kindConsts = [...block.matchAll(/ID:\s*worker\.(\w+)/g)].map((m) => m[1]);
  assert.ok(kindConsts.length > 0, 'no `ID: worker.XxxKind` entries parsed from WorkerCatalog');

  const workerDir = path.join(daemonRoot, 'internal', 'worker');
  const workerSrc = readdirSync(workerDir)
    .filter((f) => f.endsWith('.go') && !f.endsWith('_test.go'))
    .map((f) => readFileSync(path.join(workerDir, f), 'utf8'))
    .join('\n');
  const daemonIds = kindConsts
    .map((name) => {
      const m = workerSrc.match(new RegExp(`\\b${name}\\s*=\\s*"([^"]+)"`));
      assert.ok(m, `Kind constant ${name} has no string value in internal/worker`);
      return m![1];
    })
    .sort();

  assert.deepEqual(
    CATALOG_MANIFESTS.map((m) => m.id).sort(),
    daemonIds,
    'dashboard CATALOG_MANIFESTS and daemon WorkerCatalog have drifted — a worker exists in one catalogue but not the other. Update both.',
  );
});

test('hire input requires a repository and positive two-decimal quote', () => {
  assert.equal(validHireInput('/work/acme', '25.00'), true);
  assert.equal(validHireInput('', '25.00'), false);
  assert.equal(validHireInput('/work/acme', '0'), false);
  assert.equal(validHireInput('/work/acme', '2.345'), false);
});

test('hire proxy rejects cross-site simple requests before forwarding owner authority', async () => {
  const previousURL = process.env.SNAPFALL_OWNER_API_URL;
  const previousFetch = globalThis.fetch;
  process.env.SNAPFALL_OWNER_API_URL = 'http://127.0.0.1:4010/api/v1';
  let forwarded = 0;
  globalThis.fetch = async () => {
    forwarded += 1;
    return Response.json({ jobId: 'should-not-exist' }, { status: 201 });
  };
  try {
    const request = new Request('http://dashboard.local/api/workforce/build-monitor/hire', {
      method: 'POST',
      headers: {
        'content-type': 'text/plain',
        origin: 'https://attacker.example',
        'sec-fetch-site': 'cross-site',
      },
      body: JSON.stringify({ repository: '/work/acme', quoteUsdc: '25.00', by: 'anandan' }),
    });
    const response = await POST(request, { params: Promise.resolve({ id: 'build-monitor' }) });
    assert.equal(response.status, 403);
    assert.equal(forwarded, 0);
  } finally {
    globalThis.fetch = previousFetch;
    if (previousURL === undefined) delete process.env.SNAPFALL_OWNER_API_URL;
    else process.env.SNAPFALL_OWNER_API_URL = previousURL;
  }
});

test('hire proxy permits a same-origin JSON action', async () => {
  const previousURL = process.env.SNAPFALL_OWNER_API_URL;
  const previousFetch = globalThis.fetch;
  process.env.SNAPFALL_OWNER_API_URL = 'http://127.0.0.1:4010/api/v1';
  let forwarded = 0;
  let forwardedSignal: AbortSignal | null | undefined;
  globalThis.fetch = async (_input, init) => {
    forwarded += 1;
    forwardedSignal = init?.signal;
    return Response.json({ jobId: 'milestone_1', vaultJobId: '0xwatch', state: 'assigned' }, { status: 201 });
  };
  try {
    const request = new Request('http://dashboard.local/api/workforce/build-monitor/hire', {
      method: 'POST',
      headers: {
        'content-type': 'application/json',
        origin: 'http://dashboard.local',
      },
      body: JSON.stringify({ repository: '/work/acme', quoteUsdc: '25.00', by: 'anandan' }),
    });
    const response = await POST(request, { params: Promise.resolve({ id: 'build-monitor' }) });
    assert.equal(response.status, 201);
    assert.equal(forwarded, 1);
    assert.ok(forwardedSignal, 'privileged upstream request must carry a timeout/cancellation signal');
    assert.notEqual(forwardedSignal, request.signal, 'upstream signal must compose a bounded timeout');
  } finally {
    globalThis.fetch = previousFetch;
    if (previousURL === undefined) delete process.env.SNAPFALL_OWNER_API_URL;
    else process.env.SNAPFALL_OWNER_API_URL = previousURL;
  }
});

test('activation labels expose the real one-shot Build Monitor lifecycle', () => {
  assert.equal(activationLabel('assigned'), 'Check running');
  assert.equal(activationLabel('complete'), 'Check complete');
  assert.notEqual(activationLabel('complete'), 'Watching');
});

test('workforce gallery has no fixed-width track that could overflow, and collapses on narrow', () => {
  const css = readFileSync(new URL('../app/globals.css', import.meta.url), 'utf8');
  // The old four-across grid carried a minmax(390px, …) track for Build Monitor's hire form, which
  // overflowed once the three operator tools shared its row (144px columns padded to 491px). That
  // grid is gone: Build Monitor is now a full-width block, and the operator tools use only
  // minmax(0, 1fr) tracks — they shrink, never overflow. So the fixed-width track must not remain.
  assert.doesNotMatch(css, /minmax\(390px/, 'the fixed-width gallery track must be gone');
  const base = css.match(/\.operator-tools \{([\s\S]*?)\n\}/)?.[1] ?? '';
  assert.match(base, /grid-template-columns:\s*repeat\(3, minmax\(0, 1fr\)\)/);
  // …and it degrades to two then one column on narrow viewports rather than pushing the page wide.
  // These collapse rules are unique, so match them directly — the page has several same-width
  // @media blocks, and extracting "the" 560px block by first-match is fragile.
  assert.match(css, /\.operator-tools \{ grid-template-columns: repeat\(2, minmax\(0, 1fr\)\); \}/);
  assert.match(css, /\.operator-tools \{ grid-template-columns: 1fr; \}/);
});

test('workforce documentation uses the manifest display name consistently', () => {
  const readme = readFileSync(new URL('../README.md', import.meta.url), 'utf8');
  assert.doesNotMatch(readme, /Build-Monitor/);
});
