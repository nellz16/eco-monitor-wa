# Optional Vercel server-side proxy

Copy the `api/` directory from this folder into the **root of the Vite/Vercel frontend repository**. Do not place it inside `src/`.

Set these environment variables in Vercel (server-side only; none use the `VITE_` prefix):

- `WHATSAPP_GATEWAY_URL`: full Koyeb URL, e.g. `https://YOUR-APP-YOUR-ORG.koyeb.app`
- `WHATSAPP_GATEWAY_TOKEN`: same secret as Koyeb `API_TOKEN`
- `WHATSAPP_DEFAULT_RECIPIENT`: same recipient listed in Koyeb `ALLOWED_RECIPIENTS`
- `DASHBOARD_ORIGIN`: exact dashboard origin, e.g. `https://asik-dashboard.vercel.app`
- `DASHBOARD_OPERATOR_KEY`: a separate long random key. Ask the operator to enter it at runtime in the dashboard UI; do not put it in client-side source or any `VITE_` variable.

Endpoints:

- `GET /api/wa-status`: reads WhatsApp connection status. Browser request must come from `DASHBOARD_ORIGIN`.
- `POST /api/wa-send`: sends one message to the one configured default recipient. Include `x-dashboard-operator-key` with the operator key in the request header.

The Koyeb API token stays between Vercel's serverless function and Koyeb. The operator key is still a shared password, so expose the send control only to trusted operators and rotate the key if it is shared accidentally.
