import assert from 'node:assert/strict';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import path from 'node:path';
import test from 'node:test';
import { pathToFileURL } from 'node:url';

import {
  checkHost,
  checkSameOriginJson,
  ownerAuthHeaders,
  requireOwnerRequest,
  type OwnerGrant,
} from './ownerApi';
import {
  OWNER_SESSION_COOKIE,
  OWNER_SESSION_COOKIE_SECURE,
  OWNER_SESSION_TTL_MS,
  issueSessionValue,
  readCookie,
  readCookies,
  safeEqual,
  verifySessionValue,
} from './ownerSession';
import * as sessionRoute from '../app/api/owner/session/route';
import { GET as customerAcceptance } from '../app/api/customer/[jobId]/acceptance/route';

// Obvious fakes only. Neither is a real credential anywhere.
const FAKE_SECRET = 'fake-dashboard-owner-secret-for-tests-0000';
const FAKE_TOKEN = 'fake-daemon-owner-token-for-tests-00000000';
const DAEMON = 'http://127.0.0.1:4010/api/v1';
const ORIGIN = 'http://dashboard.local';

type Env = Record<string, string | undefined>;
const ENV_KEYS = [
  'SNAPFALL_OWNER_API_URL',
  'SNAPFALL_OWNER_TOKEN',
  'SNAPFALL_DASHBOARD_OWNER_SECRET',
  'SNAPFALL_DASHBOARD_HOSTS',
  'SNAPFALL_DASHBOARD_ORIGIN',
  'SNAPFALL_DASHBOARD_TRUST_PROXY',
  'NODE_ENV',
] as const;

/** Run fn with exactly these owner-related env values (others in ENV_KEYS unset), then restore. */
async function withEnv<T>(vars: Env, fn: () => Promise<T> | T): Promise<T> {
  const env = process.env as Env;
  const saved: Env = {};
  for (const k of ENV_KEYS) {
    saved[k] = env[k];
    delete env[k];
  }
  for (const [k, v] of Object.entries(vars)) if (v !== undefined) env[k] = v;
  try {
    return await fn();
  } finally {
    for (const k of ENV_KEYS) {
      if (saved[k] === undefined) delete env[k];
      else env[k] = saved[k];
    }
  }
}

const CONFIGURED: Env = {
  SNAPFALL_OWNER_API_URL: DAEMON,
  SNAPFALL_OWNER_TOKEN: FAKE_TOKEN,
  SNAPFALL_DASHBOARD_OWNER_SECRET: FAKE_SECRET,
};

/** Record every upstream call; answer with a benign stub. */
async function withFetchSpy<T>(fn: (calls: Array<{ url: string; init?: RequestInit }>) => Promise<T>): Promise<T> {
  const previous = globalThis.fetch;
  const calls: Array<{ url: string; init?: RequestInit }> = [];
  globalThis.fetch = (async (input: string | URL | Request, init?: RequestInit) => {
    calls.push({ url: String(input), init });
    if (String(input).endsWith('/events/stream')) {
      return new Response('data: {}\n\n', { headers: { 'content-type': 'text/event-stream' } });
    }
    return Response.json({ approvals: [], manifests: [], activations: [], acceptToken: 'act_FAKE' }, { status: 200 });
  }) as typeof fetch;
  try {
    return await fn(calls);
  } finally {
    globalThis.fetch = previous;
  }
}

function sessionCookieHeader(secret = FAKE_SECRET, now = Date.now()): string {
  return `theme=dark; ${OWNER_SESSION_COOKIE}=${issueSessionValue(secret, now).value}`;
}

function ownerRequest(
  method: string,
  opts: { cookie?: string; origin?: string | null; contentType?: string | null; fetchSite?: string; url?: string } = {},
): Request {
  const headers = new Headers();
  if (opts.cookie) headers.set('cookie', opts.cookie);
  const origin = opts.origin === undefined ? ORIGIN : opts.origin;
  if (origin) headers.set('origin', origin);
  const contentType = opts.contentType === undefined ? 'application/json' : opts.contentType;
  if (contentType && method !== 'GET') headers.set('content-type', contentType);
  if (opts.fetchSite) headers.set('sec-fetch-site', opts.fetchSite);
  return new Request(opts.url ?? `${ORIGIN}/api/owner-thing`, {
    method,
    headers,
    body: method === 'GET' ? undefined : '{}',
  });
}

