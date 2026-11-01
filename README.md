# bashtransfer

HTTP relay for files, from the terminal with `curl -T` or from the web interface. The file is streamed from the sender to the receiver: nothing is stored on disk or in RAM on the server, only session metadata and bounded buffers for data in transit. The sender must stay connected until the download completes.

Other documentation:

- [docs/architettura.md](docs/architettura.md): session flow, states, concurrency, HTTP codes, design choices;
- [docs/operazioni.md](docs/operazioni.md): deployment on the VM, TCP tuning, logs and monitoring, updates, troubleshooting.
- [AGENTS.md](AGENTS.md): guide for agents working on the repository, relay invariants, frontend conventions and checks.

## Technology stack

| Part | Technology |
|---|---|
| Backend | Go, `net/http` and the standard library, no external Go dependencies |
| Frontend | HTML, CSS and vanilla JavaScript; no framework such as React, Vue or Angular |
| Rendering | Go templates (`html/template`), embedded in the binary with `go:embed` |
| Browser tests | Playwright with Chromium, run from Node.js in `scripts/ui/` |

The pages are in [`internal/server/pages/`](internal/server/pages/): `layout.html` contains the shared styles and scripts; `home.html`, `download.html` and `message.html` define the respective screens. [`internal/server/pages.go`](internal/server/pages.go) handles rendering, CSP and the public state of the link.

The JavaScript uses `fetch`, `XMLHttpRequest` and NDJSON streams for reservation, upload, progress and connection confirmations. Responsive CSS and shared variables handle mobile layout, colors and the light/dark theme; Web Share and Clipboard APIs have fallbacks. There are no bundlers, npm builds or separate frontend servers. Node.js and Playwright are used only for tests, not in production: HTML, CSS and JavaScript are distributed together with the Go binary, without external resources.

## Usage

### Scenario 1: file

Sender:

```bash
curl -T myfile.zip https://bashtransfer.mydomain.it
```

Immediate response: an ASCII table with the download lines, one per tool:

```
+-----------------------------------------------------------------------------------------------+
| bashtransfer: commands for the receiver                                                       |
+--------+--------------------------------------------------------------------------------------+
| curl   | curl -f -OJ "https://bashtransfer.mydomain.it/X9ZnTRvc5H"                            |
| wget   | wget --content-disposition "https://bashtransfer.mydomain.it/X9ZnTRvc5H"             |
| aria2c | aria2c -x1 -s1 --allow-overwrite=false "https://bashtransfer.mydomain.it/X9ZnTRvc5H" |
| direct | https://bashtransfer.mydomain.it/X9ZnTRvc5H                                          |
+--------+--------------------------------------------------------------------------------------+
| One receiver per link. Keep the terminal open until the end.                                  |
+-----------------------------------------------------------------------------------------------+
```

