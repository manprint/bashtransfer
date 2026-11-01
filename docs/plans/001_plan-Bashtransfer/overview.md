# Bashtransfer — Plan overview

Authored 2026-10-27 by agent-1 (claude-haiku-5-5, supervisor role). Folder: `docs/plans/001_plan-Bashtransfer/`.
Execution starts at [STATE.md](STATE.md); all live readiness and progress are there.

## Goal and reference scenario

Command-line relay service, to be deployed on a VM with Docker behind nginx (TLS terminated by nginx). The server stores no files: it receives the body of an upload and forwards it in streaming to the first download that uses the slug.

Reference scenario (RS-1):
1. Sender: `curl -T myfile.zip https://bashtransfer.mydomain.it` (local test instance: `http://127.0.0.1:8080`).
2. The server immediately replies with two text commands (curl and wget) containing a 10-character slug. The sender stays waiting.
3. Receiver: runs `curl -o "myfile.zip" "<base>/<slug>"`. The transfer starts; the received file has the same sha256 as the one sent.
4. A second download with the same slug gets `410 Gone`.

Reference scenario (RS-2, streaming from a pipe; added in revision 2 from Specifiche):
1. Sender: `sudo tar -cpzf - mydir | curl -T - https://bashtransfer.mydomain.it/mydir.tgz`. The body arrives from stdin with unknown length (`Transfer-Encoding: chunked`); the server does not save it to disk.
2. The server immediately replies with the curl and wget commands for `mydir.tgz` (name taken from the path) and the sender stays waiting while tar produces output.
3. Receiver: `curl -o "mydir.tgz" "<base>/<slug>"`; the sha256 of the received file equals that of the tar produced.

Exact form of RS-1 (from Specifiche): URL without a trailing `/` (`https://bashtransfer.mydomain.it`). Verified in R4 (0.3).

Exclusions (out of scope):
- TLS certificate management (delegated to nginx).
- Persistence of files on disk or in a database.
- Multiple recipients for the same slug.
- Push, deployment to a remote host, publishing of images. No operation towards the external network without explicit authorization.
- Multipart/form or POST uploads: PUT only.
- Rate limiting and per-IP quotas (possible future work).

