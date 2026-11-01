# Architecture

bashtransfer is a streaming HTTP relay: the body of a `PUT` (sender) is copied into the download response (receiver) as it arrives. It does not keep files on disk or in a RAM store: it keeps only metadata and bounded buffers in transit. It supports HTTP/1.1 and unencrypted HTTP/2 towards the backend (h2c, used by the Apache proxy). This document describes how the code works; usage, configuration and logs are in the [README](../README.md), deployment in [operazioni.md](operazioni.md), and guidance for agents in [AGENTS.md](../AGENTS.md).

## Components

```
client ──TLS──▶ nginx (stream, :443) ──TCP──▶ app (:8080, Go net/http)
                                               ├─ server.handler   upload / download / usage / reserve / cancel
                                               ├─ server.pages     HTML pages (home, download, messages)
                                               ├─ relay.Registry   sessions, slugs, tombstones
                                               └─ relay.Copier     copy with reusable buffers
```

| Package | File | Responsibility |
|---|---|---|
| `cmd/bashtransfer` | `main.go` | configuration, logger, `http.Server`, signals and graceful shutdown, `-healthcheck` |
| `internal/config` | `config.go` | reading and validating the `BASHT_*` variables |
| `internal/slug` | `slug.go` | 10-character slugs `[0-9A-Za-z]` from `crypto/rand`, without modulo bias (~59 bits) |
| `internal/relay` | `registry.go` | session state: the only place that mutates it, under a mutex |
| `internal/relay` | `copy.go` | `Copier` (buffer pool, flush per write), `IdleReader` (idle timeout and sender interruption) |
| `internal/server` | `handler.go` | routing, upload, download, `Range` handling, reservation and cancellation of browser uploads, logging |
| `internal/server` | `pages.go`, `pages/*.html` | pages for browsers (home with the upload form, download page, error messages), embedded with `embed`, CSP with no external resources |
| `internal/server` | `receiver.go` | adapter for the receiver's response: headers, write deadline |
| `internal/server` | `peer*.go` | detection of a waiting sender going away (Linux, `ppoll` with `POLLRDHUP`) |
| `internal/server` | `name.go`, `auth.go` | file name sanitization, constant-time Basic auth |

nginx uses the `stream` module (TCP proxy with TLS), not the HTTP proxy: the app answers the `PUT` with the commands before reading the body, and nginx's HTTP proxy would interrupt the upload in that case.

## Frontend

The frontend uses vanilla HTML, CSS and JavaScript, with no framework, bundler or Node.js service in production. `pages.go` loads the files in `internal/server/pages/` with `go:embed` and renders them with `html/template`, escaping the server's data:

- `layout.html`: common structure, responsive CSS, color variables and copy script;
- `home.html`: file selection, sharing, terminal commands and ZIP, sender status and progress;
- `download.html`: file information, connection confirmation and download form;
- `message.html`: unknown, used, expired and other link errors.

Scripts, styles and the icon are inline; the CSP forbids external resources and limits forms and JavaScript requests to the required destinations. The theme follows `prefers-color-scheme`; roles and status labels reuse the same style and shared colors. The browser uses `fetch` for checks and polling, `XMLHttpRequest` for the body, and an NDJSON stream reader for events and outcome. Web Share shares only the link, with a fallback to copying.

Rendering produces ordinary HTML pages, not a SPA with a client-side router. A template change requires a new build and restarting the app. Node.js and Playwright belong exclusively to the browser suite in `scripts/ui/`.

## Session lifecycle

```
            PUT                        GET (Claim)                    copy finished
  ────────────▶  waiting  ─────────────────────────▶  relaying  ─────────────────▶  finished
                   │  │                                  │                          (tombstone 410)
   WAIT_TIMEOUT or │  │ the sender closes                │ error (sender, receiver,
   server shutdown │  │ (POLLRDHUP, every 1 s)           │ idle, length)
                   ▼  ▼                                  ▼
               expired / sender_gone                  aborted
               (tombstone 410)                        (tombstone 410)
```

