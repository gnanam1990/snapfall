import assert from 'node:assert/strict';
import test from 'node:test';
import { GET } from '../app/api/events/stream/route';

test('public disconnected stream ends without waiting for a client abort', { timeout: 1000 }, async () => {
  const previous = process.env.SNAPFALL_OWNER_API_URL;
  delete process.env.SNAPFALL_OWNER_API_URL;
  try {
    const response = await GET(new Request('http://localhost/api/events/stream'));
    const body = await response.text();
    assert.equal(response.status, 200);
    assert.match(response.headers.get('content-type') ?? '', /text\/event-stream/);
    assert.match(body, /^retry: 60000\n/);
    const data = body.split('\n').find(line => line.startsWith('data: '));
    const frame = JSON.parse(data!.slice(6));
    assert.equal(frame.snapshot.daemonConnected, false);
    assert.deepEqual(frame.snapshot.activeJobs, []);
    assert.equal(frame.snapshot.treasuryUsdc, null);
  } finally {
    if (previous === undefined) delete process.env.SNAPFALL_OWNER_API_URL;
    else process.env.SNAPFALL_OWNER_API_URL = previous;
  }
});