async function errorCode(res: Response): Promise<string | undefined> {
  const body = (await res.clone().json().catch(() => null)) as { error?: { code?: string } } | null;
  return body?.error?.code;
}

// ── Session value ────────────────────────────────────────────────────────────────────────────

test('a freshly issued session verifies, and never contains the secret', () => {
  const { value, expiresAt } = issueSessionValue(FAKE_SECRET, 1_000_000);
  assert.equal(expiresAt, 1_000_000 + OWNER_SESSION_TTL_MS);
  assert.ok(!value.includes(FAKE_SECRET));
  assert.equal(verifySessionValue(FAKE_SECRET, value, 1_000_001), 'valid');
});

test('session verification rejects missing, malformed, tampered, foreign and expired values', () => {
  const now = 5_000_000;
  const { value } = issueSessionValue(FAKE_SECRET, now);
  assert.equal(verifySessionValue(FAKE_SECRET, null, now), 'missing');
  assert.equal(verifySessionValue(FAKE_SECRET, 'not-a-session', now), 'malformed');
  assert.equal(verifySessionValue(FAKE_SECRET, FAKE_SECRET, now), 'malformed');
  const [v, exp, nonce, mac] = value.split('.');
  // Extending the expiry breaks the MAC.
  assert.equal(verifySessionValue(FAKE_SECRET, `${v}.${Number(exp) + 1}.${nonce}.${mac}`, now), 'bad-signature');
  assert.equal(verifySessionValue(FAKE_SECRET, `${v}.${exp}.${nonce}.${mac.slice(0, -2)}AA`, now), 'bad-signature');
  // Signed under a different secret (e.g. after rotation).
  assert.equal(verifySessionValue('fake-rotated-secret-for-tests-000000000000', value, now), 'bad-signature');
  assert.equal(verifySessionValue(FAKE_SECRET, value, now + OWNER_SESSION_TTL_MS), 'expired');
});

test('cookie parsing and constant-time comparison', () => {
  assert.equal(readCookie('a=1; snapfall_owner_session=abc.def; b=2', OWNER_SESSION_COOKIE), 'abc.def');
  assert.equal(readCookie('xsnapfall_owner_session=nope', OWNER_SESSION_COOKIE), null);
  assert.equal(readCookie(null, OWNER_SESSION_COOKIE), null);
  assert.equal(safeEqual('same', 'same'), true);
  assert.equal(safeEqual('same', 'Same'), false);
  assert.equal(safeEqual('short', 'a much longer value'), false);
});

// ── The guard ────────────────────────────────────────────────────────────────────────────────

test('guard: no daemon configured is a 503, not an open door', async () => {
  await withEnv({ SNAPFALL_DASHBOARD_OWNER_SECRET: FAKE_SECRET }, async () => {
    const gate = requireOwnerRequest(ownerRequest('GET', { cookie: sessionCookieHeader() }));
    assert.equal(gate.ok, false);
    if (!gate.ok) {
      assert.equal(gate.response.status, 503);
      assert.equal(await errorCode(gate.response), 'NO_DAEMON');
    }
  });
});

test('guard: a daemon without a usable dashboard secret fails closed (503)', async () => {
  for (const secret of [undefined, '', 'too-short', FAKE_TOKEN]) {
    await withEnv({ ...CONFIGURED, SNAPFALL_DASHBOARD_OWNER_SECRET: secret }, async () => {
      // Even a cookie minted under that value must not get through.
      const cookie = secret ? sessionCookieHeader(secret) : undefined;
      const gate = requireOwnerRequest(ownerRequest('GET', { cookie }));
      assert.equal(gate.ok, false, `secret ${JSON.stringify(secret)} must not be accepted`);
      if (!gate.ok) {
        assert.equal(gate.response.status, 503);
        assert.equal(await errorCode(gate.response), 'OWNER_ACCESS_NOT_CONFIGURED');
      }
    });
  }
});