1. **Upload** (`handler.upload`): authorization, `EnableFullDuplex` (response before reading the body), `Registry.Create` (unique slug, wait timer), `100 Continue` if requested, the command table (curl, wget, aria2c, direct), flush. Then the handler waits in `select`:
   - `Attached()`: a receiver has claimed the slug, the copy starts;
   - `Expired()`: wait timer expired or server shutdown;
   - `r.Context().Done()`: connection closed by net/http;
   - sender check ticker (Linux only): `peerClosed` sees FIN or RST even when body bytes remain unread in the kernel buffer.
2. **Download** (`handler.download`): valid slug, no `Range` starting from a byte > 0, `Registry.Claim` (succeeds only once). The receiver's handler writes nothing: it hands its own `ResponseWriter` to the sender through the `attached` channel and waits for `Finished()`. If the receiver's client leaves, it records `ErrReceiverGone` with `Abort` and keeps waiting for `Finished()`: the `ResponseWriter` stays valid as long as the sender uses it.
3. **Relay**: the sender's goroutine writes headers and body into the receiver's response (`responseReceiver`), flushing after each write. With a known length the response has `Content-Length`; otherwise it is chunked.
4. **End**: the sender always calls `Registry.Finish` (defer), which removes the session, leaves a tombstone (410 for 24 hours, at most 100000) and closes `Finished()`. The receiver, unblocked, ends its response; if the session has an error and the response has started, it aborts the connection without a final chunk (`panic(http.ErrAbortHandler)`), so the client sees an error instead of a truncated file reported as successful. At the end of its response, the sender receives the line `OK` (all bytes written to the receiver) or `ERRORE` (the fixed Italian marker, kept unchanged for compatibility: bytes delivered and the reason, or expiry without a receiver). On error, over HTTP/1 the sender's response is closed for writing, waits `abortLinger` (500 ms, so curl can read the line), and is then interrupted, so curl exits with code 18; over HTTP/2 the stream ends normally (a reset would make Apache add its own 502 page).

### Sending from the browser

The page cannot use the `PUT` response the way curl does: over HTTP/1 the browser reads it only after sending the whole body, and the response to an interrupted transfer is lost when the connection is reset. The upload therefore uses two requests:

1. **Reservation** (`handler.reserve`, `POST /` with JSON `{name, size}`): `Registry.Reserve` creates a session with a token (128 bits) and a 30-second attach timer, with no sender yet. The response is an NDJSON stream that stays open: first line link, token and commands; then `waiting`, `attached`, `progress` (bytes delivered, `Session.Delivered`, every 500 ms) and the outcome (`Session.Result`). The stream writes at least one line per second: this is also how the server notices that the page has closed (behind Apache, only after a few failed writes).
2. **Body** (`handler.upload` with `X-Bashtransfer-Token`): `Registry.BindDetachable` binds the `PUT` to the reservation with a constant-time token comparison; the attachment can be replaced before the relay, not during the copy. From there the flow is the same as curl's. The length must be the reserved one: the receiver already sees it in `Content-Length`. The browser starts the `PUT` immediately, but the server does not read the file before the receiver's claim.

The session ends with the relay outcome, `DELETE /<slug>` with the token (Cancel or the page really closing), expiry of the wait, or no `PUT` within 30 seconds. With `BASHT_SENDER_GRACE` (default 120 seconds), a sender absent before the relay keeps the session for a reattach: only metadata is kept, never the file.

The UI negotiates `foreground` in the reservation and sends small presence confirmations every five seconds (`POST /<slug>`, authenticated by the token). Moving to the background, or not confirming presence for 15 seconds, detaches the waiting body and starts the grace period. The progress stream is independent: losing it alone does not interrupt a healthy upload. Clients without presence keep the previous stream behavior.

On returning to the foreground, `Registry.Resume` with `rebind` detaches the old body before the relay; `BeginRelay` makes the start of the copy and the sender replacement atomic. If the relay has already started, the resumed stream reports `started` and the file is not sent again. Byte resumption after a body drop is not supported.

