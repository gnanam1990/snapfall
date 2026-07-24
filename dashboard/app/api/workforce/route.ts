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
    return Response.json({ manifests: [BUILD_MONITOR_MANIFEST], source: 'local-catalog' });
  }
  try {
    const upstream = await fetch(`${base}/workforce/manifests`, {
      headers: ownerHeaders(),
      cache: 'no-store',
    });
    if (!upstream.ok) {
      return Response.json({ error: { code: 'DAEMON_UNAVAILABLE', message: 'Manifest catalog unavailable.' } }, { status: 502 });
    }
    return new Response(upstream.body, {
      status: upstream.status,
      headers: { 'content-type': 'application/json' },
    });
  } catch {
    return Response.json({ error: { code: 'DAEMON_UNAVAILABLE', message: 'Manifest catalog unavailable.' } }, { status: 502 });
  }
}
