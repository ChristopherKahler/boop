# Deploying on Railway

The hub runs on Railway as one service built from `server/Dockerfile`, with one volume for its SQLite file. Nothing in the code changes between running it on your own PC and running it on Railway; only environment variables do.

Checked against docs.railway.com on 2026-10-22 and 2026-10-23.

## What the repo already does for Railway

- **Port.** The server listens on `BOOP_PORT` if it is set, otherwise on the `PORT` Railway injects, otherwise on 8080. Railway also uses `PORT` for its health check, so leave `BOOP_PORT` unset on Railway.
- **Config file.** `server/railway.json` builds with the Dockerfile, health-checks `GET /health` (60-second timeout) and restarts on failure (up to 10 retries).
- **Login from the first start.** With no login in the environment, the hub prints a one-time setup code in its log, and nobody can use it until someone enters that code and creates the login. A public Railway address is never open.
- **CI** builds the image, runs it with `PORT=9090` and checks `/health`, a 401 on an admin endpoint, and the setup code in the log.

## Steps

1. **New service from the repo.** In a Railway project: **New → GitHub Repo**, pick your copy of this repository.
2. **Root directory.** Open the service, **Settings** tab, set **Root Directory** to `/server`. Railway then builds only that folder, where the Dockerfile is.
3. **Config file path.** On the same **Settings** page, set the custom config file to `/server/railway.json`. Use this absolute path: Railway's config file does not follow the Root Directory.
4. **Volume.** Add a volume (right-click the project canvas, or the command palette) attached to this service, mount path `/data`. The database lives at `/data/boop.db` (the image sets `BOOP_DATABASE_PATH`).
5. **Variables.** Service **Variables**:

   | Variable | Value | Why |
   | --- | --- | --- |
   | `RAILWAY_RUN_UID` | `0` | Railway mounts volumes as root and the image runs as uid 1000, so without this SQLite cannot write `/data/boop.db`. |
   | `BOOP_BASE_URL` | `https://<your-service>.up.railway.app` (or your own domain) | The address the phone app, links in pushes and the setup screens use. Fill it in after step 6. |
   | `BOOP_INTAKE_URL` | same as `BOOP_BASE_URL` | The public address shown for webhook intakes. On Railway the whole hub is public, so it is the same address. |
   | `BOOP_TIMEZONE` | e.g. `America/Chicago` | Month boundaries and times in the monthly summary. Default `UTC`. |
   | `BOOP_ADMIN_USER`, `BOOP_ADMIN_PASSWORD` | optional | Set both to fix the login in the environment. Leave both unset to create the login in the browser with the setup code (step 8). |

   Leave `BOOP_PORT` and `BOOP_ALLOW_NO_LOGIN` unset. The full list is in the [README's configuration table](../README.md#configuration).
6. **Domain.** Service **Settings → Networking → Public Networking → Generate Domain** (or add your own domain). Put that `https://` address in `BOOP_BASE_URL` and `BOOP_INTAKE_URL`.
7. **Deploy.** Railway builds the image and waits for `/health` to answer 200 before it switches traffic.
8. **First visit.** Open the deploy logs and find the line with `"msg":"auth.setup_required"`; its `setup_code` field holds a code like `K7QD-M2XA`. Open your domain, enter the code, choose a username and password, and the wizard takes you from your first client to a test push on your phone. (Skip this step if you set `BOOP_ADMIN_USER` and `BOOP_ADMIN_PASSWORD`.)

Then install the phone app and connect your monitoring services: see the [install guide](install-guide.md#3-put-the-app-on-your-phone).

## Keep in mind

- **One replica.** Railway does not run replicas on a service with a volume, and the hub keeps everything in one SQLite file. Leave the replica count at 1.
- **A short gap on each deploy.** Railway lets only one deployment mount the volume at a time, so the old one stops before the new one starts. A webhook that arrives in that gap fails, so deploy when a missed alert matters least.
- **Push keys.** Without `BOOP_VAPID_PUBLIC_KEY`/`BOOP_VAPID_PRIVATE_KEY` the hub generates a key pair on first start and keeps it in the database on the volume, so phones stay subscribed across deploys. Moving a hub to a new database (for example from your PC to a fresh Railway volume) means turning notifications on again on each phone, unless both run with the same pair: print one with `boop vapid` (any copy of the binary) and set the two lines it prints on both.
- **Moving from your PC.** Either start fresh, or carry the database file into the volume before the first start. If you carry it over, each intake keeps its token, but every monitoring service still posts to the old host: change the host part of each webhook address to the Railway domain.
- **Backups.** The volume is the only copy of your data. Railway offers manual and automated backups for services with volumes; turn them on.
- **APNs** (the optional native iOS app) is configured the same way as anywhere else: set the `APNS_*` variables, with `APNS_PRIVATE_KEY` as one base64 line (see the README's [Dokploy section](../README.md#deploying-with-dokploy-or-any-compose-host) for the format).