test('guard: no cookie, bad cookie and expired cookie are 401', async () => {
  await withEnv(CONFIGURED, async () => {
    const past = Date.now() - OWNER_SESSION_TTL_MS - 1000;
    const cases: Array<[string, string | undefined]> = [
      ['no cookie', undefined],
      ['garbage cookie', `${OWNER_SESSION_COOKIE}=garbage`],
      ['raw secret as cookie', `${OWNER_SESSION_COOKIE}=${FAKE_SECRET}`],
      ['cookie from another secret', sessionCookieHeader('fake-other-dashboard-secret-0000000000000')],
      ['expired cookie', sessionCookieHeader(FAKE_SECRET, past)],
    ];
    for (const [label, cookie] of cases) {
      for (const method of ['GET', 'POST']) {
        const gate = requireOwnerRequest(ownerRequest(method, { cookie }));
        assert.equal(gate.ok, false, `${label} (${method}) must be refused`);
        if (!gate.ok) {
          assert.equal(gate.response.status, 401, label);
          assert.equal(await errorCode(gate.response), 'OWNER_SIGN_IN_REQUIRED', label);
        }
      }
    }
  });
});

test('guard: a valid session is admitted for reads and same-origin JSON mutations', async () => {
  await withEnv(CONFIGURED, async () => {
    const cookie = sessionCookieHeader();
    const read = requireOwnerRequest(ownerRequest('GET', { cookie, origin: null }));
    assert.equal(read.ok, true);
    if (read.ok) {
      assert.equal(read.grant.base, DAEMON);
      assert.equal(ownerAuthHeaders(read.grant).get('authorization'), `Bearer ${FAKE_TOKEN}`);
    }
    const write = requireOwnerRequest(ownerRequest('POST', { cookie, fetchSite: 'same-origin' }));
    assert.equal(write.ok, true);
  });
});

test('guard: a signed-in mutation that is cross-origin or not JSON is refused', async () => {
  await withEnv(CONFIGURED, async () => {
    const cookie = sessionCookieHeader();
    const cases: Array<[string, Parameters<typeof ownerRequest>[1], number]> = [
      ['foreign Origin', { cookie, origin: 'https://attacker.example' }, 403],
      ['Sec-Fetch-Site cross-site', { cookie, fetchSite: 'cross-site' }, 403],
      ['Sec-Fetch-Site same-site', { cookie, fetchSite: 'same-site' }, 403],
      ['no Origin', { cookie, origin: null }, 403],
      ['null Origin', { cookie, origin: 'null' }, 403],
      ['text/plain', { cookie, contentType: 'text/plain' }, 415],
      ['form post', { cookie, contentType: 'application/x-www-form-urlencoded' }, 415],
      ['no content type', { cookie, contentType: null }, 415],
    ];
    for (const [label, opts, status] of cases) {
      for (const method of ['POST', 'PUT', 'DELETE']) {
        const gate = requireOwnerRequest(ownerRequest(method, opts));
        assert.equal(gate.ok, false, `${label} (${method}) must be refused`);
        if (!gate.ok) assert.equal(gate.response.status, status, `${label} (${method})`);
      }
    }
  });
});

test('guard: the optional Host allowlist refuses other hosts', async () => {
  await withEnv({ ...CONFIGURED, SNAPFALL_DASHBOARD_HOSTS: 'dash.example.com, localhost:3000' }, async () => {
    const cookie = sessionCookieHeader();
    const foreign = requireOwnerRequest(ownerRequest('GET', { cookie }));
    assert.equal(foreign.ok, false);
    if (!foreign.ok) assert.equal(foreign.response.status, 403);
    const allowed = requireOwnerRequest(
      ownerRequest('POST', { cookie, url: 'http://localhost:3000/api/x', origin: 'http://localhost:3000' }),
    );
    assert.equal(allowed.ok, true);
  });
});

test('ownerAuthHeaders refuses anything that is not a grant from the guard', async () => {
  await withEnv(CONFIGURED, () => {
    const forged = { base: DAEMON } as unknown as OwnerGrant;
    assert.throws(() => ownerAuthHeaders(forged), /requires a grant/);
  });
});

test('same-origin check alone is not authentication: forged headers without a cookie get 401', async () => {
  await withEnv(CONFIGURED, async () => {
    const req = ownerRequest('POST', { fetchSite: 'same-origin' });
    assert.equal(checkSameOriginJson(req), null, 'the forged request passes the CSRF check…');
    const gate = requireOwnerRequest(req);
    assert.equal(gate.ok, false, '…but not the guard');
    if (!gate.ok) assert.equal(gate.response.status, 401);
  });
});

