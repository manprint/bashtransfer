# Operations

Deployment and management of bashtransfer on a VM with Docker. Usage and configuration are in the [README](../README.md); internal workings in [architettura.md](architettura.md).

## VM requirements

- Linux with kernel ≥ 4.15 (TCP sysctls per network namespace), Docker Engine with the `compose` plugin.
- Ports 80 and 443 reachable from the Internet; no other port is needed (the app publishes no ports).
- `tcp_bbr` module loaded on the host: compose sets `bbr` in nginx's network namespace, and a container cannot load modules.
- TLS certificate for the public name (`fullchain.pem`, `privkey.pem`), managed outside the repository (for example certbot on the host).
- Memory: each active session uses at most one copy buffer (256 KiB by default) in the app, plus the kernel's TCP buffers, which grow up to 32 MiB per socket only as needed; the total is limited by the host's `net.ipv4.tcp_mem`.

## First deployment

1. Load BBR, including across reboots:

   ```bash
   sudo modprobe tcp_bbr
   echo tcp_bbr | sudo tee /etc/modules-load.d/bbr.conf
   ```

2. The VM needs only `compose.yaml` and `deploy/nginx/nginx.conf` (cloning the repository is the simplest way). Create `.env` next to `compose.yaml` (not versioned, permissions `600`):

   ```
   BASHT_IMAGE=ghcr.io/owner/bashtransfer:1.0.0
   BASHT_CERTS_DIR=/etc/ssl/bashtransfer
   BASHT_PUBLIC_URL=https://bashtransfer.mydomain.it
   BASHT_AUTH_USER=user
   BASHT_AUTH_PASSWORD=a-long-password
   ```

   `BASHT_CERTS_DIR` must contain `fullchain.pem` and `privkey.pem` as regular files. With certbot, the files in `live/` are relative symbolic links to `archive/`, which do not resolve inside the container: copy them to `/etc/ssl/bashtransfer` with a deploy hook (section "Certificate renewal"). nginx runs without `DAC_OVERRIDE`: the files must be owned by root or readable by everyone; `privkey.pem` owned by root with permissions `600` is fine (verified).

   If the package on ghcr.io is private: `docker login ghcr.io` with a token that has the `read:packages` permission.

3. Start and check:

   ```bash
   docker compose pull
   docker compose up -d
   docker compose ps                     # app: healthy, nginx: running
   docker compose logs app | head        # server_start, server_listening
   curl -sS https://bashtransfer.mydomain.it/
   ```

4. Check nginx's effective TCP parameters:

   ```bash
   docker compose exec nginx sysctl net.ipv4.tcp_congestion_control net.ipv4.tcp_rmem net.ipv4.tcp_wmem
   ```

   Expected `bbr`, `4096 131072 33554432`, `4096 16384 33554432`. If the `tcp_bbr` module is missing, the nginx container should not start (sysctl error): load the module, or set `BASHT_TCP_CC=cubic` in `.env`.

5. End-to-end test from the workstation (not from the VM):

   ```bash
   BASHT_E2E_BASE=https://bashtransfer.mydomain.it BASHT_E2E_MB=256 \
     BASHT_E2E_AUTH=user:a-long-password bash scripts/e2e.sh
   ```

   The script accepts any certificate. Without auth configured, omit `BASHT_E2E_AUTH`. It leaves a `transfer` entry with `outcome` `aborted` in the logs for each simulated drop case: this is expected.

## Updates

Set the new tag in `.env` (`BASHT_IMAGE=ghcr.io/owner/bashtransfer:1.1.0`; use version tags, not `latest`, so you know what is running and can roll back), then:

```bash
git pull                # only if compose.yaml or nginx.conf change
docker compose pull
docker compose up -d
```

To return to the previous version: restore the previous tag in `.env` and repeat `docker compose up -d`.

Recreating `app` interrupts ongoing transfers after a 10-second grace period; waiting senders are closed immediately. Slugs issued before the restart respond with `404` (sessions are only in memory). nginx resolves `app` on every connection and does not need to be restarted.

