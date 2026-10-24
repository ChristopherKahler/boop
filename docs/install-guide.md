# Install guide

Run your own alert hub: the monitoring services you already use (Sentry, Healthchecks, Uptime Kuma, GitHub Actions, Laravel Nightwatch, or anything that sends a JSON webhook) post to it, it groups their alerts by client, and it pushes them to an app on your phone. It does not watch anything itself.

You need:

- somewhere to run it: [Railway](#railway), [any Docker host](#docker-compose), or [a Windows PC](#windows-pc);
- an **https** address your phone can reach (phones only allow push notifications from https sites);
- for the webhooks, an address the monitoring services can reach from the internet.

## 1. Run the hub

### Railway

Follow [railway.md](railway.md). Railway gives you an https address that is public, so the phone and the webhooks both work with no extra setup.

### Docker compose

```bash
git clone https://github.com/ChristopherKahler/boop && cd boop
cp .env.example .env            # then set BOOP_BASE_URL to your https address
mkdir -p data && chown 1000:1000 data   # Linux hosts only; the container runs as uid 1000
docker compose up -d --build    # listens on port 8080
```

Put it behind something that serves https: your reverse proxy (Caddy, Traefik, nginx), a Cloudflare Tunnel, or `tailscale serve` (tailnet only). Set `BOOP_BASE_URL` to that address and, if webhooks reach it on a different public address, `BOOP_INTAKE_URL` to that one. On Dokploy use `docker-compose.dokploy.yml` (see the [README](../README.md#deploying-with-dokploy-or-any-compose-host)).

### Windows PC

The hub runs in the background at sign-in, with no window, and restarts itself if it stops. No administrator rights are needed. You need Go 1.27 and Node 24 to build it, and [Tailscale](https://tailscale.com) signed in on the PC and on your phone for the phone to reach it.

```powershell
git clone https://github.com/ChristopherKahler/boop; cd boop\server\web
npm ci; npm run build                      # builds the web UI into the binary's folder
cd ..\..
pwsh -File deploy\windows\install.ps1 -TailscalePort 10002
```

- It builds `boop.exe`, installs it under `%LOCALAPPDATA%\boop`, and registers the scheduled task **Boop alert hub** (at sign-in, plus a check every 5 minutes that restarts the hub if it stopped).
- `-TailscalePort` publishes the hub on your tailnet over https (`https://<pc>.<tailnet>.ts.net:10002`), so your phone can open it while Tailscale is on. Pick any free port; leave it out to keep the hub on this PC only.
- Settings go in `%LOCALAPPDATA%\boop\boop.env`, created once with a login: your Windows user name and a random password (it is in that file). Change a setting, then run `install.ps1` again to restart the hub. Logs are in `%LOCALAPPDATA%\boop\logs`.
- To update: pull, rebuild the web UI, run `install.ps1` again. It keeps `boop.env` and the database.
- Webhooks from the internet need a public address for `/hooks/` only. With Tailscale, [Funnel](https://tailscale.com/kb/1223/funnel) can publish just that path while the rest of the hub stays on your tailnet; set `BOOP_INTAKE_URL` to the funnel address. Funnel has to be allowed in your tailnet policy first.

## 2. First visit

Open the hub's address in a browser.

- **If you set a login** (`BOOP_ADMIN_USER` and `BOOP_ADMIN_PASSWORD`, or the Windows `boop.env`), sign in with it.
- **Otherwise** the hub asks for a **setup code**. It is in the hub's log, on the line with `"msg":"auth.setup_required"`, field `setup_code` (looks like `K7QD-M2XA`): `docker compose logs boop | grep setup_code` on Docker, the deploy logs on Railway. Enter it and choose your username and password. A new code is printed each time the hub starts until a login exists.

A sign-in lasts until 30 days after your last visit and survives restarts. Five wrong passwords from one address within 15 minutes lock that address out until 15 minutes have passed since the last try.

The first-visit wizard then walks you through four steps: your first client, connecting a service, putting the app on your phone, and a test push.

## 3. Put the app on your phone

Open the hub's https address on the phone and sign in.

- **Android** (Chrome): menu ⋮ → **Add to Home screen** → **Install**.
- **iPhone** (Safari, iOS 16.4 or later): Share → **Add to Home Screen**. On iPhone, notifications only work from the home-screen app, so do this first.

Then open the app from the home screen, go to **Devices**, tap **Turn on notifications**, allow them, and tap **Send test push**. Each phone (or browser) you turn on appears in the Devices list.

A push names the client and the alert, with **Acknowledge** and the alert's links as buttons (as many as the phone shows on a notification). Tapping the notification opens that alert.

## 4. Clients, projects and routing

- A **client** is who you watch things for; each **project** (an app, a site, a server) belongs to one client. Create them on the **Clients** page.
- **Routing** (the button on each client) sets which levels push to your phone; the rest only land in the inbox. New clients push warning, error and critical, and send a "resolved" push when something that pushed recovers. A project can override the levels.
- Repeats of an open problem never push again; they are counted on the one alert. An alert that gets worse (say warning to critical) pushes again if the new level is one that pushes.
- Silences (on any event's page) still work as before: matching events are stored but never pushed.

## 5. Connect your monitoring services

Each project gets one webhook address per service: the project's settings → **Intakes** → **Add intake**, pick the service. The address is shown once (only a hash is kept); **New address** replaces it.

How the webhook addresses are protected, since they are the only part of the hub that has to be reachable from the internet:

- Each address ends in a long random secret. A wrong address gets the same "not found" answer whatever it is, so addresses cannot be guessed or probed.
- Sentry, GitHub, Laravel Nightwatch and the JSON mapping can also sign every message. Paste the service's secret into the intake and the hub refuses any message whose signature does not match.
- Each address accepts up to 120 requests a minute, and each sender up to 600 a minute across all addresses, wrong ones included. Over that, the sender is told to wait (HTTP 429), and the intake shows "rate limited". Change the numbers with `BOOP_HOOK_LIMIT` and `BOOP_HOOK_SENDER_LIMIT`.
- If an address leaks, press **New address**: the old one stops working at once.

| Service | Where to paste the address | Opens an alert on | Resolves it on |
| --- | --- | --- | --- |
| **Sentry** | Settings → Developer Settings → Create New Integration → **Internal Integration**. Webhook URL: the address. Turn on **Alert Rule Action**; under Webhooks tick **issue**. Copy its **Client Secret** into the intake's signing secret. Add the integration as an action in your alert rules. | new or unresolved issue, metric alert (critical or warning) | issue resolved, metric alert resolved |
| **Healthchecks** | Integrations → **Webhook**. For both "down" and "up": method **POST**, the address, header `Content-Type: application/json`, body `{"status": "$STATUS", "now": "$NOW", "check": $JSON}` | a check goes down (missed, late, or failed run) | it comes back up |
| **Uptime Kuma** | Settings → Notifications → **Setup Notification**, type **Webhook**, Post URL: the address, Request Body **application/json**. Turn it on for your monitors. | monitor down | monitor up |
| **GitHub Actions** | The repository's Settings → Webhooks → **Add webhook**. Payload URL: the address. Content type **application/json**. Secret: the token at the end of the address. Events: **Workflow runs**. | a run that failed, timed out or could not start | the next successful run of that workflow on the same branch |
| **Laravel Nightwatch** | The application's Settings → Issues → **Webhooks**. URL: the address. Copy the webhook's signing secret into the intake's signing secret. | a new or reopened issue | issue resolved or ignored |
| **Anything else** | The service's webhook setting. In the intake, map the service's JSON to an alert: `{{path}}` fields for title, body, level, fingerprint and link, a `level_map` for the service's own severity names, and `resolve_when` for its recovery status. **Preview** shows what a sample payload would become before you save. | every request that maps to an alert | a request matching `resolve_when` |

Apps you write yourself can still post straight to `POST /api/v1/events` with the project's API key, and any server-side Sentry SDK can report to the hub by pointing its DSN at it (see the [README](../README.md#sentry-sdks--drop-in-dsn)).

## 6. Acknowledge, resolve, and the monthly summary

- **Acknowledge** from the notification, the app or the web UI: the alert keeps when it was acknowledged and from where. **Resolve** closes it; a recovery from the service resolves it on its own. **Reopen** if it was not fixed.
- **Monthly summary**: on the Clients page, **Monthly summary** for a client shows a month's alerts by level and by project, what broke (first seen, last seen, how often), time to acknowledge and time to resolve, and what recovered on its own. **Download Markdown** gives you the same report to send. Set `BOOP_TIMEZONE` (for example `America/Chicago`) so months and times are in your zone.

## 7. Back up and update

- Everything is in one SQLite file (`boop.db`). Back it up by copying it while the hub is stopped, or with `sqlite3 boop.db ".backup backup.db"` while it runs (on Railway, use volume backups).
- To update, pull the new version and rebuild: `docker compose up -d --build`, a Railway redeploy, or `install.ps1` on Windows. Migrations run on start.