The commands column is as wide as the longest command. A triple-click on a line also selects the `|` borders, which the shell does not accept: to copy a command, select it from the start to the final quote, or use one of the methods in [Copying the command without selecting it](#copiare-il-comando-senza-selezionarlo).

Receiver: runs one of the three command lines, or opens the link on the `direct` line in the browser (see [Download from the browser](#download-dal-browser)). The file is downloaded while the sender uploads it and takes its original name (`myfile.zip`), which arrives in the `Content-Disposition` header: the lines do not contain the name, so none of them can point to a file to write.

When the receiver has finished, the sender reads the outcome at the bottom of the table (see [Outcome for the sender](#esito-per-il-mittente)).

The `Content-Disposition` header carries the name both in `filename*` (UTF-8 encoded, for browsers) and in `filename` with the UTF-8 bytes (for `curl -OJ`, which reads only `filename`): names with accented letters stay correct.

<a id="la-sorgente-non-viene-mai-alterata"></a>

#### The source is never altered

The link is sent to people who do not know the tool and may get the command wrong or forget a flag. What the system guarantees, without relying on instructions:

- **receiver**: the printed line, with any flag omitted or one of the common ones added and with hostile configuration files, does not modify the file or folder being sent, for a valid, used, expired or unknown link;
- **sender**: every form of `curl -T` without `-O`/`-o` (file, pipe, stdin, folders with `tar`) does not modify the source, whatever the outcome; with `-OJ` the source stays intact on `2xx`/`3xx` responses (see below for errors).

How:

- a receiver on another computer cannot touch it: the server never writes to the sender's disk and `curl -T` opens the source read-only;
- the risk exists only when the receiver works in the same folder as the sender (same machine or shared folder). There, none of the lines overwrites an existing file:
  - `curl -f -OJ` creates the file with `O_EXCL`: if it exists it exits with code 23 without touching it. With `-f` an HTTP error exits with code 22 without writing anything;
  - `wget --content-disposition` saves `myfile.zip.1`; busybox wget does not know the option and exits without writing;
  - `aria2c --allow-overwrite=false` saves `myfile.1.zip`, even with `allow-overwrite=true` in its `aria2.conf`;
  - the browser (`direct` line) does not overwrite an existing file: it saves under another name (`myfile (1).zip`). This is the default browser behavior, not verified by the tests;
- the browser upload reads the file through browser APIs, which give no write access to the chosen file;
- every response that is not a download (errors `400`/`404`/`410`, the response to the sender) has its own name, `bashtransfer.txt`: a `curl -OJ` that receives an error or the table does not fall back to the name in the URL.

[`scripts/invariant.py`](scripts/invariant.py), run by the e2e and by CI, checks this on every change. It uses as source a file with an accented name, a file called `bashtransfer.txt`, a folder and a symbolic link, and compares content, size, dates and inode before and after.

- Receiver: the printed line and every variant with flags omitted or added (`curl` without `-f`/`-O`/`-J`, `-C -`, `-L`; `wget` without `--content-disposition`, `-c`, `-N`, `-nc`; busybox `wget`; `aria2c` without `-x1`/`-s1`/`--allow-overwrite=false`, `-c`, `-x4 -s4`), with an empty configuration and with hostile `.curlrc`, `.wgetrc` and `aria2.conf`, on a valid, unknown, already used, expired link and with the sender gone.
- Sender: the common forms of `curl -T` (with and without a name in the URL, trailing `/`, `-N`, `-f`, `-J`, `-OJ`, `-L`, from a pipe and from stdin, `tar | curl` for folders) with a successful download, receiver gone, expired link, Ctrl+C, `401` and `503`.

Still uncovered; the test runs them without requiring a particular outcome:

- the receiver who uses the source name as the output (`curl -o myfile.zip`, `wget -O myfile.zip`, `> myfile.zip`) or asks to overwrite (`curl --clobber` or `clobber` in `.curlrc`, `aria2c --allow-overwrite=true`, or `allow-overwrite=true` in its `aria2.conf` with the printed line stripped of `--allow-overwrite=false`), or extracts an archive over the folder being sent;
- the sender who adds `-O` or `-o` to `curl -T`: curl writes the response into a file named after the URL, that is, the source (it creates it empty even if the response has no body). The response must contain the commands for the receiver, so the server cannot prevent this. With `-OJ` the source is protected from `2xx`/`3xx` responses; recent curl (8.18) ignores `Content-Disposition` on `4xx`/`5xx` responses and truncates the source even with a response without a body, so a `401` (wrong credentials), a `503` (server full) or a `503` from Apache during a container restart truncates it. curl 8.5 uses the header on errors too and leaves it intact. `-O` and `-OJ` are only for downloading: do not use them for uploads.

<a id="copiare-il-comando-senza-selezionarlo"></a>

#### Copying the command without selecting it

On Linux, a double-click on the URL selects all of it and a middle click pastes it immediately (primary selection, no Ctrl+C or menu). For a receiver with curl, the URL is enough: `curl -f -OJ URL`.

To have the curl command already in the clipboard, a function to put in the sender's `~/.bashrc` (`wl-copy` on Wayland, `xclip -selection clipboard` on X11):

```bash
bt() {
  curl -N -T "$1" "https://bashtransfer.mydomain.it/${2:-}" | tee /dev/tty \
    | sed -nu 's/^| curl *| \(.*[^ ]\) *|$/\1/p' | head -n 1 | wl-copy
}
```

`bt myfile.zip` prints the table and copies the curl line to the clipboard as soon as it arrives; `bt - mydir.tgz` reads from stdin. `curl -N` and `sed -u` are needed so the line arrives immediately, not at the end of the transfer.

The file name is taken from the upload path. With `curl -T myfile.zip https://host`, curl sends the local name as the path and the receiver's file is called `myfile.zip` (verified). The name `download` is used only when the sender uploads from stdin (`-T -`) without a name in the path.

### Scenario 2: stream from a command

Sender, without a file on disk:

```bash
sudo tar -cpzf - mydir | curl -N -T - https://bashtransfer.mydomain.it/mydir.tgz
```

The receiver gets `mydir.tgz`. `curl -N` disables output buffering: it is needed to see the command line immediately during a pipe upload. With a file (scenario 1) it is not needed.

Without a name in the path (`... | curl -T - https://bashtransfer.mydomain.it`) the receiver gets a file called `download`.

<a id="esito-per-il-mittente"></a>

### Outcome for the sender

When the transfer ends, the server writes to the sender, below the table, a line with the outcome. It applies to files and to pipe streams (whose length it does not know):

```
OK: download completed, 104857600 bytes (100.0 MiB) delivered to the receiver in 3.2s.
ERRORE: download NOT completed after 52428800 of 104857600 bytes: <reason>.
ERRORE: download NOT completed: no receiver connected in <duration>, the link has been closed.
```

- The `ERRORE:` line (the fixed Italian marker, kept unchanged for compatibility) is reliable: the receiver does not have the whole file (connection dropped, command interrupted, file not saved), and the sender must send it again.
- `OK` means that all bytes were delivered to the receiver's connection. For very small files (a few KB) the receiver's curl may already have read everything when it refuses to save (a file with the same name exists, disk full): in that case the error appears only on the receiver's terminal.
- With a direct connection the sender's curl exits with code 18 on error; behind Apache (HTTP/2 towards the backend) it exits with 0. The `OK`/`ERRORE` line is what counts.

### Welcome page

A `GET /` from a browser (`Accept` header with `text/html`) shows the courtesy page, in English:

- **Sender, Send a file**: uploading from the browser (see [Upload from the browser](#invio-dal-browser));
- **Sender, Send from a terminal**: `curl URL -T FILE` and the pipe variant for folders, with the Copy button, and what to expect (table, terminal left open, `OK`/`ERRORE` line);
- **Sender, Zip with password and send from a terminal**: ZIP with `-P 'PASSWORD'`, upload from a file or pipe, and TAR through a pipe to preserve permissions; extraction instructions, clearing the shell history, and warnings about password exposure and ZipCrypto limits;
- **Receiver, Receive a file**: the link in the browser (`direct` line) or the curl, wget and aria2c commands;
- **Install the tools**: commands to install curl, wget and aria2c, and zip/unzip for the Bash examples (Debian/Ubuntu, Fedora/RHEL, Arch, openSUSE, Alpine, macOS, FreeBSD; on Windows the ZIP/TAR examples require WSL);
- **Privacy and security**: nothing is stored; random, single-use links; with `BASHT_PUBLIC_URL` set to `https://`, HTTPS encryption in transit but not end-to-end, with the advice to encrypt sensitive files before sending them (see [Limits and security](#limiti-e-sicurezza)).

The pages are embedded in the binary (`internal/server/pages/`), load no external resources (strict CSP, `frame-ancestors 'none'`), follow the system's light or dark theme, and use `BASHT_PUBLIC_URL` as the base for the commands. Other clients (curl, wget, aria2c) receive the same `GET /` as plain text, with the instructions.

<a id="download-dal-browser"></a>

### Download from the browser

The link on the `direct` line, opened in a browser, shows a page with the file name and size, the **Download** button and the terminal commands. The download starts only from the button (a `POST` to the same URL):

- opening or reloading the page does not consume the link; neither do previews from chat, social, email and Office (User-Agent containing `bot`, `preview`, `slack`, `whatsapp`, `telegram`, `discord`, `outlook` and similar, even without `Accept: text/html`);
- an already used, expired or unknown link shows a page with the reason (`410` or `404`); a link whose download is already in progress says so ("Download already started");
- when the sender is not available, the page refreshes its status every two seconds and disables **Download**, without consuming the link. The sender must return to the upload page; when it is ready the button is re-enabled. Even a `POST` without JavaScript during the absence shows the waiting page without claiming the link;
- after the click the button becomes "Download requested" and a second download cannot start;
- for an upload from a pipe the size is "unknown (streamed by the sender)".

The sender and receiver pages show a green **READY** only after the server confirms it: the sender is available and the receiver page is connected (or the data connection is already active). A red **UNREADY** indicates connections that are not yet confirmed, absent or closed. The Download button stays disabled until confirmation; sharing the link, however, is possible even in UNREADY, so the receiver can connect. The receiver page's confirmation lasts five seconds and is renewed every two, without consuming the link. A missing network, a page in the background or expired confirmations bring back UNREADY; returning requires a new confirmation. Updates are not instantaneous: READY does not guarantee that the network stays available, and it does not mean the file has already been uploaded or stored on the server.

curl, wget and aria2c, which do not ask for `text/html`, receive the file directly with the `GET`. An unknown program that opens links with `Accept: */*` (for example a mail security scanner that does not identify itself) consumes the link as a download.

<a id="invio-dal-browser"></a>

### Upload from the browser

On the welcome page, below the title, a box accepts a file dragged in or chosen with the **Upload** button (one file only: several files dragged together are rejected). The browser:

1. requests the link (`POST /` with `{"name": ..., "size": ..., "foreground": true, "readiness": true}` as JSON) and shows **Share:** separate from the curl, wget and aria2c commands for the receiver. On mobile only the **Link** button remains, which opens the system's native share sheet (for example WhatsApp on Android), or **Copy link** if Web Share is missing or fails; it shares only the URL, never the file or the token. The receiver's commands are in an expandable section, closed initially on mobile and open on desktop;
2. sends the file (`PUT /<slug>`) only when the receiver connects, like `curl -T`;
3. shows the progress bar with percentage, speed, bytes delivered and estimated time, and at the end the outcome: "Done: the receiver got the whole file", or the reason (link expired, receiver disconnected, upload interrupted).

The page must be kept open until the end: if you close it, the browser asks for confirmation. **Cancel** and closing the page close the link immediately: a receiver who opens it later sees "Sender stopped" (410), and one who is downloading is interrupted. If instead the sender's network drops, the link closes when the server notices (see below and [Limits and security](#limiti-e-sicurezza)).

**Mobile sharing and grace period (`BASHT_SENDER_GRACE`, default 120 seconds).** After sharing, **return to the same browser page, where the sender is waiting, and keep it in the foreground until the end**. The link does not indicate that the server has already received the file: it remains a relay with no storage, neither on disk nor in a RAM buffer.

The page signals when it moves to the background and confirms presence every five seconds while visible. If these confirmations are missing for 15 seconds, or the upload drops, before the relay starts the sender enters the grace period: the link stays alive for `BASHT_SENDER_GRACE`. A receiver using the browser sees the wait without starting a long download request; one using the terminal can keep waiting as before.

On returning to the foreground the session is always checked, even if the browser had not yet reported a network error. Before the relay, the old `PUT` is detached by the server and replaced using the same token and the same link. During a relay that has already started, only the progress stream is recovered, without interrupting or repeating the body. The session does not depend on the progress connection: for clients that declare presence, losing only that stream does not interrupt a healthy upload. The wait timeout, the grace period and **Cancel** still limit its duration.

If the sender does not return within the grace period, the link shows "Sender stopped". An empty value uses the default; `0` disables the grace period. It **does not work** if the system kills the browser process (the reference to the chosen file is lost) or if the sender does not return to the foreground. Cancel and actually closing the page close the link immediately even with the grace period. A body drop during the download is not recoverable: the transfer is interrupted as always.

With `BASHT_AUTH_USER` set, the browser asks for username and password in its login dialog; if the user cancels, the page shows "This server requires a login to send files.".

Protocol (for client authors):

- `POST /`, `Content-Type: application/json`, body `{"name":"file.zip","size":1234}` (at most 4096 bytes; `size` required and non-negative): requires Basic auth like the terminal `PUT`. Responds `200` with an NDJSON stream (`application/x-ndjson`), one JSON line per event, open for the whole transfer:
  - the reservation: `{"slug","url","token","commands":[{"label","line"}...],"grace":S}` (`grace`, in seconds, is absent if the grace period is disabled). A stream resumed during the copy includes `"started":true` and the `"bytes"` already delivered: the file must not be sent again;
  - `{"event":"waiting"}` every second until a receiver connects;
  - `{"event":"attached"}` when the receiver connects;
  - `{"event":"progress","bytes":N}` with the bytes delivered to the receiver, every 500 ms if they changed and in any case every second;
  - the outcome, last line: `{"outcome":"ok"|"expired"|"receiver_gone"|"idle"|"length_mismatch"|"sender_gone","bytes":N,"seconds":S}`.
- `PUT /<slug>` with the header `X-Bashtransfer-Token: <token>` and a body of exactly `size` bytes: it must start within 30 seconds of the reservation. The token replaces Basic auth. It responds with the same outcome in JSON (on HTTP/1 the browser reads it only if it has sent the whole body: the reliable outcome is the stream's). Wrong token: `404`; different length: `400`.
- With `BASHT_SENDER_GRACE`: `POST /` with `{"resume":"<slug>","rebind":true}` and the `X-Bashtransfer-Token` header reopens the stream of a live reservation, with the reservation as the first line (`404` if the slug is closed, the token is wrong or the grace period is disabled). `rebind` is optional: if true, it atomically detaches the old `PUT` before the relay, so the client can resend it after the response. If `"started":true`, the existing body stays intact and only the progress is recovered;
- `foreground` is optional in the reservation, to keep existing clients compatible. Clients that declare it update presence with `POST /<slug>`, JSON `{"foreground":true|false}` and the same token header (`204`; wrong token or closed session: `404`). `false` signals absence; `true` renews presence for 15 seconds;
- `GET /<slug>?status=1` reads only the state, without claiming the link or reading any file bytes: JSON `{"state":"ready"|"away"}` (`200`), or the reason and a message (`404`/`410`). It is used by the receiver page, with caching disabled;
- The UI adds `X-Bashtransfer-Readiness: 1` to the status check: it only records a presence timestamp and also receives `ready` and `ready_ttl` (five seconds). With this header a claimed but live session returns `starting` or `active` (`200`); without it, it stays `claimed` (`410`). A page that has not requested the download cannot use the `active` state to re-enable the button. `readiness: true` in the reservation or resume enables the same initial fields and the events `{"event":"readiness","ready":true|false}` when the state changes and as heartbeat; the `attached` event then includes `ready`. Clients without this option keep the previous events;
- `DELETE /<slug>` with the same header: closes the link and interrupts the transfer (`204`; `404` if the token is wrong or the session is already closed). This is what the page does on Cancel or on close (`fetch` with `keepalive`).
- Without the grace period, closing the NDJSON stream closes the link. With the grace period, clients without presence handling keep the previous behaviour: closing the last stream before the relay puts the sender in a wait for a reconnection. For the UI with presence, the stream is independent of the body: losing the presence confirmations puts the session into the grace period even if TCP buffers prevent seeing the dropped connection right away. During the transfer the `BASHT_IDLE_TIMEOUT` limit still applies.

The token is random (128 bits), compared in constant time, and travels in a header, not in the query string, which proxies log. `POST /` requires JSON and the browser's `PUT`/`DELETE` require a custom header: another site cannot send them without a CORS preflight, which the server does not authorise.

### Name limits

The name is cleaned up: only the last segment of the path is kept, control characters (including C1) are removed, as are quotes, backslashes, line separators and Unicode bidirectional controls, leading dots are stripped (`.bashrc` becomes `bashrc`: the receiver runs the command in its directory and must not overwrite hidden files), and it is limited to 128 characters. An encoded `/` (`%2F`) counts as a separator: only the last part remains. `$`, backtick and `!` become `_`: the receiver can reuse the name in a shell line. An empty name, or `-` (stdout for many tools), becomes `download`.

### Multipart download and resume

The relay is a single stream: the sender's body passes once, in order, to a single receiver. It cannot serve byte ranges, so:

- the download responds with `Accept-Ranges: none`; `aria2c` uses a single connection (the printed line has `-x1 -s1`). More connections would not speed things up anyway: the limit is the sender's upload connection;
- a `GET` with `Range` that starts at a byte other than the first (resume with `curl -C -` or `wget -c`, extra connections from a download manager) receives `400` and does not consume the slug: the full download remains possible. For an unknown, closed or already claimed slug the response is that of a normal `GET` (`404` or `410`). `Range: bytes=0-` is served with `200` and the whole body;
- an interrupted download cannot be resumed: the sender must send it again.

## Configurazione

All variables are optional. An empty value is the same as not setting it.

| Variable | Default | Meaning |
|-----------|---------|-------------|
| `BASHT_LISTEN` | `:8080` | The server's HTTP listen address. |
| `BASHT_PUBLIC_URL` | empty | Base of the URLs in the commands: `http(s)://host[/prefix]`, without query, credentials or characters the shell interprets (validated at startup). If empty, `X-Forwarded-Proto` and `Host` are used; a `Host` with characters the shell interprets (`$`, `!`, spaces, quotes, backtick, backslash) receives `400`. Required in compose. |
| `BASHT_AUTH_USER` | empty | User for uploads. Must be set together with `BASHT_AUTH_PASSWORD`. |
| `BASHT_AUTH_PASSWORD` | empty | Password for uploads. |
| `BASHT_WAIT_TIMEOUT` | `1h` | How long the sender can wait for a receiver before the slug expires. |
| `BASHT_SENDER_GRACE` | `120` | Whole seconds (0–86400). A browser sender that is absent before the relay starts keeps the link alive for this time and can reconnect. Empty uses the default; `0` disables the grace period. It does not make an interrupted body recoverable during the download. |
| `BASHT_IDLE_TIMEOUT` | `2m` | Interrupts the relay if the sender sends no bytes for this time, or if the receiver does not accept a single write within this time (minimum `1s`). |
| `BASHT_MAX_SESSIONS` | `1000` | Maximum number of slugs active at the same time. |
| `BASHT_BUF_SIZE` | `262144` | Size of the copy buffer, between 4096 and 4194304 bytes. The 256 KiB default is the buffer with the highest absolute throughput of the relay across all four sessions. |
| `BASHT_LOG_FORMAT` | `text` (`json` in compose) | Log format on stderr: `text` (logfmt, `key=value`) or `json` (one object per line). |
| `BASHT_LOG_LEVEL` | `info` | Minimum level: `debug`, `info`, `warn`, `error`. `debug` adds each session's intermediate events and the `404` rejections. |

An invalid value stops startup with a message that names the variable.

## Authentication

If `BASHT_AUTH_USER` and `BASHT_AUTH_PASSWORD` are set, every `PUT` requires Basic auth; without credentials or with wrong credentials the response is `401` with `WWW-Authenticate`. For uploads from the browser, the credentials are needed for the reservation (`POST /`; the browser asks for them in its login dialog); the page's `PUT` and `DELETE` use the reservation's token. The download (`GET`, or `POST` from the download page's button) requires no credentials: anyone with the slug can download.

```bash
curl -u user:password -T myfile.zip https://bashtransfer.mydomain.it
```

If the variables are not set, the upload is anonymous.

## Image

The workflow `.github/workflows/image.yml` runs `gofmt`, `go vet`, `go test -race`, `scripts/e2e.sh` (with `aria2c`) and the frontend tests `scripts/ui/run.sh` (Playwright, direct servers and Apache) on every pull request and push, and publishes the image to GitHub Container Registry (`ghcr.io/<owner>/<repo>`, with the workflow token, no secret):

| Event | Published tags |
|---|---|
| push to `main` | `main`, `sha-<commit>` |
| tag `vX.Y.Z` | `X.Y.Z`, `X.Y`, `latest` |
| pull request | none (build only) |

The image is multi-architecture (`linux/amd64`, `linux/arm64`, cross-compiled without emulation), with SBOM and provenance. The first push creates a private package: to pull it without logging in, make it public from the package settings on GitHub, or run `docker login ghcr.io` on the VM with a token that has the `read:packages` permission.

Release:

```bash
git tag v1.0.0 && git push origin v1.0.0
```

## Deploy with Docker Compose

`compose.yaml` is the production stack: `app` (the relay, published image) and `nginx` (TLS, ports 80 and 443). The full procedure for the VM is in [docs/operazioni.md](docs/operazioni.md).

1. Prepare a directory with `fullchain.pem` and `privkey.pem`. The repository does not manage certificates.
2. Set the variables, for example in an untracked `.env` file next to `compose.yaml`:

   ```
   BASHT_IMAGE=ghcr.io/owner/bashtransfer:1.0.0
   BASHT_CERTS_DIR=/etc/ssl/bashtransfer
   BASHT_PUBLIC_URL=https://bashtransfer.mydomain.it
   BASHT_AUTH_USER=user
   BASHT_AUTH_PASSWORD=a-long-password
   ```

3. Start:

   ```bash
   docker compose pull
   docker compose up -d
   ```

To build the image from source instead of downloading it:

```bash
BASHT_IMAGE=bashtransfer:local docker compose -f compose.yaml -f compose.build.yaml up -d --build
```

Compose variables (in addition to the server's `BASHT_*`): `BASHT_IMAGE` (required), `BASHT_CERTS_DIR` (required), `BASHT_TCP_CC` (nginx congestion control algorithm, default `bbr`).

Networks: `frontend` exposes nginx's published ports; `backend` is `internal`, connects nginx and `app`, and has no outbound route: `app` can be reached only from nginx and cannot open connections to the Internet.

Container isolation:

- `app`: read-only filesystem, no capabilities, `no-new-privileges`, user `nonroot` (distroless image without a shell).
- `nginx`: read-only filesystem with tmpfs for the pid and temporary files, only the capabilities `CHOWN`, `SETUID`, `SETGID`, `NET_BIND_SERVICE` (the master starts as root, the workers as `nginx`), `no-new-privileges`. Without `DAC_OVERRIDE` the master reads the certificates only if they belong to root or are readable by everyone: a root-owned `privkey.pem` with mode `600` (as with certbot) works.
- Rotated logs (3 files of 10 MB per container).

The nginx configuration is in `deploy/nginx/nginx.conf` (nginx 1.30 stable, pinned by digest): automatic workers, `worker_connections 8192`, DNS resolution on every connection (the `app` container can be recreated without restarting nginx), 128k copy buffers, TCP keepalive towards the client (60 s, then 4 probes every 15 s: a vanished client frees the session in about 2 minutes) and a cap of 64 simultaneous connections per client IP (`limit_conn`; remove the two lines to disable it). TLS is terminated by the `stream` block (TCP proxy): the relay passes bytes through unchanged, without nginx's HTTP proxy, which cuts off an upload's body once the server has already responded (verified in this environment). The `http` block only serves the redirect from 80 to 443.

Because nginx adds no HTTP headers, `BASHT_PUBLIC_URL` is required in compose: the printed commands use that URL, not `Host` or `X-Forwarded-Proto`.

Without nginx the server speaks plain HTTP only: it does not perform TLS.

The image has a `HEALTHCHECK` (`/bashtransfer -healthcheck`: `GET /` on `BASHT_LISTEN`, loopback only); `nginx` starts when `app` is healthy. On shutdown nginx receives `SIGQUIT` and waits up to 30 seconds for ongoing transfers to finish (`stop_grace_period`); `app` has an internal 10-second grace period (15 in compose).

<a id="dietro-apache-mod_proxy_http2"></a>

### Behind Apache (mod_proxy_http2)

An alternative to nginx when port 443 is already served by Apache. The server accepts HTTP/1.1 and cleartext HTTP/2 (h2c) on the same port; Apache must speak h2c to it.

Apache's HTTP/1.1 proxy (`ProxyPass http://`) does not work: it sends the whole upload body before reading the response, while the server responds with the commands and reads the body only when the receiver arrives, so `curl -T` hangs without printing anything. With `h2c://` the request and the response flow together.

What to do:

1. Start the app with an image that includes h2c support (later than `sha-66a3f84`) and with `BASHT_PUBLIC_URL` equal to the public HTTPS URL. This fixes the URLs in the commands independently of the incoming `Host` and forwarded headers. The app must be reachable only from Apache on the `BASHT_LISTEN` port (a private shared Docker network, or a port published on `127.0.0.1` for Apache on the host). Do not publish port 8080 to the Internet or start nginx alongside Apache on the same public ports. An app-only Compose example and deployment checks are in [Apache deployment](docs/operazioni.md#apache-deployment).
2. Enable the modules. `proxy_http2` uses the nghttp2 library (included in the Debian/Ubuntu `apache2` packages). `mod_http2` (HTTP/2 towards clients) is not needed: if `a2enmod` enables it as a dependency, `Protocols http/1.1` in the vhost (see below) excludes it for this site:

   ```bash
   sudo a2enmod proxy proxy_http proxy_http2 headers ssl reqtimeout
   ```

3. In the global Apache configuration, not inside the vhost, reduce the banner:

   ```apache
   ServerTokens Prod
   ServerSignature Off
   ```

   These settings do not replace security updates. Check the installed Debian package revision and active modules; the `Server` banner alone does not establish whether a CVE has been patched.

4. Use this TLS vhost, adapting the hostname, backend and certificate paths:

   ```apache
   <VirtualHost *:443>
       ServerName bashtransfer.mydomain.it

       ErrorLog ${APACHE_LOG_DIR}/bashtransfer-error.log
       LogFormat "%{%Y-%m-%dT%H:%M:%S%z}t %m %>s %B %D" bashtransfer_private
       CustomLog ${APACHE_LOG_DIR}/bashtransfer-access.log bashtransfer_private

       Protocols http/1.1

       ProxyRequests     Off
       ProxyPreserveHost On
       ProxyPass         / h2c://bash-transfer:8080/ flushpackets=on timeout=3900 connectiontimeout=5 retry=0 keepalive=On
       ProxyPassReverse  / h2c://bash-transfer:8080/
       ProxyIOBufferSize 65536
       RequestHeader set X-Forwarded-Proto expr=%{REQUEST_SCHEME}
       RequestReadTimeout handshake=30 header=20-40,MinRate=500 body=0
       LimitRequestBody  0
       SetEnv no-gzip    1
       SetEnv no-brotli  1

       Header onsuccess unset Strict-Transport-Security
       Header always set Strict-Transport-Security "max-age=31536000"
       Header onsuccess unset Permissions-Policy
       Header always set Permissions-Policy "camera=(), microphone=(), geolocation=(), web-share=(self)"
       Header onsuccess unset X-Content-Type-Options
       Header always set X-Content-Type-Options "nosniff"
       Header onsuccess unset Referrer-Policy
       Header always set Referrer-Policy "no-referrer"
       Header onsuccess unset X-Frame-Options
       Header always set X-Frame-Options "DENY"

       SSLCertificateFile    /etc/letsencrypt/live/bashtransfer.mydomain.it/fullchain.pem
       SSLCertificateKeyFile /etc/letsencrypt/live/bashtransfer.mydomain.it/privkey.pem
       Include /etc/letsencrypt/options-ssl-apache.conf
       SSLProtocol -all +TLSv1.2 +TLSv1.3
   </VirtualHost>
   ```

   `Protocols http/1.1` keeps the client-to-Apache connection on HTTP/1.1: with HTTP/2 towards the client (if `mod_http2` is loaded and `Protocols` does not exclude it) the upload is limited by Apache's flow-control window and, on a network with latency, drops considerably (measured locally with 40 ms of simulated RTT: about 11 MB/s with h2, over 200 MB/s with HTTP/1.1; the download does not change). Only `proxy_http2` towards the app stays h2c. `bash-transfer` is the container name on the network shared with Apache; with the app on `127.0.0.1` use `h2c://127.0.0.1:8080/`.

   `RequestReadTimeout` bounds the initial TLS handshake and request headers, but explicitly disables the body timer and minimum rate: a waiting relay or a slow pipe must not inherit `body=20,MinRate=500`. `LimitRequestBody 0` preserves arbitrary file and stream sizes. This is a deliberate tradeoff: use bounded session limits, optional upload authentication and appropriate connection/rate controls at Apache, which sees the client IP. Do not add modules that buffer the complete upload.

   `timeout=3900` is an I/O inactivity timeout, not a maximum transfer duration. It gives margin above a one-hour `BASHT_WAIT_TIMEOUT` and a five-minute `BASHT_IDLE_TIMEOUT`; adjust it when changing those budgets. Keepalive traffic also resets it while waiting.

   The `onsuccess unset` / `always set` pairs normalize both Apache header tables, avoiding duplicate headers from the app and covering Apache-generated errors. HSTS applies only to this hostname: add neither `includeSubDomains` nor `preload` without checking their implications, and enable it only once HTTPS and certificate renewal work. The permissions policy keeps native Web Share available. `X-Frame-Options` supplements the app's `frame-ancestors 'none'`; do not overwrite the app's CSP, whose form and connection policies vary by page. Removing its `unsafe-inline` requires an application change, not an extra proxy CSP.

   `SSLProtocol` is deliberately after the Certbot include so the TLS version policy is not overwritten by that file. Check the complete configuration, including other vhosts and global includes.

5. Check and reload:

   ```bash
   sudo apache2ctl configtest && sudo systemctl reload apache2
   ```

6. If Apache responds `500` and the `error.log` shows `AH01144: No protocol handler was valid for the URL ... (scheme 'h2c')`, `mod_proxy_http2` is not loaded. Check with `apache2ctl -M | grep http2`: `proxy_http2_module` must appear. With a Debian-based Apache container, enable the modules in the image (`RUN a2enmod proxy proxy_http proxy_http2 headers ssl reqtimeout` in the Dockerfile): changes made only in a running container are lost when it is recreated. Other Apache images use their own `LoadModule` configuration.
7. Verify: `curl -T FILE https://bashtransfer.mydomain.it` must print the table immediately with the `curl`, `wget`, `aria2c`, `direct` lines; running one of them from another terminal, or opening the `direct` link in the browser, the file passes. If upload authentication is configured, add `-u USER` and let curl prompt for the password.

How it behaves:

- Over HTTP/2 the server always sends `100 Continue`: Apache strips `Expect`, and without the 100, curl (which uses it above about 1 MB) discards the body as soon as it sees the response with the commands.
- Over HTTP/2 the server does not send `Connection: close`: it would be translated into a `GOAWAY` that blocks the proxy while the body is pending.
- While the sender waits for the receiver, the server writes a `\r` byte to it every 2 seconds (invisible in the terminal: the commands end with `\n`). Apache does not notice on its own that a sender has vanished until it reads the body again; the write of that byte fails, Apache closes the stream and the slug is freed within about 4 seconds. A receiver who tries earlier gets `410` or an abort, never a corrupt file.
- The same traffic resets the proxy's `timeout`: the receiver's wait is limited only by `BASHT_WAIT_TIMEOUT` (verified with `timeout=5` and a 15-second wait). The `timeout` instead counts during the transfer: a longer network interruption closes it.
- The app sees only Apache's IP: any per-IP limits must be set in Apache.
- Responses generated by Apache (the `301` redirect of the :80 vhost, the `503` while the app is stopped during a restart) do not go through the app and do not carry the name `bashtransfer.txt` (see [The source is never altered](#la-sorgente-non-viene-mai-alterata)). Optionally, in the :443 and :80 vhosts, with the `headers` module:

  ```apache
  Header always set Content-Disposition "inline; filename=\"bashtransfer.txt\"" "expr=-z resp('Content-Disposition')"
  ```

  The condition leaves the download names intact. It protects a sender who mistakenly adds `-OJ` to the upload and, with older curl versions, from the `503`: curl 8.18 ignores the header on `4xx`/`5xx` responses (verified with Apache 2.4.59, curl 8.18 and 8.5).
- Upload from the browser: the page closes the link with an explicit `DELETE` (Cancel, closing the page), which passes immediately. If it does not go through and the browser drops the connection, Apache signals the NDJSON stream's closure to the backend only after several failed writes to the browser: the link closes in about 3.5 seconds.
- Apache's `combined` access log records the request line (including the slug, query and terminal-upload file name), authenticated user and Referer. Whoever reads a live slug can download the file. The private format above records only timestamp, method, status, response bytes and duration (`%D`, microseconds), not IP, path, query, user or headers. Apply it to the site's port-80 vhost too. Other access logs, Apache's error log, debug/trace logging and any upstream proxy need a separate review; the custom format does not redact them. Restrict and handle existing logs according to the retention policy.
- The h2c relay and web UI were verified with Apache 2.4.69 (image `httpd:2.4`) over plain HTTP towards the client (Playwright with Chromium). This is not a verification of your production vhost or Certbot include. Repeat step 7 and the [Apache deployment checks](docs/operazioni.md#apache-deployment) with your server's package and TLS.

## Log

One application event per line on stderr, in the format set by `BASHT_LOG_FORMAT`. Each line has `time`, `level` and `msg` (the event name, stable: use it to filter). **Slugs, file names, paths and credentials never appear in the app's logs**: a session is identified by `id`, 16 random hexadecimal characters independent of the slug, so whoever reads these logs cannot download. Proxy access/error logs are separate: see the private log format in [Behind Apache](#dietro-apache-mod_proxy_http2).

| Event (`msg`) | Level | When | Fields |
|---|---|---|---|
| `server_start` | INFO | startup | `listen`, `public_url`, `auth` (bool), `wait_timeout`, `idle_timeout`, `sender_grace`, `max_sessions`, `buf_size`, `log_level`, `go`, `gomaxprocs` |
| `server_listening` | INFO | port open | `addr` |
| `upload_reserved` | DEBUG | browser upload reserved (`POST /`) | `id`, `length` |
| `upload_accepted` | DEBUG | session created, commands sent; for the browser, start of the `PUT` | `id`, `length` (-1 if unknown) |
| `upload_cancelled` | DEBUG | browser upload cancelled (`DELETE`) | `id` |
| `upload_resumed` | INFO | the browser sender reopened the stream after a drop (`BASHT_SENDER_GRACE`) | `id` |
| `sender_detached` | INFO | the browser sender dropped before the receiver and the link stays alive for the grace period | `id`, `wait_s`, `grace_s` |
| `sender_grace_expired` | INFO | the grace period expired without the sender returning: the link closes | `id`, `wait_s` |
| `download_claimed` | DEBUG | a receiver claimed the slug | `id` |
| `receiver_attached` | DEBUG | the relay starts | `id`, `wait_s` |
| `transfer` | INFO, WARN if `aborted` | end of each session, once | `id`, `outcome`, `bytes`, `length` (if known), `wait_s`, `transfer_s` and `rate_mbs` (if a receiver attached), `err` (if present) |
| `request_rejected` | see below | request rejected | `method`, `status`, `reason` |
| `shutdown_start` | INFO | first SIGTERM/SIGINT | `sessions` (live), `grace` |
| `shutdown_forced` | WARN | grace period expired | `err`, `sessions` |
| `shutdown_done` | INFO | exit | `forced`, `duration_s` |
| `session_create_failed`, `claim_failed`, `full_duplex_unavailable` | ERROR | internal errors (unexpected) | `err` |
| (net/http message) | WARN | HTTP server errors (accept, malformed requests) | `component=net/http` |

`outcome` of `transfer`:

- `ok`: all bytes delivered;
- `aborted`: relay started and interrupted; `err` says why (`sender idle`, `receiver gone`, `length mismatch`, `relay interrupted`, network error on the sender's or receiver's side);
- `expired`: no receiver within `BASHT_WAIT_TIMEOUT`, or server shutdown;
- `sender_gone`: the sender dropped before a receiver attached.

`wait_s` is the receiver's wait, `transfer_s` the relay's duration, `rate_mbs` the average speed in MB/s (10^6 bytes). Durations are seconds with three decimals, numbers also in JSON.

`reason` of `request_rejected`, with its level: `unauthorized` (401, WARN), `capacity` (503, WARN: `BASHT_MAX_SESSIONS` reached), `range_not_supported` (400, INFO), `invalid_host` (400, INFO), `gone` (410, INFO: link already claimed by a receiver, page "Link already used or expired"), `gone_sender` (410, INFO: the sender dropped or cancelled before any receiver, page "Sender stopped"), `gone_expired` (410, INFO: no receiver within `BASHT_WAIT_TIMEOUT`, page "Link expired"), `not_found` (404, DEBUG: nonexistent slugs, scans; INFO for the browser's `PUT` with a wrong token or an expired reservation), `bad_request` (400: DEBUG for a malformed `POST /`, INFO for a browser `PUT` with a length different from the reserved one), `unsupported_media_type` (415, DEBUG: `POST /` not JSON), `method_not_allowed` (405, DEBUG), `internal` (500 or 503, ERROR).

Example (`json`):

```json
{"time":"2026-10-29T18:02:11.4+01:00","level":"INFO","msg":"transfer","id":"9f3c2a71d04be6a8","outcome":"ok","bytes":268435456,"length":268435456,"wait_s":4.217,"transfer_s":2.31,"rate_mbs":116.21}
```

Examples of log analysis in [docs/operazioni.md](docs/operazioni.md#log).

<a id="limiti-e-sicurezza"></a>

## Limits and security

- The slug is the secret: anyone who knows it can download while the transfer is active. Use HTTPS.
- What the server keeps: nothing on disk. The content passes through the process memory only during the relay (the copy buffer, `BASHT_BUF_SIZE`, plus the kernel's and the proxy's TCP buffers); the name, size and slug remain in memory while the session is active, and the closed slug (without the name) for 24 hours as a tombstone. The app's logs contain no slugs, names or content; the proxy's logs do, unless configured otherwise (see [Behind Apache](#dietro-apache-mod_proxy_http2)).
- Encryption: HTTPS protects the file between the clients and the proxy (nginx or Apache). It is not end-to-end encryption: the proxy decrypts, and the proxy-to-app segment (internal Docker network; cleartext h2c behind Apache) and the app's memory see the file in plaintext. Whoever controls the server while a transfer is in progress can read it; completed transfers cannot be recovered from the server. For sensitive files: encrypt them before sending (for example `gpg -c`, `age -p`, or a password-protected 7z archive) and pass the password through another channel. The welcome page says so.
- One receiver per slug. A second `GET` on the same slug receives `410`. The download page for browsers does not consume the slug: the Download button (`POST`) does.
- A closed, expired or already claimed slug responds `410` for 24 hours; after that, `404`.
- The sender must stay connected until the receiver has finished: if it drops while waiting (even after sending the whole file), the slug is freed within about one second and responds `410`. If the TCP buffers are already full of unread data, the close stays queued behind the data: the slug is freed when the sender's kernel gives up the connection (measured in this environment: about 6 minutes); behind nginx the keepalive towards the client also applies. On Linux the server checks the connection every second (`POLLRDHUP`), because net/http does not detect the drop until the body is read; on other systems the session remains until `BASHT_WAIT_TIMEOUT`.
- The relay saves nothing to disk: if the sender or the receiver drops halfway, the transfer fails and the receiver sees an error (the download is interrupted without a final chunk). If the receiver drops, the sender is interrupted immediately, even if it is not sending bytes at that moment.
- The response to the sender has `Connection: close`: the upload's connection is not reused.
- A sender idle for longer than `BASHT_IDLE_TIMEOUT` interrupts the transfer.
- With `BASHT_AUTH_USER` unset anyone can send a file, from the terminal or from the web page. The app sees only nginx's IP (stream without PROXY protocol): the only per-IP limit is `limit_conn` in nginx; there are no bandwidth quotas or rate limits.
- Tombstones are capped at 100000: under very high load a closed slug returns `404` before 24 hours.
- A receiver that stays connected without reading is interrupted by `BASHT_IDLE_TIMEOUT`.
- A server shutdown (SIGTERM) immediately closes the senders still without a receiver (the slug responds `410` until the restart, then `404`: sessions do not survive the process) and gives ongoing transfers 10 seconds, then interrupts them. A second SIGINT or SIGTERM terminates the process immediately.
- The relay does not verify integrity: the receiver should compare a hash if needed.

## Performance

Measured on loopback with `scripts/bench.sh` (8 CPUs, Intel Xeon, go1.27.3, 256 MiB transfers, median of 5 samples per session). Four sessions with the same code. Relay-to-baseline ratio over two equivalent hops, median of the four sessions: 1.10 with 64 KiB buffers, 0.99 with 256 KiB, 0.98 with 1 MiB. The variability between sessions is large: individual ratios range from 0.89 to 1.21. The D13 threshold (0.90) is met by the 256 KiB median, but a single session dropped to 0.896. On a real network the values depend on the network and have not been measured.

## Network performance

With a 1 Gbit/s link the limit is not the relay but TCP: the window must cover the bandwidth × RTT product (1 Gbit/s × 40 ms = 5 MB) and the congestion control algorithm decides how much is lost to packet loss. Each transfer has two independent TCP legs, and the slower one sets the speed:

- **download** (relay → receiver): the sending side counts, that is nginx (algorithm and `tcp_wmem`), and the receiver's `tcp_rmem`;
- **upload** (sender → relay): the sender counts (algorithm and `tcp_wmem`), which the server does not control.

`compose.yaml` sets, in nginx's network namespace (which terminates the clients' connections), `bbr`, `tcp_rmem` and `tcp_wmem` with a maximum of 32 MiB and `tcp_slow_start_after_idle=0`. On the host the `tcp_bbr` module is needed, which a container cannot load:

```bash
sudo modprobe tcp_bbr
echo tcp_bbr | sudo tee /etc/modules-load.d/bbr.conf
```

If the module is missing, the sysctl write fails and the nginx container should not start (not verified: here the module is already loaded). `BASHT_TCP_CC=cubic` switches back to cubic. No container is privileged: the `net.*` sysctls are per namespace. Buffers grow only as needed (autotuning), but a slow receiver can fill the send buffer up to its maximum: the total stays limited by the host's `net.ipv4.tcp_mem`.

Measured with `scripts/netbench.sh` (Docker, netem at 1 Gbit/s per direction, direct HTTP to the app, 10 s per point, MB/s; 117 MB/s is the line rate). Profiles: `stock` = cubic, maximum buffers 6 MiB for receive and 4 MiB for send (kernel defaults); `tuned` = the compose configuration; `host` = the development workstation (bbr, `tcp_rmem` 32 MiB, `tcp_wmem` 4 MiB).

| Leg | Relay | Client | 20 ms | 20 ms, 0.1% loss | 40 ms | 40 ms, 0.1% loss |
|---|---|---|---|---|---|---|
| download | stock | stock | 115 | 19 | 60 | 25 |
| download | tuned | stock | 117 | 115 | 88 | 83 |
| download | tuned | host | 117 | 117 | 115 | 115 |
| upload | any | stock | 118 | 21–29 | 67 | 29–38 |
| upload | any | host | 118 | 111 | 67 | 61–62 |

On the server side, `bbr` with stock buffers stops at 68 MB/s at 40 ms (limit of `tcp_wmem`); cubic with 32 MiB buffers holds at 40 ms without loss (115) but collapses with 0.1% loss (38). Only the combination holds the line rate in all cases.

On the client side, to use the full gigabit even in upload with an RTT above about 30 ms, the Linux sender must raise its send buffer (and `bbr` is advisable):

```bash
sudo sysctl -w net.ipv4.tcp_wmem="4096 16384 33554432" net.ipv4.tcp_rmem="4096 131072 33554432"
sudo sysctl -w net.ipv4.tcp_congestion_control=bbr   # requires the tcp_bbr module
```

Through the compose stack (nginx stream with TLS, Basic auth), same emulation, client with the `host` profile:

| Leg | nginx | 20 ms | 20 ms, 0.1% loss | 40 ms | 40 ms, 0.1% loss |
|---|---|---|---|---|---|
| download | stock | 116 | 30 | 67 | 26 |
| download | compose (`tuned`) | 117 | 117 | 114 | 114 |
| upload | stock | 119 | 110 | 66 | 62 |
| upload | compose (`tuned`) | 118 | 109 | 66 | 62 |

nginx's TLS is not the bottleneck: without netem, with sender and receiver on the internal network, the relay through nginx reaches about 950 MB/s (16 CPUs shared by all containers). Not measured on real networks. A Wi-Fi client is often limited by the radio before TCP.

Command used: `BASHT_NB_IMAGE=<local image with sh, curl, tc, ethtool, pkill> bash scripts/netbench.sh` (requires `sudo modprobe sch_netem` on the host).

## Verification status

- Unit and integration tests (`go test -race ./...`), including concurrency (40 parallel transfers), absence of leftover goroutines, stalled receiver, expired sender and keep-alive: verified.
- Local end-to-end script (`scripts/e2e.sh`: file and stdin, download with `wget`, sender and receiver dropping halfway, sender dropping while waiting for the receiver, parallel transfers, with and without auth, name with spaces, `direct` line, download page that does not consume the link, upload with the browser's protocol and cancellation with `DELETE`): verified, with curl 8.18 and 8.5.
- Web pages and upload from the browser with Playwright (headless Chromium), with direct connection and through Apache in h2c: layout at 1280, 390 and 320 px in light/dark theme, fallback fonts, Copy and stale or late async responses, ZIP commands with password, mobile sharing separate from the terminal commands, Web Share with simulated APIs and Copy fallback. Also verified: upload and drag-and-drop, curl/browser receivers with identical sha256, progress, outcomes, Cancel, closing the page, grace period and sender reconnection, presence in the foreground, READY/UNREADY labels, expired confirmations and late or failed status responses, single-use links, pipe and Basic auth. Losing only the stream follows the presence and grace rules; it does not always equal closing the session. Script: `scripts/ui/run.sh` (see [Development](#sviluppo)), also run by CI in both modes. Other browsers and the real behaviour of the operating system on Android/iOS are not covered by these automated tests.
- Docker image and compose configuration (`docker compose config`, starting `app` and responding to `GET /` from the internal network): verified.
- Full path through nginx (compose stack, port 443, test certificate): external `scripts/e2e.sh` with 256 MiB, auth (`BASHT_E2E_AUTH`), 20 parallel transfers, `wget`, sender and receiver dropping (including the sender waiting), identical sha256: verified. Redirect from 80 and `GET /` over HTTPS: verified. The test certificate has not been verified with a public CA.
- Auth through nginx (without credentials and with wrong credentials: `401`; with correct credentials: identical transfer; `GET` of the slug without credentials: `410` after use): verified by the external e2e with `BASHT_E2E_AUTH`.
- Image healthcheck and nginx's ordered start after `app`: verified.
- Production stack with isolation (nginx 1.30 read-only with reduced capabilities, internal `backend` network with no outbound route): external e2e with auth and 20 parallel transfers, redirect from 80, nginx stopped with a transfer in progress (waited for until the end, file intact): verified.
- Multi-architecture image: `linux/amd64` and `linux/arm64` builds verified locally and in GitHub Actions CI. The `image` workflow runs the `test` and `ui` jobs; only after they succeed does it build and publish the image to GHCR for pushes to `main` and version tags.
- `aria2c` 1.37 against the relay: download from file and from pipe with identical sha256; even with `-x4` it uses a single connection and completes.
- `aria2c` line, rejection of resume (`curl -C -`: `400`, slug still downloadable), immediate interruption of the sender when the receiver drops, closing of senders waiting at shutdown: verified by Go tests and the local `scripts/e2e.sh`, including the download with `aria2c` 1.37.
- The source is never altered (`scripts/invariant.py`, run by the local e2e and by CI): 2488 cases with curl 8.18, GNU wget 1.25, busybox wget 1.37 and aria2c 1.37; 2060 receiver cases and 178 sender cases with no altered source; the rest are the limits listed in [The source is never altered](#la-sorgente-non-viene-mai-alterata). Three deliberately faulty builds are detected: printed lines `curl -f -o "nome"` (36 cases) and `wget -nc -O "nome"` (3 cases, busybox ignores `-nc`), responses without `Content-Disposition` (16 sender cases with `-OJ`).
- Effect of the congestion control algorithm and TCP buffers: measured with `scripts/netbench.sh` and through the compose stack with netem (see "Network performance"). Compose stack with nginx's sysctls: external `scripts/e2e.sh` with 256 MiB, auth and 20 parallel transfers verified.

<a id="sviluppo"></a>

## Development

For the backend, use the Go version specified in `go.mod` and in CI. The scripts require Bash, Python 3 and curl; the e2e also uses wget, aria2c and busybox when available. The frontend tests also require Node.js, npm and Chromium; the Apache mode requires Docker.

To run the app locally:

```bash
BASHT_LISTEN=127.0.0.1:8080 go run ./cmd/bashtransfer
```

Open `http://127.0.0.1:8080/`. The templates are embedded at build time: after changing HTML, CSS or JavaScript, restart `go run` or rebuild the binary. There is no `npm run dev` or `npm run build` command.

Existing checks:

```bash
gofmt -l .                    # the output must be empty
go vet ./...
go test -race -count=1 ./...
node --check scripts/ui/ui.mjs
bash scripts/e2e.sh
# against an already running stack (BASHT_PUBLIC_URL equal to BASHT_E2E_BASE):
BASHT_E2E_BASE=https://127.0.0.1 BASHT_E2E_MB=256 bash scripts/e2e.sh
# stack with Basic auth: every PUT uses the credentials and 401s are checked
BASHT_E2E_BASE=https://127.0.0.1 BASHT_E2E_AUTH=user:password BASHT_E2E_PARALLEL=20 bash scripts/e2e.sh
# only the source invariant (the local e2e runs it at the end); fails if a tool is missing
go build -o /tmp/bt ./cmd/bashtransfer
BASHT_INV_REQUIRE=curl,wget,aria2c,busybox python3 scripts/invariant.py /tmp/bt
# frontend tests with Playwright (headless Chromium); the first time, from scripts/ui:
#   npm ci && npx playwright-core install --no-shell chromium
#   add --with-deps if the system libraries are missing
bash scripts/ui/run.sh direct   # direct servers
bash scripts/ui/run.sh apache   # through Apache 2.4 in Docker (h2c proxy)
```

`scripts/invariant.py` uses the system's curl, wget, aria2c and busybox (missing ones are skipped, except those listed in `BASHT_INV_REQUIRE`), starts four server instances on free ports (normal, short wait, with auth, with capacity 1) and writes the outcome of each case as JSON, one line per case, to the file given in `BASHT_INV_DUMP`. `BASHT_INV_JOBS` (default 8) is the number of cases run in parallel.

`scripts/ui/run.sh` builds the binary, starts four servers (normal, 3-second wait, Basic auth `u:p`, grace period `BASHT_SENDER_GRACE=8`), either directly on free ports of `127.0.0.1` or in containers behind an Apache configured as in the [Behind Apache](#dietro-apache-mod_proxy_http2) vhost (`scripts/ui/httpd.conf`, `httpd` image pinned by digest), and runs `scripts/ui/ui.mjs`. The suite covers desktop/mobile layout, copy and sharing, upload and reception, outcomes, presence and reconnection, readiness and single-use links. Chromium limits connections to the local test hosts (`EXCLUDE_HOSTS` can add explicit hosts). `BASHT_UI_OUT` keeps files and screenshots; `ONLY` (regex) selects the tests.

The Apache runner recreates containers named `bt-ui-*` and the network `bt-ui-net`: check that they are not in use by other tests or people before running it. To select the readiness and recovery tests:

```bash
ONLY='peer readiness|sender grace' bash scripts/ui/run.sh direct
```

For documentation-only changes there is no need to run the application suite. For frontend changes, update the relevant tests and also check the Apache path when requests or the connection lifecycle change. More guidance in [AGENTS.md](AGENTS.md).
