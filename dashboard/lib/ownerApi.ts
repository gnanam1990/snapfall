/**
 * Owner/customer-API proxy helpers. The browser talks to Next.js route handlers; the
 * handlers talk to the daemon's H2 API server-side. This keeps the daemon on its
 * loopback posture and lets the mock stand in when SNAPFALL_OWNER_API_URL is unset.
 *
 *   SNAPFALL_OWNER_API_URL  e.g. http://127.0.0.1:4010/api/v1  (unset ⇒ mock/empty)
 *   SNAPFALL_OWNER_TOKEN    the OWNER bearer, forwarded only server-side, owner routes only
 *
 * The customer portal (V9) is different: its credential is the per-job accept token
 * from the magic link, forwarded from the CLIENT's request, never the owner token —
 * the two principals never cross even at the proxy layer.
 */

export function ownerApiBase(): string | null {
  return process.env.SNAPFALL_OWNER_API_URL?.replace(/\/$/, '') ?? null;
}

export function ownerAuthHeaders(extra?: HeadersInit): Headers {
  const h = new Headers(extra);
  const token = process.env.SNAPFALL_OWNER_TOKEN;
  if (token) h.set('authorization', `Bearer ${token}`);
  return h;
}

/** Forward an upstream JSON response verbatim — status AND body — so the client sees
 *  the daemon's real codes (409 STALE_VIEW, 410 APPROVAL_EXPIRED, …), never a flattened
 *  200. */
export function forwardJSON(status: number, body: string): Response {
  return new Response(body, { status, headers: { 'content-type': 'application/json' } });
}

/** Proxy a customer-portal request (V9). The credential is the CLIENT's — the per-job
 *  accept token from the magic link, forwarded from the incoming Authorization header,
 *  NOT the server's owner token. This never attaches SNAPFALL_OWNER_TOKEN. */
export async function proxyCustomer(
  req: Request,
  jobId: string,
  subpath: 'acceptance' | 'accept' | 'invoice',
  method: 'GET' | 'POST',
): Promise<Response> {
  const base = ownerApiBase();
  if (!base) {
    return Response.json(
      { error: { code: 'NO_DAEMON', message: 'owner API not configured (SNAPFALL_OWNER_API_URL unset)' } },
      { status: 503 },
    );
  }
  const headers = new Headers({ accept: 'application/json' });
  const auth = req.headers.get('authorization'); // the customer's magic-link credential
  if (auth) headers.set('authorization', auth);
  let body: string | undefined;
  if (method === 'POST') {
    headers.set('content-type', 'application/json');
    body = await req.text();
  }
  try {
    const upstream = await fetch(`${base}/customer/jobs/${encodeURIComponent(jobId)}/${subpath}`, {
      method,
      headers,
      body,
      cache: 'no-store',
    });
    return forwardJSON(upstream.status, await upstream.text());
  } catch {
    return Response.json({ error: { code: 'UPSTREAM', message: 'owner API unreachable' } }, { status: 502 });
  }
}
