import { CATALOG_MANIFESTS } from '@/lib/workforce';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

function ownerHeaders(): Headers {
  const headers = new Headers({ accept: 'application/json' });
  const token = process.env.SNAPFALL_OWNER_TOKEN;
  if (token) headers.set('authorization', `Bearer ${token}`);
  return headers;
}

export async function GET(): Promise<Response> {
  const base = process.env.SNAPFALL_OWNER_API_URL?.replace(/\/$/, '');
  if (!base) {
    // No daemon (the public deploy): serve the committed catalogue. This is the SAME source the
    // page initialises from (lib/workforce.ts CATALOG_MANIFESTS), so the route and the page cannot
    // disagree about which workers exist. These entries are static repo artefacts — name, category,
    // description, permission chips — not daemon state, so they must not depend on a daemon (the
    // same reasoning as treasury/pool reading chain directly). A daemon, when present, remains the
    // source below.
    return Response.json({ manifests: CATALOG_MANIFESTS, activations: [], source: 'local-catalog' });
  }
  try {
    const [catalog, activationState] = await Promise.all([
      fetch(`${base}/workforce/manifests`, { headers: ownerHeaders(), cache: 'no-store' }),
      fetch(`${base}/workforce/activations`, { headers: ownerHeaders(), cache: 'no-store' }),
    ]);
    if (!catalog.ok || !activationState.ok) {
      return Response.json({ error: { code: 'DAEMON_UNAVAILABLE', message: 'Workforce state unavailable.' } }, { status: 502 });
    }
    const catalogBody = await catalog.json() as { manifests?: unknown[] };
    const activationBody = await activationState.json() as { activations?: unknown[] };
    return Response.json({
      manifests: catalogBody.manifests ?? [],
      activations: activationBody.activations ?? [],
    });
  } catch {
    return Response.json({ error: { code: 'DAEMON_UNAVAILABLE', message: 'Workforce state unavailable.' } }, { status: 502 });
  }
}
