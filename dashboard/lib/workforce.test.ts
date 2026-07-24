import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

import { POST } from '../app/api/workforce/[id]/hire/route';
import { BUILD_MONITOR_MANIFEST, activationLabel, validHireInput } from './workforce';

test('build-monitor catalog projection keeps the bounded permissions visible', () => {
  assert.equal(BUILD_MONITOR_MANIFEST.id, 'build-monitor');
  assert.deepEqual(BUILD_MONITOR_MANIFEST.permissions, ['Read-only repo', 'No payments', 'No shell']);
  assert.equal(BUILD_MONITOR_MANIFEST.checklistPath, '.snapfall/milestone.json');
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

test('workforce gallery collapses fixed-width tracks before the sidebar causes overflow', () => {
  const css = readFileSync(new URL('../app/globals.css', import.meta.url), 'utf8');
  const intermediate = css.match(/@media \(max-width: 1120px\) \{([\s\S]*?)\n\}/)?.[1] ?? '';
  assert.match(intermediate, /\.manifest-grid\s*\{\s*grid-template-columns:\s*repeat\(2, minmax\(0, 1fr\)\)/);
  assert.doesNotMatch(intermediate, /minmax\(390px/);
});

test('workforce documentation uses the manifest display name consistently', () => {
  const readme = readFileSync(new URL('../README.md', import.meta.url), 'utf8');
  assert.doesNotMatch(readme, /Build-Monitor/);
});
