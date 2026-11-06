# ECO-MONITOR AI — WhatsApp Gateway (Koyeb)

Small Go HTTP service using `whatsmeow` with an SQLite session store. It provides a QR pairing page, connection status, health endpoint, and a restricted one-recipient text-message endpoint.

> **Important WhatsApp policy note:** `whatsmeow` is an unofficial WhatsApp Web multi-device client library, not Meta's official WhatsApp Business Platform. WhatsApp's terms and messaging guidance restrict unauthorized automated or non-personal use and may suspend accounts for automated messaging. Use this only for a controlled technical prototype with your own/test account and recipients who have explicitly agreed. For an operational alerting product, use the official WhatsApp Business Platform / Cloud API instead.

> **Koyeb Free limitation:** Free instances have 512 MB RAM, 0.1 vCPU and 2 GB SSD, but scale to zero after one hour without incoming traffic. Local SSD is ephemeral and there are no persistent volumes on the Free plan. SQLite is therefore acceptable for a short-lived ASIK demo, but the WhatsApp linked-device session can be lost after a reschedule/redeployment. UptimeRobot traffic can help prevent idle scale-down, but cannot guarantee no restarts or preserve the local database.

## What is included

- `GET /healthz` — unauthenticated health endpoint for Koyeb and UptimeRobot.
- `GET /pair` — password-protected QR pairing UI (the API token is entered into the page, never placed in the URL).
- `GET /api/status` — connection/session status; Bearer token required.
- `GET /api/qr.png` — current WhatsApp pairing QR; Bearer token required.
- `POST /api/send` — sends one text message to a number in `ALLOWED_RECIPIENTS`; Bearer token required, message limited to 1,000 characters, with a configurable global cooldown.
- SQLite session database at `/data/whatsmeow.db` by default.
- Optional Vercel server-side proxy examples in `vercel-proxy/`.

## 1. Prepare GitHub repository

1. Create a new **private** GitHub repository, for example `eco-monitor-wa-gateway`.
2. Upload the files from this folder to the repository root (`main.go`, `go.mod`, `Dockerfile`, `.dockerignore`, `.gitignore`, `README.md`).
3. Do not upload `.env` files, SQLite database files, or real API tokens.
4. The Dockerfile uses Go 1.27.2 and builds the SQLite CGO driver inside the image. `whatsmeow` is pinned in `go.mod` to a known compatible snapshot (`v0.0.0-20261017175727-b2b1e71d45e2`).

## 2. Generate secrets and choose recipients

Create two separate random secrets:

- `API_TOKEN`: at least 32 characters, preferably 64 random characters. This authenticates calls to the Koyeb gateway.
- `DASHBOARD_OPERATOR_KEY`: another long random secret, used only if you copy the optional Vercel proxy functions into the frontend repository.

Choose one test recipient first. Use international format without `+`, spaces, or punctuation. Example format for Indonesia: `62812xxxxxxx`.

The number must be explicitly listed in `ALLOWED_RECIPIENTS`. The API refuses any number not in that allowlist. For the first test, send only to a number you own or a recipient who has expressly agreed to receive the test message.

## 3. Deploy on Koyeb