The receiver reads `GET /<slug>?status=1` without claiming the session. The UI enables Download only after a recent connection confirmation; `ClaimReady` rechecks the sender's availability under lock, leaving the link intact if the sender is absent. Terminal receivers can still claim a session and wait for the sender.

A link opened in a browser (`Accept: text/html`) or by a preview program (User-Agent of chat, mail or Office apps) returns the download page (`handler.landing`, `Registry.Peek`: does not claim the slug). The page's button sends a `POST /<slug>`, handled like curl's `GET`.

### Peer readiness

READY does not mean a file has been uploaded to the server. `Session.PeerReady` uses a single predicate under `Registry.mu`: session still alive and error-free, sender available, and, before the claim, a recent presence from the receiver's page. Between claim and `BeginRelay` the session is UNREADY; after the start, the data connection confirms the receiver. The sender's availability remains required during the copy too.

The receiver's page sends `GET /<slug>?status=1` with `X-Bashtransfer-Readiness: 1` every two seconds. `ObserveReceiver` only updates a presence timestamp, valid for five seconds, without claiming or reading the file. The response includes `ready` and `ready_ttl`; a claimed but live session may be `starting` or `active` (`200`). The check without the header keeps the previous protocol: `ready`/`away` before the claim, `claimed` with `410` after. The legacy `ready` state alone indicates the sender's availability, not that both peers are attached.

The sender negotiates `readiness: true` in the reservation and in the resume. The NDJSON stream sends the initial value, the readiness transitions and heartbeats; `attached` then includes the current confirmation. Without negotiation, the events for older clients stay unchanged. `attached` signals the actual start of the relay, not merely the `PUT` being attached.

Both UIs start UNREADY and let local confirmations expire. The receiver's page accepts confirmations only from successful status responses and invalidates stale requests when visibility changes, the network is lost, or the download is requested. After the click it keeps monitoring the transfer, but does not re-enable the button; another visitor to an already active link stays UNREADY. Share remains usable before confirmation, to avoid a circular requirement.

The labels are conservative updates, not an instant fault detector: READY does not guarantee future connections or completion. After the end they return to UNREADY; the sender's outcome and the browser download show whether the file arrived.

## Concurrency

- **One goroutine writes to the receiver**: the sender's. The receiver's handler touches the `ResponseWriter` only after `Finished()`, whose closing establishes the happens-before ordering (including for the `started` field).
- **Session state** (`claimed`, `err`, session map, tombstones) protected by `Registry.mu`. `Slug`, `ID`, `Name`, `Length`, `Created` are immutable.
- **Channels**: `attached` (buffer 1, a single send under lock in `Claim`), `expired` (closed once under lock), `finished` (closed once with `sync.Once`), `aborted` (closed once under lock on the first `Abort`). No blocking send under lock.
- **Relay interruption**: during the copy a goroutine waits on `Aborted()`; on disconnection or Cancel, it sets past deadlines both on the read from the sender (`IdleReader.Stop`) and on the write to the receiver. Even a connected receiver that does not read is interrupted immediately, without waiting for the idle timeout. For this reason the response to the sender has `Connection: close`.
- **Deadlines**: the sender read deadline is renewed on every `Read` (`BASHT_IDLE_TIMEOUT`), the receiver write deadline is renewed on every `Write` (same value) and removed at the end of the response, because the receiver's connection may be reused (keep-alive). The attach and grace timers are tied to a generation: a callback already queued cannot end a later attachment.
- **Flush errors**: even an error in the last flush to the receiver is propagated as `receiver_gone`, rather than declaring `OK` just because `Write` had accepted the bytes into the buffer.
- **Shutdown**: `Shutdown` closes the listener and calls `Registry.ExpireWaiting` (waiting senders end with `expired`); ongoing transfers get 10 seconds, then `Close`. Compose's `stop_grace_period` (15 s) stays above that.

## HTTP status codes