### Frontend updates and browser check

HTML, CSS and JavaScript are templates embedded in the Go binary. Node.js, npm, a frontend server or a separate build are not needed on the VM: to publish UI changes, you must update the image and recreate `app`, with the same effects on transfers described above.

After an update, verify from the workstation and from a phone:

1. Open the HTTPS page, choose a file and share the link with **Link** or **Copy link**. UNREADY on the sender before the receiver arrives is normal and does not prevent Share.
2. Return to the same page in the sender's browser and keep it in the foreground; open the link on another device. After the server confirms, both pages show a green READY and the Download button is enabled. Opening the link and polling do not consume it.
3. Before starting the download, move the sender to the background and then back: verify UNREADY and the subsequent reconnection within `BASHT_SENDER_GRACE`. The grace period does not keep the file and does not allow resuming interrupted downloads.
4. Press Download only once and keep both devices connected. Check the received file and the outcome on the sender. UNREADY after the session has ended is normal: it does not mean the transfer failed.

The Playwright checks simulate mobile viewports and some native APIs; they do not replace this check of real behavior on Android/iOS and in the in-app browsers of chat apps.

## Certificate renewal

nginx reads the certificates at startup. After renewal:

```bash
docker compose exec nginx nginx -s reload
```

The reload does not interrupt transfers: old workers exit when their connections finish. With certbot, a deploy hook that copies the files and reloads nginx, for example `/etc/letsencrypt/renewal-hooks/deploy/bashtransfer.sh` (executable):

```bash
#!/bin/sh
set -e
install -m 644 -o root -g root /etc/letsencrypt/live/bashtransfer.mydomain.it/fullchain.pem /etc/ssl/bashtransfer/fullchain.pem
install -m 600 -o root -g root /etc/letsencrypt/live/bashtransfer.mydomain.it/privkey.pem /etc/ssl/bashtransfer/privkey.pem
cd /opt/bashtransfer && docker compose exec -T nginx nginx -s reload
```

(`/opt/bashtransfer` is the directory containing `compose.yaml` and `.env`.)

## Log

JSON format in compose (`BASHT_LOG_FORMAT`), rotation 3 × 10 MB per container. Events and fields are described in the README, section "Log". Examples with `jq`:

```bash
# all outcomes, live
docker compose logs -f --no-log-prefix app | jq -c 'select(.msg=="transfer")'

# failed transfers with the reason
docker compose logs --no-log-prefix app | jq -c 'select(.msg=="transfer" and .outcome!="ok") | {time, outcome, err, bytes}'

# count per outcome
docker compose logs --no-log-prefix app | jq -r 'select(.msg=="transfer") | .outcome' | sort | uniq -c

# speed of successful transfers above 100 MB (MB/s)
docker compose logs --no-log-prefix app | jq -r 'select(.msg=="transfer" and .outcome=="ok" and .bytes>1e8) | .rate_mbs'

# upload attempts with wrong credentials and exhausted sessions
docker compose logs --no-log-prefix app | jq -c 'select(.msg=="request_rejected" and (.reason=="unauthorized" or .reason=="capacity"))'
```

`BASHT_LOG_LEVEL=debug` adds `upload_accepted`, `download_claimed`, `receiver_attached` (all with the same `id` as the final `transfer`) and the `404`s. The app only sees nginx's IP: client IPs are not in the app's logs. nginx's `stream` block has no access log.

## Monitoring

- Container status: `docker inspect -f '{{.State.Health.Status}}' $(docker compose ps -q app)`. The healthcheck runs `GET /` every 30 s.
- Signals to watch in the logs: `request_rejected` with `reason=capacity` (raise `BASHT_MAX_SESSIONS` or look for what is occupying sessions), many `unauthorized` (password attempts), `transfer` with `outcome=aborted` and `err="sender idle"` (slow or stuck senders), any event at `ERROR` level.

## Troubleshooting