// ── Sign-in route ────────────────────────────────────────────────────────────────────────────

function signIn(secret: unknown, opts: { origin?: string; contentType?: string } = {}): Promise<Response> {
  return sessionRoute.POST(
    new Request(`${ORIGIN}/api/owner/session`, {
      method: 'POST',
      headers: { 'content-type': opts.contentType ?? 'application/json', origin: opts.origin ?? ORIGIN },
      body: JSON.stringify({ secret }),
    }),
  );
}

test('sign-in: the right secret sets a hardened cookie holding a signed value, not the secret', async () => {
  await withEnv(CONFIGURED, async () => {
    const res = await signIn(FAKE_SECRET);
    assert.equal(res.status, 204);
    const cookie = res.headers.get('set-cookie') ?? '';
    assert.match(cookie, new RegExp(`^${OWNER_SESSION_COOKIE}=v1\\.`));
    assert.match(cookie, /; HttpOnly/);
    assert.match(cookie, /; SameSite=Strict/);
    assert.match(cookie, /; Path=\//);
    assert.doesNotMatch(cookie, /Secure/, 'development cookies work over plain http');
    assert.ok(!cookie.includes(FAKE_SECRET));

    const value = cookie.split(';')[0];
    const status = await sessionRoute.GET(new Request(`${ORIGIN}/api/owner/session`, { headers: { cookie: value } }));
    assert.deepEqual(await status.json(), { ownerApi: true, accessConfigured: true, signedIn: true });
    // …and the issued cookie opens the guard.
    assert.equal(requireOwnerRequest(ownerRequest('GET', { cookie: value })).ok, true);
  });
});

test('sign-in: production cookies are Secure', async () => {
  await withEnv({ ...CONFIGURED, NODE_ENV: 'production' }, async () => {
    const res = await signIn(FAKE_SECRET);
    assert.equal(res.status, 204);
    assert.match(res.headers.get('set-cookie') ?? '', /; Secure/);
  });
});

test('sign-in: wrong secret, cross-site, non-JSON and unconfigured attempts set no cookie', async () => {
  await withEnv(CONFIGURED, async () => {
    for (const [label, res, status] of [
      ['wrong secret', await signIn('fake-wrong-secret-000000000000000000000'), 401],
      ['non-string secret', await signIn(12345), 401],
      ['cross-site', await signIn(FAKE_SECRET, { origin: 'https://attacker.example' }), 403],
      ['text/plain', await signIn(FAKE_SECRET, { contentType: 'text/plain' }), 415],
    ] as Array<[string, Response, number]>) {
      assert.equal(res.status, status, label);
      assert.equal(res.headers.get('set-cookie'), null, label);
    }
  });
  await withEnv({ SNAPFALL_OWNER_API_URL: DAEMON }, async () => {
    const res = await signIn(FAKE_SECRET);
    assert.equal(res.status, 503);
    assert.equal(res.headers.get('set-cookie'), null);
  });
});

test('sign-out clears the cookie', async () => {
  await withEnv(CONFIGURED, async () => {
    const res = await sessionRoute.DELETE(
      new Request(`${ORIGIN}/api/owner/session`, {
        method: 'DELETE',
        headers: { 'content-type': 'application/json', origin: ORIGIN },
      }),
    );
    assert.equal(res.status, 204);
    assert.match(res.headers.get('set-cookie') ?? '', new RegExp(`^${OWNER_SESSION_COOKIE}=; .*Max-Age=0`));
  });
});

// ── Every owner route, through its real handler ──────────────────────────────────────────────

const API_ROOT = path.resolve(process.cwd(), 'app', 'api');

function routeFiles(dir: string = API_ROOT): string[] {
  return readdirSync(dir).flatMap((name) => {
    const full = path.join(dir, name);
    if (statSync(full).isDirectory()) return routeFiles(full);
    return name === 'route.ts' ? [full] : [];
  });
}

const rel = (file: string) => path.relative(API_ROOT, file).split(path.sep).join('/');

/** Routes that attach the owner token. Adding one means adding it here, on purpose. */
const EXPECTED_OWNER_ROUTES = [
  'approvals/[id]/decision/route.ts',
  'approvals/route.ts',
  'events/stream/route.ts',
  'jobs/[jobId]/accept-link/route.ts',
  'workforce/[id]/hire/route.ts',
  'workforce/route.ts',
];

test('every API route that can attach the owner token goes through requireOwnerRequest', () => {
  const files = routeFiles();
  assert.ok(files.length > 10, 'route enumeration found too few files — the test itself is broken');
  const ownerRoutes: string[] = [];
  for (const file of files) {
    const src = readFileSync(file, 'utf8');
    // Nothing reads the owner credentials or builds a bearer except lib/ownerApi.
    assert.doesNotMatch(src, /process\.env(\.|\[\s*['"])SNAPFALL_OWNER_(TOKEN|API_URL)/, `${rel(file)} reads owner env directly`);
    assert.doesNotMatch(src, /Bearer\s/, `${rel(file)} builds its own bearer header`);
    if (!/ownerAuthHeaders|ownerApiBase/.test(src)) continue;
    ownerRoutes.push(rel(file));
    assert.match(src, /requireOwnerRequest\(req\)/, `${rel(file)} uses owner helpers without the guard`);
    // Every owner-header construction uses the guard's grant.
    const calls = [...src.matchAll(/ownerAuthHeaders\(([^,)]*)/g)].map((m) => m[1].trim());
    assert.ok(calls.length > 0, `${rel(file)} imports owner helpers but never builds owner headers`);
    for (const arg of calls) assert.equal(arg, 'gate.grant', `${rel(file)} builds owner headers from ${arg}`);
  }
  assert.deepEqual(ownerRoutes.sort(), [...EXPECTED_OWNER_ROUTES].sort());
});

const METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE'] as const;
type Handler = (req: Request, ctx: { params: Promise<Record<string, string>> }) => Promise<Response>;

async function ownerHandlers(): Promise<Array<{ route: string; method: string; handler: Handler }>> {
  const out: Array<{ route: string; method: string; handler: Handler }> = [];
  for (const route of EXPECTED_OWNER_ROUTES) {
    const mod = (await import(pathToFileURL(path.join(API_ROOT, route)).href)) as Record<string, unknown>;
    for (const method of METHODS) {
      if (typeof mod[method] === 'function') out.push({ route, method, handler: mod[method] as Handler });
    }
  }
  return out;
}

const params = () => ({ params: Promise.resolve({ id: 'req-1', jobId: 'job-1' }) });

test('every owner handler refuses an unauthenticated caller and forwards nothing', async () => {
  const handlers = await ownerHandlers();
  assert.ok(handlers.length >= EXPECTED_OWNER_ROUTES.length);
  await withEnv(CONFIGURED, () =>
    withFetchSpy(async (calls) => {
      for (const { route, method, handler } of handlers) {
        // Forged browser headers, a well-formed body, no session.
        const res = await handler(ownerRequest(method, { fetchSite: 'same-origin' }), params());
        assert.equal(res.status, 401, `${method} ${route}`);
      }
      assert.equal(calls.length, 0, 'no upstream call may happen without an owner session');
    }),
  );
});

test('every owner handler refuses when the dashboard secret is missing, even with a cookie', async () => {
  const handlers = await ownerHandlers();
  await withEnv({ ...CONFIGURED, SNAPFALL_DASHBOARD_OWNER_SECRET: undefined }, () =>
    withFetchSpy(async (calls) => {
      for (const { route, method, handler } of handlers) {
        const res = await handler(ownerRequest(method, { cookie: sessionCookieHeader() }), params());
        assert.equal(res.status, 503, `${method} ${route}`);
        assert.equal(await errorCode(res), 'OWNER_ACCESS_NOT_CONFIGURED', `${method} ${route}`);
      }
      assert.equal(calls.length, 0);
    }),
  );
});

test('owner mutations refuse a signed-in but cross-site or text/plain request', async () => {
  const mutations = (await ownerHandlers()).filter((h) => h.method !== 'GET');
  assert.deepEqual(
    mutations.map((h) => h.route).sort(),
    ['approvals/[id]/decision/route.ts', 'jobs/[jobId]/accept-link/route.ts', 'workforce/[id]/hire/route.ts'],
  );
  await withEnv(CONFIGURED, () =>
    withFetchSpy(async (calls) => {
      const cookie = sessionCookieHeader();
      for (const { route, method, handler } of mutations) {
        const crossSite = await handler(
          ownerRequest(method, { cookie, origin: 'https://attacker.example', fetchSite: 'cross-site', contentType: 'text/plain' }),
          params(),
        );
        assert.ok([403, 415].includes(crossSite.status), `${route} cross-site → ${crossSite.status}`);
        const plain = await handler(ownerRequest(method, { cookie, contentType: 'text/plain' }), params());
        assert.equal(plain.status, 415, `${route} text/plain`);
      }
      assert.equal(calls.length, 0);
    }),
  );
});

test('a signed-in owner reaches the daemon with the owner bearer on every owner handler', async () => {
  const handlers = await ownerHandlers();
  await withEnv(CONFIGURED, () =>
    withFetchSpy(async (calls) => {
      const cookie = sessionCookieHeader();
      for (const { route, method, handler } of handlers) {
        const before = calls.length;
        const res = await handler(ownerRequest(method, { cookie }), params());
        assert.ok(res.status < 300, `${method} ${route} → ${res.status}`);
        assert.ok(calls.length > before, `${method} ${route} did not reach the daemon`);
        for (const call of calls.slice(before)) {
          assert.ok(call.url.startsWith(DAEMON), call.url);
          assert.equal(new Headers(call.init?.headers).get('authorization'), `Bearer ${FAKE_TOKEN}`);
        }
        await res.body?.cancel();
      }
    }),
  );
});

test('without a daemon, owner read routes still serve their public fallbacks unauthenticated', async () => {
  await withEnv({}, () =>
    withFetchSpy(async (calls) => {
      const approvals = await import('../app/api/approvals/route');
      const res = await approvals.GET(new Request(`${ORIGIN}/api/approvals`));
      assert.deepEqual(await res.json(), { approvals: [], source: 'mock' });
      const stream = await import('../app/api/events/stream/route');
      const sse = await stream.GET(new Request(`${ORIGIN}/api/events/stream`));
      assert.equal(sse.status, 200);
      assert.match(await sse.text(), /daemonConnected":false/);
      assert.equal(calls.length, 0);
    }),
  );
});

test('customer portal routes still forward the customer credential without an owner session', async () => {
  await withEnv(CONFIGURED, () =>
    withFetchSpy(async (calls) => {
      const res = await customerAcceptance(
        new Request(`${ORIGIN}/api/customer/job-1/acceptance`, { headers: { authorization: 'Bearer act_FAKE_customer' } }),
        { params: Promise.resolve({ jobId: 'job-1' }) },
      );
      assert.equal(res.status, 200);
      assert.equal(calls.length, 1);
      assert.equal(calls[0].url, `${DAEMON}/customer/jobs/job-1/acceptance`);
      assert.equal(new Headers(calls[0].init?.headers).get('authorization'), 'Bearer act_FAKE_customer');
    }),
  );
});

// ── Behind a reverse proxy ───────────────────────────────────────────────────────────────────

const PUBLIC = 'https://dash.example.com';
const UPSTREAM = 'http://127.0.0.1:3000';

/** A request as the Next.js server receives it from a proxy: URL and Host are the upstream's. */
function proxied(
  method: string,
  headers: Record<string, string>,
  path = '/api/owner-thing',
  body: string | undefined = method === 'GET' ? undefined : '{}',
): Request {
  const h = new Headers({ host: '127.0.0.1:3000', ...headers });
  if (method !== 'GET' && !h.has('content-type')) h.set('content-type', 'application/json');
  return new Request(`${UPSTREAM}${path}`, { method, headers: h, body });
}

const proxiedSignIn = (headers: Record<string, string>) =>
  sessionRoute.POST(proxied('POST', headers, '/api/owner/session', JSON.stringify({ secret: FAKE_SECRET })));

test('proxy rewriting Host (nginx default): refused today, fixed by SNAPFALL_DASHBOARD_ORIGIN', async () => {
  const browser = { origin: PUBLIC, 'sec-fetch-site': 'same-origin' };
  // The failure the reviewer found: Host is 127.0.0.1:3000, Origin is the public name.
  await withEnv(CONFIGURED, async () => {
    assert.equal((await proxiedSignIn(browser)).status, 403);
  });
  await withEnv({ ...CONFIGURED, SNAPFALL_DASHBOARD_ORIGIN: PUBLIC }, async () => {
    const res = await proxiedSignIn(browser);
    assert.equal(res.status, 204, 'sign-in works behind the proxy');
    const cookie = (res.headers.get('set-cookie') ?? '').split(';')[0];
    assert.equal(requireOwnerRequest(proxied('POST', { ...browser, cookie })).ok, true, 'and so do owner mutations');

    // The configured origin is exact: another site, another scheme or another port is refused.
    for (const origin of ['https://attacker.example', 'http://dash.example.com', 'https://dash.example.com:8443', 'null']) {
      const gate = requireOwnerRequest(proxied('POST', { origin, cookie }));
      assert.equal(gate.ok, false, origin);
      if (!gate.ok) assert.equal(gate.response.status, 403, origin);
    }
    // Host no longer matters to the CSRF check, so a spoofed X-Forwarded-Host changes nothing.
    assert.notEqual(checkSameOriginJson(proxied('POST', { origin: 'https://attacker.example', 'x-forwarded-host': 'attacker.example' })), null);
  });
});

test('SNAPFALL_DASHBOARD_ORIGIN accepts a list and normalises default ports', async () => {
  await withEnv({ ...CONFIGURED, SNAPFALL_DASHBOARD_ORIGIN: `${PUBLIC}/, http://localhost:3000` }, () => {
    assert.equal(checkSameOriginJson(proxied('POST', { origin: 'https://dash.example.com:443' })), null);
    assert.equal(checkSameOriginJson(proxied('POST', { origin: 'http://localhost:3000' })), null);
    assert.notEqual(checkSameOriginJson(proxied('POST', { origin: 'http://localhost:3001' })), null);
  });
});

test('a malformed SNAPFALL_DASHBOARD_ORIGIN fails closed across the owner surface', async () => {
  for (const bad of ['dash.example.com', 'https://dash.example.com/app', 'ftp://dash.example.com', 'https://u:p@dash.example.com']) {
    await withEnv({ ...CONFIGURED, SNAPFALL_DASHBOARD_ORIGIN: bad }, async () => {
      const gate = requireOwnerRequest(proxied('GET', { cookie: sessionCookieHeader() }));
      assert.equal(gate.ok, false, bad);
      if (!gate.ok) assert.equal(await errorCode(gate.response), 'OWNER_ACCESS_NOT_CONFIGURED', bad);
      const signInRes = await proxiedSignIn({ origin: PUBLIC });
      assert.equal(signInRes.status, 503, bad);
      assert.equal(signInRes.headers.get('set-cookie'), null, bad);
      const status = await sessionRoute.GET(proxied('GET', {}, '/api/owner/session'));
      assert.equal(((await status.json()) as { accessConfigured: boolean }).accessConfigured, false, bad);
    });
  }
});

test('proxy setting X-Forwarded-Host: trusted only with SNAPFALL_DASHBOARD_TRUST_PROXY, last value wins', async () => {
  const forwarded = { origin: PUBLIC, 'x-forwarded-host': 'dash.example.com' };
  const env = { ...CONFIGURED, SNAPFALL_DASHBOARD_HOSTS: 'dash.example.com' };
  await withEnv({ ...env, SNAPFALL_DASHBOARD_TRUST_PROXY: '1' }, async () => {
    assert.equal(checkHost(proxied('GET', forwarded)), null, 'the allowlist sees the public name');
    const res = await proxiedSignIn(forwarded);
    assert.equal(res.status, 204);
    const cookie = (res.headers.get('set-cookie') ?? '').split(';')[0];
    assert.equal(requireOwnerRequest(proxied('POST', { ...forwarded, cookie })).ok, true);
    // A proxy that appends leaves the client's value first; only the proxy's last value counts.
    const appended = { origin: 'https://attacker.example', 'x-forwarded-host': 'attacker.example, dash.example.com' };
    assert.equal(checkHost(proxied('GET', appended)), null);
    assert.notEqual(checkSameOriginJson(proxied('POST', appended)), null, 'Origin must match the proxy-written host');
  });
  // Without the opt-in, the same request is judged on its real Host.
  await withEnv(env, async () => {
    const res = checkHost(proxied('GET', forwarded));
    assert.equal(res?.status, 403);
    assert.equal((await proxiedSignIn(forwarded)).status, 403);
  });
});

test('without the opt-in a spoofed X-Forwarded-Host is ignored', async () => {
  await withEnv({ ...CONFIGURED, SNAPFALL_DASHBOARD_HOSTS: 'dashboard.local' }, () => {
    // Direct exposure: the attacker's page is rebound to the dashboard and adds its own header.
    const spoof = new Request('http://attacker.example/api/owner-thing', {
      method: 'POST',
      headers: {
        host: 'attacker.example',
        'x-forwarded-host': 'dashboard.local',
        origin: 'http://attacker.example',
        'content-type': 'application/json',
      },
      body: '{}',
    });
    assert.equal(checkHost(spoof)?.status, 403, 'the allowlist checks Host, not the spoofed header');
    const toForwarded = new Request(`${ORIGIN}/api/owner-thing`, {
      method: 'POST',
      headers: { host: 'dashboard.local', 'x-forwarded-host': 'attacker.example', origin: 'http://attacker.example', 'content-type': 'application/json' },
      body: '{}',
    });
    assert.equal(checkSameOriginJson(toForwarded)?.status, 403, 'nor can it make a foreign Origin look same-host');
  });
  for (const value of ['0', 'yes', 'on ']) {
    await withEnv({ ...CONFIGURED, SNAPFALL_DASHBOARD_TRUST_PROXY: value }, () => {
      const req = proxied('POST', { origin: 'https://attacker.example', 'x-forwarded-host': 'attacker.example' });
      assert.notEqual(checkSameOriginJson(req), null, `TRUST_PROXY=${value} is not an opt-in`);
    });
  }
});

test('proxy preserving Host (proxy_set_header Host $host) needs no new setting', async () => {
  await withEnv({ ...CONFIGURED, SNAPFALL_DASHBOARD_HOSTS: 'dash.example.com' }, async () => {
    const res = await proxiedSignIn({ host: 'dash.example.com', origin: PUBLIC });
    assert.equal(res.status, 204);
  });
});

// ── Cookie shadowing and the __Host- prefix ──────────────────────────────────────────────────

test('every same-named cookie is tried, so a planted one cannot shadow the real session', async () => {
  assert.deepEqual(readCookies('snapfall_owner_session=a; x=1; snapfall_owner_session=b', OWNER_SESSION_COOKIE), ['a', 'b']);
  await withEnv(CONFIGURED, () => {
    const real = issueSessionValue(FAKE_SECRET).value;
    const forged = issueSessionValue('fake-other-secret-for-tests-0000000000000').value;
    // A parent-domain cookie arrives first, as browsers order longer paths / older cookies first.
    const cookie = `${OWNER_SESSION_COOKIE}=${forged}; ${OWNER_SESSION_COOKIE}=${real}`;
    assert.equal(requireOwnerRequest(ownerRequest('GET', { cookie })).ok, true);
    // Planted values alone still open nothing.
    const gate = requireOwnerRequest(ownerRequest('GET', { cookie: `${OWNER_SESSION_COOKIE}=${forged}; ${OWNER_SESSION_COOKIE}=junk` }));
    assert.equal(gate.ok, false);
  });
});

test('production uses the __Host- cookie (Secure, Path=/, no Domain) and ignores the bare name', async () => {
  await withEnv({ ...CONFIGURED, NODE_ENV: 'production' }, async () => {
    const res = await signIn(FAKE_SECRET);
    assert.equal(res.status, 204);
    const set = res.headers.get('set-cookie') ?? '';
    assert.ok(set.startsWith(`${OWNER_SESSION_COOKIE_SECURE}=v1.`), set);
    assert.match(set, /; Secure/);
    assert.match(set, /; Path=\//);
    assert.doesNotMatch(set, /Domain=/i);
    const value = set.split(';')[0];
    assert.equal(requireOwnerRequest(ownerRequest('GET', { cookie: value })).ok, true);
    // The same valid value under the bare name is not accepted in production: any host could set that.
    const bare = `${OWNER_SESSION_COOKIE}=${value.slice(value.indexOf('=') + 1)}`;
    assert.equal(requireOwnerRequest(ownerRequest('GET', { cookie: bare })).ok, false);

    const out = await sessionRoute.DELETE(
      new Request(`${ORIGIN}/api/owner/session`, { method: 'DELETE', headers: { 'content-type': 'application/json', origin: ORIGIN } }),
    );
    assert.ok((out.headers.get('set-cookie') ?? '').startsWith(`${OWNER_SESSION_COOKIE_SECURE}=; `));
  });
});
