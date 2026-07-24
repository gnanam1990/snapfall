export const runtime = 'nodejs';

function ownerHeaders(): Headers {
  const headers = new Headers({ accept: 'application/json', 'content-type': 'application/json' });
  const token = process.env.SNAPFALL_OWNER_TOKEN;
  if (token) headers.set('authorization', `Bearer ${token}`);
  return headers;
}

function isTrustedMutation(req: Request): boolean {
  const contentType = req.headers.get('content-type')?.split(';', 1)[0]?.trim().toLowerCase();
  if (contentType !== 'application/json') return false;

  const fetchSite = req.headers.get('sec-fetch-site')?.toLowerCase();
  if (fetchSite) return fetchSite === 'same-origin';

  const origin = req.headers.get('origin');
  if (!origin) return false;
  try {
    return new URL(origin).origin === new URL(req.url).origin;
  } catch {
    return false;
  }
}

export async function POST(req: Request, context: { params: Promise<{ id: string }> }): Promise<Response> {
  if (!isTrustedMutation(req)) {
    return Response.json(
      { error: { code: 'FORBIDDEN', message: 'Workforce activation requires a same-origin JSON request.' } },
      { status: 403 },
    );
  }
  const base = process.env.SNAPFALL_OWNER_API_URL?.replace(/\/$/, '');
  if (!base) {
    return Response.json(
      { error: { code: 'DAEMON_UNAVAILABLE', message: 'Start the daemon and configure SNAPFALL_OWNER_API_URL to hire a watcher.' } },
      { status: 503 },
    );
  }
  const { id } = await context.params;
  let body: unknown;
  try {
    body = await req.json();
  } catch {
    return Response.json({ error: { code: 'BAD_REQUEST', message: 'Malformed hire request.' } }, { status: 400 });
  }
  try {
    const upstream = await fetch(`${base}/workforce/${encodeURIComponent(id)}/hire`, {
      method: 'POST',
      headers: ownerHeaders(),
      body: JSON.stringify(body),
      cache: 'no-store',
      signal: AbortSignal.any([req.signal, AbortSignal.timeout(5_000)]),
    });
    return new Response(upstream.body, {
      status: upstream.status,
      headers: { 'content-type': 'application/json' },
    });
  } catch {
    return Response.json({ error: { code: 'DAEMON_UNAVAILABLE', message: 'Worker activation endpoint unavailable.' } }, { status: 502 });
  }
}