| Request | Status | When |
|---|---|---|
| `GET /` | 200 | usage text (also healthcheck); browsers get the courtesy page |
| `POST /` | 200 | reservation of a browser upload: NDJSON stream until the outcome |
| `POST /` | 400, 401, 415, 503 | invalid JSON or missing `size`; auth; non-JSON `Content-Type`; capacity |
| `PUT /[name]` | 200 | commands; the response body stays open until the end of the relay |
| `PUT` | 401 | auth configured and credentials missing or wrong |
| `PUT` | 503 | `BASHT_MAX_SESSIONS` reached |
| `GET /`, `PUT` | 400 | empty `Host` or with characters the shell interprets (only without `BASHT_PUBLIC_URL`) |
| `GET /<slug>` | 200 | download (`Content-Disposition`, `Accept-Ranges: none`, `Cache-Control: no-store` headers); browsers and previews get the download page, which does not consume the slug |
| `POST /<slug>` | like `GET` | download from the page's button |
| `POST /<slug>` + token and JSON `foreground` | 204, 404 | sender presence update; wrong token or session already ended |
| `GET /<slug>?status=1` | 200, 404, 410 | non-consuming check; with the readiness header also `starting`/`active` for live claimed sessions |
| `PUT /<slug>` + token | 200, 400, 404 | body of the browser upload; length different from the reservation; wrong token or expired reservation |
| `DELETE /<slug>` + token | 204, 404 | cancels the browser upload; wrong token or session already ended |
| `GET /<slug>` | 400 | `Range` not starting from the first byte; the slug is not consumed |
| `GET /<slug>` | 404 | unknown slug or invalid syntax (browsers get a page) |
| `GET /<slug>` | 410 | slug already claimed, finished or expired (for 24 hours; browsers get a page) |
| `GET /<slug>` | 503 | transfer interrupted before the response started |
| other methods | 405 | with `Allow: GET, POST, PUT` (also `HEAD`, and `DELETE` without a token) |
| paths with multiple segments | 404 | |

## Design choices

- **Go, net/http, no external dependencies.** The relay copies with `io.CopyBuffer` and a 256 KiB buffer from `sync.Pool`; measured on par with a two-hop baseline (README, "Performance"). The real limit is TCP: see README, "Network performance".
- **Flush on every write**: the stream must reach the receiver while the sender is still sending (`tar | curl` scenario), without waiting for a buffer to fill. Each write is as large as the read from the sender (up to the copy buffer), so the cost per byte stays low.
- **One receiver, no Range.** The sender's body is read only once. Segmented parallel download (aria2 `-x N`) would require disk or unbounded RAM and would not increase speed, which is limited by the sender's upload. A resume request gets `400` (not `416`, which `curl -C -` interprets as an already complete file) and does not consume the slug.
- **Slug as a secret**: 10 characters, ~59 bits; the logs use a separate ID.
- **File name**: taken from the path and cleaned up (README, "Name limits"). It reaches the receiver only in `Content-Disposition`: the printed lines do not contain the name, so none of them points to a file to write.
- **The source is never altered**: the three printed lines do not overwrite an existing file (`curl -OJ` creates with `O_EXCL`, `wget --content-disposition` and `aria2c --allow-overwrite=false` rename). Every response that is not a download has `Content-Disposition: inline; filename="bashtransfer.txt"`, so that a `curl -OJ` on an error or on the table does not fall back to the name in the URL, which for the sender is the source. `scripts/invariant.py` checks the invariant across all command variants (README, "The source is never altered").
- **Detecting a waiting sender**: net/http does not see the client closing until the body is read; `ppoll` with `POLLRDHUP` at zero timeout sees it immediately, even with unread data. Outside Linux, only `BASHT_WAIT_TIMEOUT` remains.
- **Basic auth only on upload** (curl's `PUT` and the browser's `POST /`): constant-time comparison of the SHA-256 digests of user and password. The browser's `PUT` and `DELETE` are authorized by the reservation token.
- **Browser upload without CORS**: `POST /` accepts only `application/json`, `PUT` and `DELETE` require `X-Bashtransfer-Token`; another site cannot send them without a preflight, which the server does not authorize. The token travels in a header, not in the query string logged by proxies.
- **Download page before the file**: links pasted into chat and mail are opened by previews; with a `GET` that downloads, the preview would consume the link before the receiver.
