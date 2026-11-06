import { timingSafeEqual } from 'node:crypto';

function getRequestOrigin(req) {
  const raw = req.headers.origin || req.headers.referer || '';
  try {
    return new URL(raw).origin;
  } catch {
    return '';
  }
}

function secureEquals(a, b) {
  const left = Buffer.from(String(a || ''));
  const right = Buffer.from(String(b || ''));
  return left.length === right.length && timingSafeEqual(left, right);
}

export default async function handler(req, res) {
  if (req.method !== 'POST') {
    res.setHeader('Allow', 'POST');
    return res.status(405).json({ ok: false, error: 'Method not allowed' });
  }

  let expectedOrigin = '';
  try { expectedOrigin = new URL(process.env.DASHBOARD_ORIGIN || '').origin; } catch {}
  const origin = getRequestOrigin(req);
  if (!expectedOrigin || origin !== expectedOrigin) {
    return res.status(403).json({ ok: false, error: 'Origin not allowed' });
  }

  const operatorKey = process.env.DASHBOARD_OPERATOR_KEY;
  if (!operatorKey || !secureEquals(req.headers['x-dashboard-operator-key'], operatorKey)) {
    return res.status(401).json({ ok: false, error: 'Dashboard operator key is missing or invalid' });
  }

  const baseUrl = (process.env.WHATSAPP_GATEWAY_URL || '').replace(/\/+$/, '');
  const token = process.env.WHATSAPP_GATEWAY_TOKEN;
  const defaultRecipient = process.env.WHATSAPP_DEFAULT_RECIPIENT;
  if (!baseUrl || !token || !defaultRecipient) {
    return res.status(503).json({ ok: false, error: 'WhatsApp Gateway is not configured on Vercel' });
  }

  const message = typeof req.body?.message === 'string' ? req.body.message.trim() : '';
  if (!message || [...message].length > 1000) {
    return res.status(400).json({ ok: false, error: 'Message must contain 1 to 1000 characters' });
  }

  try {
    const upstream = await fetch(`${baseUrl}/api/send`, {
      method: 'POST',
      headers: {
        Authorization: `Bearer ${token}`,
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({ to: defaultRecipient, message }),
      cache: 'no-store',
      signal: AbortSignal.timeout(70000),
    });
    const body = await upstream.json();
    return res.status(upstream.status).json(body);
  } catch {
    return res.status(502).json({ ok: false, error: 'Cannot reach WhatsApp Gateway' });
  }
}
