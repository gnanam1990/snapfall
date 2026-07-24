import { BUILD_MONITOR_MANIFEST } from '@/lib/workforce';

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
    return Response.json({ manifests: [BUILD_MONITOR_MANIFEST], activations: [], source: 'local-catalog' });
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