1. Sign in to [Koyeb](https://app.koyeb.com/).
2. Create a **Web Service** from GitHub and select the private repository and `main` branch.
3. Select the **Dockerfile** builder / Dockerfile deployment option. Set the Dockerfile path to `/Dockerfile` if the UI asks for it.
4. Name the application `eco-monitor-wa` and the service `gateway` (names can vary).
5. Select region **Frankfurt, Germany**.
6. Select the **Free** instance (512 MB RAM, 0.1 vCPU, 2 GB SSD). Free service availability is limited to one instance per organization.
7. Expose port `8080` as **HTTP**, with route `/`.
8. Add environment variables:

| Name | Value | Notes |
|---|---|---|
| `PORT` | `8080` | Match exposed port. |
| `DATA_DIR` | `/data` | SQLite path on Koyeb local SSD. |
| `API_TOKEN` | your long random secret | Mark as Secret if Koyeb offers the Secret type. |
| `ALLOWED_RECIPIENTS` | `62812xxxxxxx` | Comma-separated allowlist; initially use one number. |
| `DEFAULT_RECIPIENT` | `62812xxxxxxx` | Must also be in the allowlist. |
| `ALLOWED_ORIGINS` | `https://asik-dashboard.vercel.app` | Optional. Exact origin(s), comma-separated. Proxy-to-proxy calls do not require browser CORS. |
| `MIN_SEND_INTERVAL_SECONDS` | `20` | Minimum interval between sends. |

9. Deploy. Wait for the deployment state to become **Healthy**.
10. Open the generated Koyeb domain and test `/healthz`. It should return JSON similar to `{"ok":true,"service":"eco-monitor-whatsapp-gateway"}`.

Koyeb Web Services provide the `PORT` environment variable automatically when a port is exposed. The application binds to `0.0.0.0` via `:PORT`.

## 4. Pair WhatsApp using QR

1. Open `https://YOUR-SERVICE.koyeb.app/pair`.
2. Paste the `API_TOKEN` into the password field and click **Periksa status & tampilkan QR**.
3. On the phone that owns the WhatsApp account, open WhatsApp → **Settings / Setelan** → **Linked devices / Perangkat tertaut** → **Link a device / Tautkan perangkat**.
4. Scan the QR displayed on `/pair`.
5. Wait until the page shows `connected` and `whatsapp_logged_in: true`.

Keep the QR and API token private. The QR is short-lived and rotates; the page automatically refreshes it. If the page reports `qr_timeout_restart_required`, restart the Koyeb service and open `/pair` again.

If the WhatsApp account is paired already and the SQLite file still exists, the gateway attempts to reconnect automatically on startup. If the local database is lost, pair the account again.

## 5. Test the connection and send one message

Read status (replace the URL and token):

```bash
curl -H "Authorization: Bearer YOUR_API_TOKEN" \
  https://YOUR-SERVICE.koyeb.app/api/status
```

Send a test message to the configured allowlisted test number:

```bash
curl -X POST https://YOUR-SERVICE.koyeb.app/api/send \
  -H "Authorization: Bearer YOUR_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"to":"62812xxxxxxx","message":"Tes notifikasi ECO-MONITOR AI. Ini pesan uji."}'
```

Expected success response:

```json
{"ok":true,"message":"message accepted by WhatsApp server"}
```

The endpoint reports success only after WhatsApp acknowledges the send request. It is not a delivery/read receipt.

## 6. Set up UptimeRobot

1. Sign in to [UptimeRobot](https://uptimerobot.com/).
2. Choose **Add New Monitor** → **HTTP(s)**.
3. Friendly Name: `ECO-MONITOR Koyeb Gateway`.
4. URL: `https://YOUR-SERVICE.koyeb.app/healthz`.
5. Monitoring interval: Free plan's 5-minute interval.
6. Save the monitor.

This sends periodic external HTTP requests to the Koyeb app and can reset the one-hour idle timer as long as checks keep reaching the service. It is not a persistent-volume substitute and does not prevent provider maintenance, deployments, or restarts. Check that the monitor is Up after setup.

## 7. Optional: connect the Vercel frontend safely

Do **not** place `API_TOKEN` in a `VITE_*` variable or React source code. Anything bundled into a Vite frontend is visible to website visitors.

If you want a frontend API route:

1. Copy `vercel-proxy/api/` into the root of the Vercel frontend repository (alongside `src/`, `package.json`, and `vite.config.*`).
2. Add these Vercel environment variables (server-side names; no `VITE_` prefix):
   - `WHATSAPP_GATEWAY_URL` = Koyeb base URL.
   - `WHATSAPP_GATEWAY_TOKEN` = same as Koyeb `API_TOKEN`.
   - `WHATSAPP_DEFAULT_RECIPIENT` = allowlisted destination number.
   - `DASHBOARD_ORIGIN` = exact production dashboard URL.
   - `DASHBOARD_OPERATOR_KEY` = separate long random operator password.
3. Redeploy Vercel.
4. The frontend can call `GET /api/wa-status` for status and `POST /api/wa-send` with `x-dashboard-operator-key` for a send request. Ask a trusted operator to enter the operator key at runtime; do not hardcode it in frontend source.

The proxy only accepts the configured dashboard origin and sends to the single configured default recipient. Keep the send UI available only to trusted operators. For a real public production system, add authenticated user sessions and audit logging before exposing message-sending controls.

## 8. Troubleshooting

- **Koyeb deployment fails during build:** inspect Build Logs. Confirm the builder is Dockerfile and the Dockerfile path is `/Dockerfile`. Go dependency downloads require outbound internet access during build.
- **Container exits immediately with `API_TOKEN` error:** set `API_TOKEN` to a string at least 32 characters long.
- **`/healthz` works but WhatsApp is offline:** check `/pair` and `/api/status`; the HTTP service can be healthy while WhatsApp is unpaired or disconnected.
- **QR not visible:** confirm `API_TOKEN`, reload `/pair`, and inspect runtime logs. Restart the service if the QR session timed out.
- **Send endpoint returns 401:** check the Bearer token.
- **Send endpoint returns 403:** the recipient must appear in `ALLOWED_RECIPIENTS` exactly in international digits-only format.
- **Send endpoint returns 503:** scan the QR and wait until the gateway reports both connected and logged in.
- **Session is lost after restart/redeploy:** expected on Koyeb Free if ephemeral storage was reset. Re-pair the account. Use persistent storage or an external database for ongoing operation.
- **High memory use or restart:** the Free instance is small. Keep one linked account, avoid heavy history processing, and use this only as a lightweight prototype.

## API summary

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/` | No | Service information |
| GET | `/healthz` | No | Uptime / health check |
| GET | `/pair` | Page; token required for API polling | QR pairing UI |
| GET | `/api/status` | Bearer | WhatsApp connection status |
| GET | `/api/qr.png` | Bearer | QR image |
| POST | `/api/send` | Bearer | Send one text message to allowlisted recipient |

## References

- Koyeb Free Instances: https://www.koyeb.com/docs/reference/instances
- Koyeb local SSD: https://www.koyeb.com/docs/reference/storage
- Koyeb scale-to-zero: https://www.koyeb.com/docs/run-and-scale/scale-to-zero
- Koyeb Go/Docker deployment: https://www.koyeb.com/docs/deploy/go
- whatsmeow API: https://pkg.go.dev/go.mau.fi/whatsmeow
- whatsmeow repository: https://github.com/tulir/whatsmeow
- WhatsApp Terms: https://www.whatsapp.com/legal/terms-of-service
- WhatsApp Messaging Guidelines: https://www.whatsapp.com/legal/messaging-guidelines