Constraints: sandbox without external network access (user's CLAUDE.md); Docker and nginx must be verified locally or after authorization.

## Decisions

| ID | Decision and consequence | Authority/source | Supersedes |
|----|--------------------------|------------------|------------|
| D1 | Language **Go** (minimum toolchain pinned in 0.2, not below the version with `http.ResponseController.EnableFullDuplex`, see R3). Statically linked binary, `CGO_ENABLED=0`. The relay is network-bound; Go with `net/http` saturates the bandwidth with little code. Rust discarded: comparable performance for an I/O-bound relay, higher implementation cost for the same scope; decision is reversible. | Supervisor, user delegation ("you decide the best one") | — |
| D2 | In-memory relay: one `session` per slug; the body is read into fixed buffers and written to the receiver with a flush per block. No files on disk. One receiver per slug only. | Supervisor, from Specifiche ("relay only") | — |
| D3 | Slug: 10 characters from the alphabet `0-9A-Za-z` (62 symbols), generated with `crypto/rand` and rejection sampling (no modulo bias). On collision with a live session: regeneration up to 5 attempts, then 503. | Supervisor | — |
| D4 | Sender protocol: `PUT /` or `PUT /<name>`. The server creates the session and immediately writes the `text/plain` response with the command block (see Interfaces), using full-duplex: response written and flushed before reading the body. The body is read only when a receiver attaches. The sender receives no final outcome beyond the connection closing. | Supervisor; depends on R1 (branch A) | — |
| D5 | File name: the `<name>` segment of the path if present, otherwise `download`. Sanitization: basename only, removal of control characters, `"` and `\`, maximum 128 characters. If R1 shows that curl does not send the name in the path with `curl -T f https://host/`, branch B of R1 applies (see phase_02 and overview R1). | Supervisor; depends on R1 | — |
| D6 | Receiver: `GET /<slug>`. First claim wins (atomic operation in the registry). Response `200` with `Content-Type: application/octet-stream`, `Content-Disposition: attachment; filename="<name>"`; `Content-Length` forwarded only if the sender declared it. Unknown slug: `404`. Slug already claimed or concluded: `410`. | Supervisor | — |
| D7 | Receiver wait: if no receiver attaches within `BASHT_WAIT_TIMEOUT` (default `1h`), the session is removed and the sender's connection is closed. | Supervisor | — |
| D8 | Failures: if the sender drops during the relay, the response to the receiver is interrupted without a regular closing of the final chunk (the client sees an error, no fake success). If the receiver drops, the sender-side copy fails and the session is removed. Each session terminates only once (I-3). | Supervisor | — |
| D9 | Optional basic auth, on `PUT` only: `BASHT_AUTH_USER` and `BASHT_AUTH_PASSWORD` must both be set or both be absent (otherwise an error at startup). Comparison with `subtle.ConstantTimeCompare` on SHA-256 digests. Response `401` with `WWW-Authenticate: Basic`. `GET` download is always public with the slug. | User (Q1: PUT only), supervisor (mechanism) | — |
| D10 | Configuration only via environment variables; no configuration file. | Supervisor | — |
| D11 | Live session limit `BASHT_MAX_SESSIONS` (default `1000`); above the limit `PUT` receives `503`. | Supervisor | — |
| D12 | The server speaks plain HTTP; TLS is terminated by nginx with a `stream` block (rev 8: nginx's HTTP proxy is not used for the relay, see R2). `BASHT_PUBLIC_URL` is mandatory in compose. `X-Forwarded-Proto` and `Host` are used only for the base URL of the commands when `BASHT_PUBLIC_URL` is not set (direct use without nginx). | Supervisor | — |
| D13 | Performance: buffers from a `sync.Pool` of 256 KiB, `io.CopyBuffer`, flush per block, no compression, no allocation per chunk. Acceptance criterion: relay throughput ≥ 90% of the direct `io.CopyBuffer` baseline with the same buffer, measured on loopback on the same host. | Supervisor | — |
| D14 | Deploy: multi-stage Dockerfile, runtime `distroless/static:nonroot`, pinned image tags. `compose.yaml` with `nginx` and `app` on an internal network; `app` publishes no ports on the host. | Supervisor | — |
| D17 | Terminal slugs (revision 3): a slug that has been claimed, concluded or expired goes into a tombstone list for `24h` (constant, not configurable in this version) and receives `410` (D6, RS-1 step 4). Without tombstones `Finish` would make it `404`, contrary to D6. After 24h the slug is `404`. Memory limit: about 100 bytes per transfer in the last 24h; declared in 4.3. | Supervisor (contract correction, D6 requirement already approved) | 1.1 (Finish/Claim without tombstone); 1.1 tests `TestRegistryWaitTimeout`, `TestRegistryFinishOnce` |
| D16 | Upload from stdin (RS-2): `PUT` with `Transfer-Encoding: chunked` and unknown length (`ContentLength == -1`), same protocol as D4–D6, name from the path (`PUT /mydir.tgz`). No server-side size limit; duration is limited only by inactivity (D15). Constraint: the command block arrives before the end of stdin (R4 case B). | User (Specifiche, scenario 2), supervisor | — |

Config (D10) in full form: `BASHT_LISTEN` (default `:8080`), `BASHT_PUBLIC_URL` (default: derived from the request), `BASHT_AUTH_USER`, `BASHT_AUTH_PASSWORD`, `BASHT_WAIT_TIMEOUT` (default `1h`), `BASHT_MAX_SESSIONS` (default `1000`), `BASHT_BUF_SIZE` (default `262144`, minimum value 4096).

## Open questions

- **Q1 — resolved by the user:** auth only on `PUT` (upload with `-T`). The download `GET /<slug>` is public: the slug is the secret.
- **User authorizations:** network access for the official nginx documentation (R2) and pulling the pinned base Docker images (4.1, 4.2).

No open product question. Technical choices are recorded as D1–D15.

## Architecture

```
sender ──PUT /<name>──▶ upload handler ─┐ creates session(slug, name, clen?)  registry (map slug→session, mutex)
                          (full-duplex) │ writes commands, waits for claim  ▲
                                        │                                  │ atomic claim (1 winner)
receiver ──GET /<slug>──▶ download handler ┘ delivers its own writer ───────┘
                          │
                          └─ relay: io.CopyBuffer(writer, uploadBody) with buffers from sync.Pool, Flush per block
```

Ownership: the registry is the only point that mutates session state. The sender's goroutine owns the upload body and the copy; the receiver's goroutine waits for the end signal. The wait timeout is one goroutine per session with a `time.Timer` managed by the registry.

Session states: `waiting` → `claimed` → `done` | `aborted`. Transitions only through registry methods, each happening exactly once (I-3).

Package layout (all NEW, details in the phase file):
- `cmd/bashtransfer/main.go`: bootstrap, config reading, starting the `http.Server`.
- `internal/config/`: env parsing and validation.
- `internal/slug/`: slug generation.
- `internal/relay/`: registry, sessions, buffer pool, relay.
- `internal/server/`: HTTP handlers, auth, text response with the commands.
- `scripts/e2e.sh`, `scripts/bench.sh`.
- `Dockerfile`, `compose.yaml`, `deploy/nginx/nginx.conf` (rev 8: replaces `bashtransfer.conf`), `README.md`, `go.mod`.

## Interfaces and compatibility

| Surface | Exact name/type | Default | Errors/conflicts | Compatibility |
|---------|-----------------|---------|------------------|---------------|
| HTTP upload | `PUT /` and `PUT /<name>`, streamed body, optional `Content-Length`; from stdin without length = `Transfer-Encoding: chunked` (D16) | — | `401` auth, `503` session limit or slug collisions, `405` other methods on the upload path | new |
| HTTP upload response | `200 text/plain`, two lines: `curl -o "<name>" "<base>/<slug>"` and `wget -O "<name>" "<base>/<slug>"` | — | — | new; fixed-line format for scripts |
| HTTP download | `GET /<slug>` | — | `404` unknown slug, `410` already claimed, concluded or expired (D7) | new |
| HTTP root | `GET /` usage text | — | — | new |
| Env | see D10 | see D10 | invalid config → exit 1 at startup with a message on stderr | new |
| Binary | `bashtransfer` with no arguments; SIGINT/SIGTERM → ordered shutdown | — | — | — |

Standard errors: plain-text error bodies with one line; no internal details (stack, paths) exposed.

## Phase map

| Logical phase | File | Sub-phase IDs | Depends on | Assignment |
|---------------|------|---------------|------------|------------|
| 0 | [phase_01.md](phase_01.md) | 0.1, 0.2, 0.3 | none | agent-2 (probe, skeleton); agent-1 review |
| 1 | [phase_02.md](phase_02.md) | 1.1, 1.2, 1.3 | 0 (0.1 for R1, 0.2 for R3) | agent-2; review agent-1 |
| 2 | [phase_03.md](phase_03.md) | 2.1, 2.2 | 1 | agent-2; review agent-1 |
| 3 | [phase_04.md](phase_04.md) | 3.1 | 1 | agent-2; review agent-1 |
| 4 | [phase_05.md](phase_05.md) | 4.1, 4.2 (R2 residual and network), 4.3 docs | 2, 3 | agent-2; review agent-1 |

No live status in this table.

## Reuse map

Greenfield: the repository has no code (no commits, only an untracked `Specifiche.md`). No symbols to reuse. External references to the standard library are verified locally with `go doc` (see R3) and must not be reimplemented.

| Need | Path and symbol | Contract to preserve |
|------|-----------------|----------------------|
| Streaming HTTP | `net/http`, `http.ResponseController.EnableFullDuplex`, `http.Flusher` | D4, D6 |
| Reusable buffered copy | `io.CopyBuffer`, `sync.Pool` | D13 |
| Secure slug | `crypto/rand.Int` | D3 |
| Constant-time comparison | `crypto/subtle.ConstantTimeCompare` | D9 |

## Research and evidence

External research (B2) is limited: the sandbox cannot access the external network without explicit authorization (user rules in CLAUDE.md). Claims that depend on external sources remain UNVERIFIED. Those verifiable locally (installed curl, Go toolchain, local binaries) are verified with isolated probes outside the repository.

| ID | Question / required or optional | Evidence state | Affected decisions / units / tests |
|----|---------------------------------|----------------|------------------------------------|
| R1 | `curl -T <file> https://host/` (URL without file name): (a) does the server receive the name in the path? (b) does curl print the server's response before the end of the upload? Required. | CONFIRMED (branch A) — local probe 0.1, curl 8.18.0 | D4, D5; 0.1, 1.2, 1.3 |
| R2 | nginx directives for streaming upload/download without buffering and unlimited body (`proxy_request_buffering off`, `proxy_buffering off`, `client_max_body_size 0`, long read/write timeouts, `proxy_http_version 1.1`). Required for 4.2. | CONFIRMED for the buffering, body and HTTP version directives; `client_body_timeout` read from nginx.org (default 60s). Superseded by rev 8: the nginx HTTP proxy is replaced by TLS `stream`, so these directives are no longer in use (see ### R2). Proxy timeouts must be raised to ≥ BASHT_WAIT_TIMEOUT. | D12, D14; 4.2 unblocked, with timeout parameters |
| R3 | `http.ResponseController.EnableFullDuplex` available from the minimum Go version; behaviour on the body while the response is being written. Required for D4. | CONFIRMED — local source: `go doc` on go1.27.3; introduced in go1.21 (`$GOROOT/api/go1.21.txt`). `go.mod`: `go 1.21`. | D1, D4; 0.2, 1.2 |
| R4 | Two curl cases: (a) does `curl -T file https://host` without a trailing `/` (RS-1 form) produce a path with the file name? (b) does `tar ... \| curl -T - https://host/x.tgz` print the command block before the end of stdin? Required for RS-1 and RS-2 (D16). | CONFIRMED — local probe 0.3, curl 8.18.0: (a) path `/payload.bin` (branch A: name preserved); (b) response line 3 ms after `PUT_START`, stdin EOF +3 s (branch B-OK); (c) stdin without name → path `/` (limitation of D5, non-blocking) | D4, D5, D16; 0.3, 1.2, 1.3 |

### R1 — curl upload behaviour
- Sources: local `curl --help all`/`man curl` (installed version) and local probe.
- Applicability: curl version installed in the sandbox; client behaviour may vary between versions.
- Dates: not applicable (local verification).
- Documented fact: none yet.
- Supervisor inference: the project must work with the standard `curl` on the sender's machine; the sandbox version is only a hint.
- Local check (0.1, curl 8.18.0, 2026-10-27): probe in a scratch directory outside the repo, server on `127.0.0.1` that sends the response as a chunk (without final chunk), waits 3 s, then reads the body (8 MiB random).
  - S3 `curl -N -T payload.bin http://127.0.0.1:<port>/`: the `COMANDI-DI-PROVA` line arrives within 2 ms of the server's response; `BODY_END` at +3 s with 8388608/8388608 bytes. Branch A.
  - Without `-N` the line on a pipe arrives only at the end of the upload: curl's stdio buffering, not the protocol. The README (4.3) must cite `curl -N` only for output on a pipe; on a terminal the line appears immediately.
  - S4: `PUT /probe-name.zip` → path `/probe-name.zip`. `PUT /` with a URL ending in `/` → curl adds the local basename: path `/payload.bin`. The name is therefore available in both cases (D5 valid).
  - First pass discarded: response with full `Content-Length`. curl closes the upload right after the response (body received 576 KiB out of 8 MiB). Does not represent D4.
  - Implementation constraint (D4): the response to `PUT` must stay open (no final chunk) until the end of the relay. Closing the response before reading the body makes the sender's upload abort.
- Outcome: **branch A**. Decision impact: D4 and D5 apply as written. Branch B (curl prints only at the end of the upload): **stop** and escalate to the user, because the sender would not see the commands before the relay; no substitute technical choice is authorized without a decision. Branch C (path without name): D5 uses `download` as the default name and the limitation is declared, or the user chooses an alternative (e.g. `curl -T f -H` — not in the spec). Escalation in any case.
- Unresolved/replacement: none after 0.1.

### R2 — nginx streaming directives
- Sources: https://nginx.org/en/docs/http/ngx_http_proxy_module.html and https://nginx.org/en/docs/http/ngx_http_core_module.html (fetched 2026-10-27, authorized by the user; indexed with context-mode).
- Applicability: current nginx.org documentation, not tied to a pinned version. The image version must be pinned in 4.2 and compared with the release notes (`proxy_request_buffering` exists since 1.7.11).
- Documented facts (confirmed):
  - `proxy_request_buffering off`: the body is forwarded to the upstream as soon as it arrives (default `on`, introduced in 1.7.11). With HTTP/1.1 towards the upstream (default `proxy_http_version 1.1`) the option is respected.
  - `proxy_buffering off`: the response is passed to the client without buffering; nginx reads at most `proxy_buffer_size` at a time. The header `X-Accel-Buffering: no` has the same effect but must not be used as a substitute.
  - `proxy_http_version 1.1` is the default.
  - `client_max_body_size 0` disables the body size check (default `1m`).
  - `proxy_read_timeout` and `proxy_send_timeout` default `60s`; they apply between two consecutive read/write operations, not to the whole transmission.
- Supervisor inference (not guaranteed by the source): while the receiver is waiting, the app does not read the body and emits no bytes after the commands. So nginx sees a stalled upstream: `proxy_send_timeout` closes the connection to the app after 60s of blocked writing, and `proxy_read_timeout` closes it after 60s without response bytes. Both must be ≥ `BASHT_WAIT_TIMEOUT` (default 1h = 3600s). For the relay in progress, the sender's silence is limited by `BASHT_IDLE_TIMEOUT` (D15), which must be < `proxy_read_timeout`.
- Residual closed (rev 7): `client_body_timeout` default `60s`, http/server/location context, applies between two consecutive body reads, not to the whole transmission (source: nginx.org core module, read in 4.2). Choice: `client_body_timeout 3600s` in the server on port 443, so nginx does not truncate an idle sender before `BASHT_IDLE_TIMEOUT`.
- Local check 4.2: `nginx -t` passes in the `nginx:1.27-alpine` image pinned by digest.
- Rev 8 (B-NGX resolved): the nginx HTTP proxy (`proxy_pass` in `http`) interrupts the upload body when the upstream has already responded (paired tests: upstream that responds before the body is read → stall at 3.9 MB, EOF; upstream that responds after → 8 MiB in 3 s). D4 requires the response before the body, so R2 is not enough. Choice: TLS and forwarding in `stream` (TCP) on 443; `http` only for the 80→443 redirect. Verified: `stream` with the real binary 8 MiB and 256 MiB (external e2e); compose stack on 443 with 256 MiB (RS-1 and RS-2, identical sha256, 2.3 s); redirect 80 → 443 and `GET /` via HTTPS.
- Decision impact (rev 8, supersedes rev 7 for the http block): `BASHT_PUBLIC_URL` mandatory in compose (nginx stream adds no HTTP headers, so no `X-Forwarded-Proto`). External e2e checks that the commands use the test base. Original decision impact (SUPERSEDED by rev 8, no longer applied): 4.2 unblocked; the `proxy_*` and `client_max_body_size` parameters from this point are not used in the current configuration. nginx parameters: `client_max_body_size 0`, `proxy_request_buffering off`, `proxy_buffering off`, `proxy_http_version 1.1`, `proxy_read_timeout` and `proxy_send_timeout` = 3600s (≥ default `BASHT_WAIT_TIMEOUT`); `proxy_read_timeout` > `BASHT_IDLE_TIMEOUT`. Reversible choice; the alternative (shorter timeouts) rejected because it breaks D7.
- Unresolved/replacement: forwarding of the client's chunked body (RS-2) with `proxy_request_buffering off` must be verified in the same source. `client_body_timeout` (default 60s, between two consecutive body reads from the client) not yet read: to be verified in 4.2 with the same source before declaring the configuration correct. One point stays open: if nginx does not read the body while the app waits, `client_body_timeout` may fire on the sender; to be verified with a slow e2e test (upload waiting > 60s before the download).

### R3 — Go full duplex
- Sources: `go doc net/http.ResponseController.EnableFullDuplex` on the installed toolchain; `$GOROOT/api/go1.21.txt` line 356 (`pkg net/http, method (*ResponseController) EnableFullDuplex() error #57786`). Local source, no web search.
- Local check (0.2, 2026-10-27): `go version` = `go1.27.3 linux/amd64`; `go doc` prints the signature `func (c *ResponseController) EnableFullDuplex() error` and the description (HTTP/1: reading the body interleaved with writing the response). `go vet ./...` exit 0; `go list ./...` = `bashtransfer/cmd/bashtransfer`.
- Decision impact: the method exists since go1.21; `go.mod` declares `go 1.21` (minimum). D4 valid. The builder image in 4.1 uses a toolchain ≥ 1.21 (the pinned choice must be recorded in 4.1). Not a web search.

### R4 — curl upload without trailing `/` and from stdin
- Sources: local probe with installed curl (version recorded in 0.3), outside the repo.
- Applicability: sandbox curl; the real sender's behaviour may vary.
- Documented fact: none yet.
- Local check (0.3, curl 8.18.0, 2026-10-27; probe server in scratch `r4/probe_server.py` on `127.0.0.1:18090`, closed after the probe, no residual listener; log in `scratchpad/r4/server.log`):
  - (A) `curl -N -T r1/payload.bin http://127.0.0.1:18090` (URL without `/`, 8388608 bytes, `CL=8388608`): `PUT_START path=/payload.bin`; `BODY_END bytes=8388608`. The name is in the path even without a trailing slash.
  - (B) `{ printf x; sleep 3; head -c 8388608 /dev/urandom; } | curl -N -T - http://127.0.0.1:18090/mydir.tgz`: `PUT_START path=/mydir.tgz CL=- TE=chunked` at .714694; `COMANDI-DI-PROVA` line visible at .717428 (+3 ms); stdin EOF at .731 (+3 s after the line); `BODY_END bytes=8388609` (1 byte `x` + 8388608). No `Content-Length`.
  - (C) `head -c 1024 /dev/urandom | curl -N -T - http://127.0.0.1:18090`: `PUT_START path=/` (no name). Response visible; `BODY_END bytes=1024`.
  - Observation: curl sends `Expect: 100-continue` in all cases; the server does not reply 100 and curl proceeds anyway (no block observed).
- Decision impact: (A) path with basename → RS-1 unchanged, D5 valid. (B) response visible before EOF → D16 valid, no escalation. (C) stdin without name: D5 uses `download`; for RS-2 the name is always in the path (`/mydir.tgz`), so non-blocking. Limitation to document in 4.3: `tar ... | curl -T - https://host` without a name produces `download`.
- Outcome: **branch A/B-OK/C-limit** → R4 CONFIRMED, no escalation. Unresolved/replacement: none.

## Invariants

| ID | Meaning | Guarding test |
|----|---------|---------------|
| I-1 | The server does not write the body to disk and uses at most one buffer per active relay. | `TestRelayUsesPooledBuffer` (1.2); code review in 1.2 and P1 |
| I-2 | For a slug there is at most one receiver; every other claim receives `410`. | `TestRegistryClaimOnce`, `TestGetSecondClaim410` (1.1, 1.2) |
| I-3 | Every session transitions to a terminal state (`done`/`aborted`) and is removed from the registry exactly once. | `TestRegistryRemoveOnce`, `TestReceiverAbortCleansSession` (1.1, 1.2) |
| I-4 | The received byte stream is identical to the one sent, or the receiver sees a transfer error. Never silent truncation. | `TestRelayRoundTripSHA` with sha256 (1.2); `TestSenderAbortFailsReceiver` (1.2) |
| I-5 | Live slugs are unique; the slug has 10 characters from the D3 alphabet. | `TestGenerateLength`, `TestGenerateUnique` (1.1) |

## Risks

| Risk | Design mitigation | Verification |
|------|-------------------|--------------|
| curl does not show the response before the end of the upload (R1-B) | Stop and escalate, no relay implementation before the outcome of 0.1 | outcome recorded in overview R1 |
| Goroutines and connections left open by abandoned senders | D7 timeout, I-3, `http.Request.Context` on every wait | `TestRegistryWaitTimeout`, `TestSenderAbortFailsReceiver` |
| Many senders waiting consume resources (public instance) | D11 session limit; each session uses a goroutine and no buffer until a receiver is present | `TestRegistryMaxSessions` |
| nginx buffering cancels streaming | R2, 4.2 cannot be closed until the config is verified; regression test with nginx in compose | 4.2 and G-P4 |
| Anonymous public upload abused | D9 optional auth; Q1; rate limiting out of scope | documented in 4.3 |
| Filename with dangerous characters in the header | D5 sanitization | `TestFilenameSanitize` (2.2) |

## Verification strategy

- Baseline (G-BASE): repository without code; record `git status --short` and `go version` before the first change.
- Unit (per sub-phase): only the tests of the modified package, with name and expected count in STATE §3.
- Phase closure P<N>: `go test -race -count=1 ./...` with a non-zero test count and the phase's e2e tests (`scripts/e2e.sh`).
- Benchmark in 3.1 and P3: `scripts/bench.sh` on loopback; verdict according to D13.
- Final (G-FINAL, last phase): RS-1 and RS-2 run on a local instance via `compose.yaml` and nginx, after network authorization for pulling the images (4.1–4.2).

The exact commands are in STATE.md §3 (gate register), not repeated in the phases.

## Model-assignment summary

Roster: `agent-1:claude-haiku-5-5,agent-2:claude-haiku-5-5` (explicit user override: Haiku 5.5 only, no Sonnet or Opus).

| Unit(s) | Implementer | Required supervisor review |
|---------|-------------|----------------------------|
| 0.1, 0.2, 0.3 | agent-2 (claude-haiku-5-5) | agent-1 (claude-haiku-5-5), after 0.1: verification of the R1 outcome and chosen branch |
| 1.1, 1.2, 1.3 | agent-2 | agent-1, high-risk review before writing `handler.go` (concurrency and lifecycle) and closure of P1 |
| 2.1, 2.2 | agent-2 | agent-1 on 2.1 (auth contract) and P2 |
| 3.1 | agent-2 | agent-1 on the D13 criterion and P3 |
| 4.1, 4.2, 4.3 | agent-2 | agent-1 on 4.2 (directives) and P4 |

Declared limitation: with a single model, the "supervisor review" is a **self-review by another agent of the same model**. It is not a review by a stronger model. Each review record in STATE.md states this limit and must not be presented as a strong approval.
