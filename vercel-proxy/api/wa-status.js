function getRequestOrigin(req) {
  const raw = req.headers.origin || req.headers.referer || '';
  try {
    return new URL(raw).origin;
  } catch {
    return '';
  }
}

export default async function handler(req, res) {
  if (req.method !== 'GET') {
    res.setHeader('Allow', 'GET');
    return res.status(405).json({ ok: false, error: 'Method not allowed' });
  }

  let expectedOrigin = '';
  try { expectedOrigin = new URL(process.env.DASHBOARD_ORIGIN || '').origin; } catch {}
  const origin = getRequestOrigin(req);
  if (!expectedOrigin || origin !== expectedOrigin) {
    return res.status(403).json({ ok: false, error: 'Origin not allowed' });
  }

  const baseUrl = (process.env.WHATSAPP_GATEWAY_URL || '').replace(/\/+$/, '');
  const token = process.env.WHATSAPP_GATEWAY_TOKEN;
  if (!baseUrl || !token) {
    return res.status(503).json({ ok: false, error: 'WhatsApp Gateway is not configured on Vercel' });
  }

  try {
    const upstream = await fetch(`${baseUrl}/api/status`, {
      method: 'GET',
      headers: { Authorization: `Bearer ${token}` },
      cache: 'no-store',
      signal: AbortSignal.timeout(12000),
    });
    const body = await upstream.json();
    return res.status(upstream.status).json(body);
  } catch {
    return res.status(502).json({ ok: false, error: 'Cannot reach WhatsApp Gateway' });
  }
}