| Symptom | Probable cause | What to do |
|---|---|---|
| nginx does not start, error on `tcp_congestion_control` | `tcp_bbr` module not loaded | `sudo modprobe tcp_bbr`, or `BASHT_TCP_CC=cubic` |
| `docker compose up` asks for `BASHT_IMAGE`, `BASHT_PUBLIC_URL` or `BASHT_CERTS_DIR` | required variable missing | set it in `.env` |
| `docker compose pull` fails with `denied` or `unauthorized` | private ghcr.io package | make it public, or `docker login ghcr.io` with a `read:packages` token |
| nginx does not start: `cannot load certificate` or `Permission denied` | certbot symbolic links, or files not owned by root and not readable | copy the files as regular root-owned files (deploy hook above) |
| slow upload with high RTT, fast download | sender's send buffer (Linux default 4 MiB: ~66 MB/s at 40 ms) | on the sender: `sysctl -w net.ipv4.tcp_wmem="4096 16384 33554432"` and `bbr` (README, "Network performance") |
| slow download with high RTT | receiver's receive buffer or packet loss on its path | on the receiver: `net.ipv4.tcp_rmem` with a maximum of 32 MiB |
| speed stuck below ~200–300 Mbit/s on one side only | client on Wi-Fi | measure with the client on a cable |
| `401` on upload | credentials missing or wrong | `curl -u user:password -T ...` |
| `503` on upload | `BASHT_MAX_SESSIONS` reached | check the logs; raise the limit |
| the client gets a reset after 64 connections | `limit_conn perip 64` in nginx | raise or remove it in `deploy/nginx/nginx.conf` |
| `410` on download | slug already used, finished or expired | the sender must send again |
| `404` on download | wrong slug, or server restarted | check the URL; send again |
| `400` on download | resume (`curl -C -`, `wget -c`) or a download manager with multiple connections | download from the start with the printed command |
| transfer interrupted after a sender pause | `BASHT_IDLE_TIMEOUT` (2 min) | raise it if the sender's command can stay idle for a long time (for example `tar` over many small, slow files) |
| the sender waits and the slug expires | no receiver within `BASHT_WAIT_TIMEOUT` (1 h) | raise it; it must stay below nginx's `proxy_timeout` (24 h) |
| UNREADY on the sender before the receiver opens the link | no receiver page has confirmed presence yet | share the link; Share remains available |
| Download stays waiting after sharing on WhatsApp | sender's browser in the background or body detached | return to the same sender page within `BASHT_SENDER_GRACE` and keep it in the foreground; if the download was already interrupted, send again |
| UNREADY with both pages visible | confirmations missing or expired, failed requests, or the control stream blocked by the proxy | check the `?status=1` requests and the `POST /` stream in the browser; verify the network, the proxy configuration and the running image, without sharing tokens or credentials |

## Verification and measurement scripts

| Script | What it does | Where |
|---|---|---|
| `go test -race ./...` | unit and integration tests | development |
| `scripts/e2e.sh` | e2e with curl, wget, aria2c (if installed), drops, parallel transfers, auth; locally or against a stack (`BASHT_E2E_BASE`) | development, VM |
| `scripts/invariant.py` | the source is never altered: every variant of the receiver and sender commands, hostile configurations, links in every state (run by the local e2e) | development, CI |
| `bash scripts/ui/run.sh direct` | Playwright: desktop/mobile layout, upload and download, sharing, readiness, presence and recovery | development, CI |
| `bash scripts/ui/run.sh apache` | same suite through Apache/h2c; requires Docker and free `bt-ui-*`/`bt-ui-net` names | development, CI |
| `scripts/bench.sh` | relay throughput on loopback against a two-hop baseline | development |
| `scripts/netbench.sh` | effect of congestion-control algorithm and TCP buffers with netem in Docker (requires `sudo modprobe sch_netem` and a local image with `tc`) | development |

`netbench.sh` changes the sysctls only inside the network namespaces of its containers: the host is not modified.

Node.js, npm and Chromium are required only on the machine running the browser tests, not on the production VM. Installation and test selection are described in the [README, Development](../README.md#sviluppo).
