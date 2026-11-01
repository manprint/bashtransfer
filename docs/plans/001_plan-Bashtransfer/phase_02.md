# Phase 1 — Relay core: configuration, slug, registry, streaming upload/download

Intent: the local server runs RS-1 without authentication: upload with immediate commands, download with identical sha256, second download with `410`.
Prerequisites: P0 closed with R1 branch A and R3 CONFIRMED (phase 0, `phase_01.md`). Artifacts: `go.mod` with `module bashtransfer`, `cmd/bashtransfer/main.go` placeholder.
Phase closure: P1; review by agent-1 (claude-haiku-5-5, self-review by the same model), with a high-risk review before writing `handler.go` (after `copy.go` and registry; concurrency and lifecycle).

## State and ownership contract
Read STATE.md §0–1 first for scope, ownership, recovery, checks and commit rules.
Open before edits; checkpoint at a recovery boundary; close with evidence. The worker (agent-2, implementation) executes the OPEN unit; the executing session is the only writer of STATE.md; missing design or conflicting tests → agent-1 (supervisor).

## Local design context
Plan revision: 3 (D17: 24h tombstones, terminal slugs → 410; correction of 1.1 `Claim`/`Finish`/tests and of the 1.2 410 mapping; see STATE §8). Revision 2: upload from stdin D16, RS-2 tests and e2e, prerequisite R4 from 0.3.
- D1: Go, `net/http`, no external dependencies.
- D2: in-memory relay, one receiver per slug, no files on disk.
- D3: 10-character slug from a 62-character alphabet, `crypto/rand`, rejection sampling with threshold 248 (= 62·4): byte `b` accepted only if `b < 248`, character = `alphabet[b % 62]`. Collision with a live session: up to 5 attempts, then error `ErrSlugCollision` (→ 503).
- D4: `PUT` enables full duplex (`http.ResponseController.EnableFullDuplex`), writes `200 text/plain` with the commands and flushes before reading the body. The body is read only after a receiver attaches.
- D5: file name = path segment, sanitized (basename, no control characters, `"`, `\`, max 128); default `download`. R1 branch A from 0.1 already chosen.
- D6: first claim wins; `200` with `Content-Type: application/octet-stream`, `Content-Disposition: attachment; filename="<name>"`, `Content-Length` only if known; unknown slug `404`; already claimed or concluded `410`.
- D7: a waiting session expires after `WaitTimeout`: removed from the registry; the sender receives an interruption (abort).
- D8: failure mid-relay → the receiver is aborted with `panic(http.ErrAbortHandler)` (final chunk not sent, the client sees an error). Do not use a normal `return` in this case: Go would cleanly close the truncated body.
- D13: buffers from the pool; flush after every `Write` to the receiver.
- I-1 (no disk, one buffer per relay), I-2 (one receiver per slug), I-3 (removal only once), I-4 (sha256 or error), I-5 (slugs unique among live sessions).

Abort protocol (binding for 1.1 and 1.2):
- `Session.Abort(err)`: sets `err` only on the first call, closes nothing.
- `Registry.Finish(s)`: idempotent (`sync.Once`); removes `s` from the map if it is still the registered one, stops the timer, closes `Finished()`. Called **only from the sender's goroutine, after the last write to the receiver**. Never from the receiver. This prevents the receiver from returning while the sender is still writing to its `ResponseWriter`.
- The receiver waits on `Finished()`; when its request context is cancelled it calls `Abort(ErrReceiverGone)` and keeps waiting on `Finished()`.
- The sender, if it ends without a complete relay (ctx cancelled, copy error, expiry), calls `Abort(...)` before `Finish`.

## Sub-phases

### 1.1 Config, slug and registry (pure logic, no HTTP)
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** implementation following the contract below; review by agent-1 on the correctness of `Finish`/`Abort` (I-3) before closing.
- **Files:**
  - NEW `internal/config/config.go`, `internal/config/config_test.go`.
  - NEW `internal/slug/slug.go`, `internal/slug/slug_test.go`.
  - NEW `internal/relay/registry.go`, `internal/relay/registry_test.go`.
- **Change:**
  Preconditions: P0 closed.
  Contract config: `func Load(getenv func(string) string) (Config, error)`. `Config{Listen string; PublicURL string; AuthUser, AuthPassword string; WaitTimeout time.Duration; MaxSessions int; BufSize int}`. Defaults: `:8080`, `""`, `""`, `""`, `1h`, `1000`, `262144`. Errors: `BASHT_AUTH_USER` without password or vice versa; `BASHT_WAIT_TIMEOUT` not parsable or ≤ 0; `BASHT_MAX_SESSIONS` < 1; `BASHT_BUF_SIZE` < 4096 or > 4194304. The error message names the variable.
  Contract slug: `func New() (string, error)` as D3. Exact alphabet `0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz`.
  Contract registry:
  - `type Session struct{ Slug, Name string; Length int64 }` (Length -1 if unknown) with methods `Attached() <-chan Receiver`, `Expired() <-chan struct{}`, `Finished() <-chan struct{}`, `Err() error`, `Abort(err error)`.
  - `type Receiver interface{ io.Writer; Flush() }`, declared in `registry.go`, used by 1.2.
  - `func NewRegistry(maxSessions int, waitTimeout time.Duration) *Registry` with internal field `gen func() (string, error)` (default `slug.New`, replaceable in tests).
  - `Create(name string, length int64) (*Session, error)`: `ErrCapacity` if the map is full (D11), `ErrSlugCollision` after 5 attempts; starts `time.AfterFunc(waitTimeout, expire)`.
  - `Claim(slug string, rcv Receiver) (*Session, error)`: live slug already claimed → `ErrClaimed`; live slug not claimed → marks it claimed, stops the timer, sends `rcv` on `Attached()` (buffered channel of 1, never blocking); slug not in the map but in a tombstone (D17) → `ErrGone`; otherwise `ErrNotFound`.
  - Tombstones (D17): `gone map[string]struct{}` plus a FIFO queue `[]struct{slug string; at time.Time}`; `bury(slug)` adds to the map and the queue; `prune(now)` (called at the start of `Create` and `Claim`) removes entries older than `tombstoneTTL = 24h` from the head of the queue. `Create` treats a slug in a tombstone as occupied (does not assign it).
  - `expire(s)`: if the session is still in the map and not claimed → removes it, `bury`, closes `Expired()`; otherwise no-op (idempotent).
  - `Finish(s)`: as per the abort protocol; if in the map → removes it, `bury`, stops the timer; closes `Finished()` with `sync.Once`.
  - Sentinels: `ErrNotFound`, `ErrClaimed`, `ErrGone`, `ErrCapacity`, `ErrSlugCollision`, `ErrSenderGone`, `ErrReceiverGone`, `ErrLengthMismatch` in `registry.go`.
  - `Len() int` for tests (number of sessions in the map).
  Steps:
  1. S1 — `config.Load` with validation; expected: default and error tests pass.
  2. S2 — `slug.New` with rejection sampling; expected: length 10, correct alphabet.
  3. S3 — `Registry` with a single mutex for the map and session state; `Claim` and `expire` under the same lock; `Finish` with `sync.Once`; expected: no session field accessed outside the lock except the channels.
  Recovery boundary: after S3 (complete registry logic), checkpoint in STATE §6 with the list of created files.
  Failure handling: if the concurrency test reveals a double claim, do not relax the test: fix the lock and record the failure in STATE §10 (do-not-repeat).
- **Unit tests:**
  - `internal/config`: `TestLoadDefaults` (no variables → D10 default values), `TestLoadAuthPairRequired` (user only → error naming the variable; password only → error), `TestLoadRejectsBadNumbers` (table test: timeout `abc`, `0s`; sessions `0`; buf `1024`, `8388609`). Command: `go test ./internal/config -run 'TestLoad' -v -count=1`; expected discovery: 3 tests.
  - `internal/slug`: `TestGenerateLength` (len 10), `TestGenerateAlphabet` (every character in the alphabet, 1000 samples), `TestGenerateUnique` (20000 samples, no duplicates). Command: `go test ./internal/slug -v -count=1`; discovery 3.
  - `internal/relay`: `TestRegistryClaimOnce` (two goroutines call `Claim` with two dummy receivers: exactly one `nil` error, the other `ErrClaimed`; I-2), `TestRegistryFinishOnce` (`Finish` twice, no panic, `Len()==0`, `Finished()` closed, then `Claim` on the same slug → `ErrGone`; I-3), `TestRegistryWaitTimeout` (waitTimeout 50 ms, `Expired()` closed within 2 s, then `Claim` → `ErrGone`; I-3), `TestRegistryCapacity` (max 2: third `Create` → `ErrCapacity`; after `Finish` of one, `Create` succeeds), `TestRegistrySlugCollision` (fake gen that always returns the same slug: second `Create` gets `ErrSlugCollision` after 5 attempts; I-5), `TestRegistryAbortKeepsFirst` (two `Abort` calls → `Err()` is the first). Command: `go test ./internal/relay -run 'TestRegistry' -v -count=1 -race`; discovery 6.
- **e2e tests:** N/A — no HTTP behaviour in this unit.
- **Done:** NEW files present; expected discovery reached (3 + 3 + 6 tests); `go vet ./...` exit 0; G-U1.1 pass in STATE §7 with count and review; agent-1 review of `Finish`/`Abort` in STATE §7; unit closed in STATE §4 with `uncommitted`.

### 1.2 HTTP relay: upload with immediate commands, streaming download
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** implementation of the contract of D4, D6, D8. High-risk review by agent-1 **before** implementing `handler.go` (concurrency and receiver lifecycle) and after, on the diff. Record both reviews in STATE §7.
- **Files:**
  - READ: `internal/relay/registry.go` (1.1), `internal/config/config.go` (1.1).
  - NEW `internal/relay/copy.go`, `internal/relay/copy_test.go`: `type Copier struct{ pool sync.Pool; size int }`, `func NewCopier(size int) *Copier`, `func (c *Copier) Copy(dst Receiver, src io.Reader) (int64, error)` with `io.CopyBuffer` on a pooled buffer and `dst.Flush()` after every successful `Write` (implemented as a `flushWriter` wrapper).
  - NEW `internal/server/handler.go`, `internal/server/handler_test.go`: `type Options struct{ PublicURL string; Authorize func(*http.Request) bool }` (nil = no check; 2.1 hooks it in), `func New(reg *relay.Registry, copier *relay.Copier, opts Options) http.Handler`.
  - NEW `internal/server/receiver.go`: adapter `http.ResponseWriter` → `relay.Receiver` (`Write`, `Flush` via `http.NewResponseController(w).Flush()`), plus the `started` flag.
- **Change:**
  Preconditions: 1.1 closed (`Registry`, `Session` API defined as above).
  Steps:
  1. S1 — `copy.go` (`Copier`, `flushWriter`); expected: `TestCopyFlushesEachWrite`, `TestCopyRoundTripBytes`, `TestCopyAllocsBounded` pass with `-race`. Recovery boundary: checkpoint in STATE §6.
  2. S2 — `receiver.go` (adapter with `started`, `WriteHeader`, `Write`, `Flush`); expected: compiles; no dedicated test, covered by `TestGetSecondClaim410` and `TestRelayRoundTripSHA`.
  3. S3 — `handler.go` upload branch (`PUT`): auth hook, full duplex, `Create`, command block, select on `Attached`/`Expired`/context; expected: `TestUploadReturnsCommandsBeforeBody`, `TestUploadMethodNotAllowed` pass. Recovery boundary: checkpoint after S3.
  4. S4 — `handler.go` download branch (`GET`): `Claim`, wait for `Finished`, abort; expected: `TestGetUnknown404`, `TestGetSecondClaim410`, `TestRelayRoundTripSHA` pass.
  5. S5 — abort tests `TestSenderAbortFailsReceiver`, `TestReceiverAbortCleansSession` (mechanics below); expected: pass within the indicated times. Recovery boundary: checkpoint after S5.
  Upload from stdin (D16, RS-2): `Transfer-Encoding: chunked`, `r.ContentLength == -1` accepted; no size limit; duration limited only by D15; name from the path.
  Upload contract (`PUT /` and `PUT /<name>`; other paths → 404; other methods → 405 with `Allow: PUT`):
  - (Download, see below: `ErrClaimed` and `ErrGone` → 410.)
  - If `Authorize` is non-nil and returns false → 401 with body `unauthorized` (`WWW-Authenticate` added in 2.1; the 2.1 logic reuses this point).
  - `rc := http.NewResponseController(w)`; `rc.EnableFullDuplex()` error → 500 (should not happen, R3 verified).
  - `length := r.ContentLength` (-1 if unknown; 0 accepted). `s, err := reg.Create(name, length)`: `ErrCapacity`/`ErrSlugCollision` → 503 with body `service unavailable`, no session.
  - `ok := false; defer func(){ if !ok { s.Abort(ErrSenderGone) }; reg.Finish(s) }()`. Sentinel errors `ErrSenderGone`, `ErrReceiverGone`, `ErrLengthMismatch` added to `registry.go` in this unit (WRITE).
  - Header `Content-Type: text/plain; charset=utf-8`; write the command block (format in `overview.md` § Interfaces: `curl -o "<name>" "<base>/<slug>"` and `wget -O "<name>" "<base>/<slug>"`, where `<base>` = `PublicURL` if set; otherwise scheme = value of `X-Forwarded-Proto` if it is `http` or `https` (other values ignored), otherwise `https` if `r.TLS != nil`, otherwise `http`; host = `r.Host`); `rc.Flush()`.
  - `select`:
    - `rcv := <-s.Attached()`: set receiver headers (`Content-Type: application/octet-stream`, `Content-Disposition: attachment; filename="<name>"`, `Content-Length` if `s.Length >= 0`), `WriteHeader(200)` on the receiver (through the `rcv` adapter), then `n, err := copier.Copy(rcv, r.Body)`. If `err != nil` or (`s.Length >= 0` and `n != s.Length`): `s.Abort(err)` (or `ErrLengthMismatch`) and `panic(http.ErrAbortHandler)` in the sender, which aborts the sender's connection (`curl` exits with an error). Only with a complete and valid copy: `ok = true`, normal return.
    - `<-s.Expired()`: no receiver within `WaitTimeout`: `panic(http.ErrAbortHandler)` in the sender (D7); the connection drops and `curl` exits with an error.
    - `<-r.Context().Done()`: sender gone; return (`ok` false → `Abort(ErrSenderGone)`).
  - The slug and the receiver are concluded in the sender's goroutine via `defer` (abort protocol).
  Download contract (`GET /<slug>`; path other than one segment → 404; slug invalid for alphabet/length → 404):
  - `rcv := &receiver{w: w}`; `s, err := reg.Claim(slug, rcv)`: `ErrNotFound` → 404; `ErrClaimed` or `ErrGone` → 410.
  - wait for `<-s.Finished()`; on cancellation of `r.Context()` call `s.Abort(ErrReceiverGone)` and keep waiting on `Finished()`.
  - After `Finished()`: if `s.Err() != nil`: if `rcv.started` → `panic(http.ErrAbortHandler)` (D8); otherwise `503` with text `transfer aborted`.
  - If `s.Err() == nil`: normal return (Go closes the chunked body correctly).
  Failure handling:
  - A sender that expires or has a copy error receives the aborted connection close (`panic(http.ErrAbortHandler)` in its handler), not a complete `200`: no fake success outcome (I-4).
  - Writes to the receiver that fail (client closed) → `Copy` error → `Abort` → `Finish`: no panic in the sender.
  - No `panic` except `http.ErrAbortHandler` in the receiver.
  - Copy: the buffer returns to the pool even on error (`defer c.pool.Put`).
  Recovery boundary: after `copy.go` (checkpoint in STATE §6), after `handler.go` upload, after download.
- **Unit tests:** names and oracles:
  - `internal/relay/copy_test.go`: `TestCopyFlushesEachWrite` (fake receiver counts Flush = number of Write), `TestCopyRoundTripBytes` (random 1 MiB input, identical output), `TestCopyAllocsBounded` (`testing.AllocsPerRun` on 16 MiB with a 256 KiB buffer ≤ 20 allocations; I-1).
  - `internal/server/handler_test.go` (use `httptest.NewServer`, real HTTP client over loopback):
    - `TestUploadReturnsCommandsBeforeBody`: `io.Pipe` as body; the client reads the first line of the response within 2 s **before** writing to the pipe; the line contains `curl -o "` and `wget -O "` (D4).
    - `TestRelayRoundTripSHA`: subtests with sizes 0, 1, 1 MiB, 16 MiB; sha256 of the download = sha256 of the upload; (I-4).
    - `TestGetUnknown404`: valid but non-existent slug → 404.
    - `TestGetSecondClaim410`: two GETs on the same slug; the second gets 410 (I-2).
    - `TestSenderAbortFailsReceiver`: mechanics: raw TCP connection (`net.Dial`) to the test server; send `PUT /x.bin HTTP/1.1` with `Content-Length: 16777216`; read the response with the commands; start `GET /<slug>` in a goroutine; write 1 MiB and close the connection. Oracle: the receiver gets fewer than 16 MiB **and** `io.ReadAll` returns a non-`nil` error (`io.ErrUnexpectedEOF` or a network error), never `nil` (I-4).
    - `TestReceiverAbortCleansSession`: mechanics: the sender writes from `io.Pipe` in 64 KiB blocks until a write fails; the receiver reads 1 MiB then cancels its own request context. Oracle: within 5 s the sender's write fails (or the PUT request ends with an error) and `reg.Len()==0` (I-3). The registry is created by the test and passed to `server.New`.
    - `TestUploadMethodNotAllowed`: `POST /` → 405 with `Allow: PUT`.
    - `TestUploadUnknownLength`: `PUT /mydir.tgz` with `Transfer-Encoding: chunked` from `io.Pipe` (unknown length), 1 MiB then 16 MiB; sha256 of the download = sha256 of the upload; the receiver gets no `Content-Length`.
    - `TestUploadNameFromPath`: `PUT /mydir.tgz` → command block contains `curl -o "mydir.tgz"` and `wget -O "mydir.tgz"`.
    - Command: `go test ./internal/relay ./internal/server -v -count=1 -race`; discovery: 3 copy tests + 9 server tests (subtests on the sizes inside `TestRelayRoundTripSHA`).
- **e2e tests:** N/A in this unit (covered by `scripts/e2e.sh` in 1.3). Handler test coverage is complete for the HTTP contract.
- **Done:** all listed tests run and passed with `-race`, no skipped tests (`-v` shows `PASS` for each); agent-1 pre-implementation and post-diff reviews recorded in STATE §7; I-1..I-4 covered; unit closed in STATE §4 with `uncommitted`.

### 1.3 Bootstrap, shutdown and local e2e script
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** main and e2e script; agent-1 review of the diff and of the outcome of `scripts/e2e.sh`.
- **Files:**
  - WRITE `cmd/bashtransfer/main.go` (replaces the placeholder from 0.2).
  - NEW `scripts/e2e.sh` (executable, `set -euo pipefail`).
- **Change:**
  Preconditions: 1.1 and 1.2 closed.
  Contract main: loads `config.Load(os.Getenv)`; error → message on stderr and exit 1. Creates `relay.NewRegistry`, `relay.NewCopier(cfg.BufSize)`, `server.New`. `http.Server{Addr: cfg.Listen, Handler, ReadHeaderTimeout: 10 s, IdleTimeout: 120 s}`; **no** `ReadTimeout` or `WriteTimeout` (they would block the relay). Listens for SIGINT/SIGTERM; `Shutdown` with a 10 s context, then `Close`. Logs to stderr with `log/slog`.
  Contract e2e script (`scripts/e2e.sh`):
  1. creates a temporary directory (outside the repository), a random 8 MiB payload file, sha256;
  2. builds the binary in that directory (`go build -o`);
  3. starts the binary on a free port of `127.0.0.1` (`BASHT_LISTEN=127.0.0.1:<porta>`), waits for `GET /` to respond;
  4. starts the upload in the background with stdout to a file: `curl -sS -T payload.bin http://127.0.0.1:<porta>/e2e.bin > upload.out &`; waits for the first line, extracts the slug with regex `[0-9A-Za-z]{10}`;
  5. downloads with `curl -sS -o received.bin http://127.0.0.1:<porta>/<slug>`; compares sha256;
  5b. RS-2: `cat payload.bin | curl -sS -T - http://127.0.0.1:<porta>/mydir.tgz > upload2.out &` (unknown length); slug from `upload2.out`; download command with `-o "mydir.tgz"`; expected: name `mydir.tgz` in the command block, identical sha256;
  6. second download of the same slug → HTTP 410 (`curl -s -o /dev/null -w '%{http_code}'`);
  7. unknown slug → 404;
  8. terminates the binary and the upload, cleans up the temporary directory (only the one created by the script).
  The curl behaviour (R1) is already CONFIRMED in 0.1: the script must not depend on assumptions about the name in the path: the expected name is `e2e.bin` only in the `curl -o` command; the server uses the name from the path (D5).
  Steps:
  1. S1 — write `main.go`; expected: `go build ./cmd/bashtransfer` exit 0.
  2. S2 — write `scripts/e2e.sh`; expected: `bash -n scripts/e2e.sh` exit 0.
  3. S3 — run `scripts/e2e.sh`; expected: exit 0 and line `E2E OK`.
  Recovery boundary: before S3 (server process in background): record PID and temporary directory in STATE §6.
  Failure handling: if the upload does not get the slug within 10 s → STOP, record outcome `FAIL` with the server log; do not change the contract to make the script pass.
- **Unit tests:** N/A for the binary; coverage is in the 1.1/1.2 tests. Discovery check for the gate: `go test ./... -count=1 -v` lists the packages `config`, `slug`, `relay`, `server` with counts as above.
- **e2e tests:** `scripts/e2e.sh` (oracle: exit 0, identical sha256, 410 on the second download, 404 for an unknown slug). Gate G-E2E1.
- **Done:** `go build` pass; `bash -n` pass; `scripts/e2e.sh` pass with `E2E OK` recorded in STATE §7; no leftover process (`pgrep -f bashtransfer` empty); unit closed in STATE §4.

## Phase gates and closure
- Required gates: G-U1.1, G-U1.2, G-E2E1 (STATE §3), plus `go test -race -count=1 ./...` as G-P1.
- Assertions: I-1..I-5 covered; D4 verified by `TestUploadReturnsCommandsBeforeBody`; D8 verified by `TestSenderAbortFailsReceiver`.
- README obligation: the distributed behaviour is the upload/download; README not yet written. Owner: unit 4.3. P1 closure check: README absent, obligation recorded as pending on 4.3 in STATE §11.
- Every sub-phase DONE. Run G-P1 now.
- Supervisor agent-1 (claude-haiku-5-5) reviews: high-risk review of `Finish`/`Abort` and of `handler.go` (receiver lifecycle) before writing `handler.go`; P1 closure review with the list of invariants.
- P1 closure: unit `P1` in STATE §4, phase DONE in STATE §11 only if G-P1 passes with a non-zero count.
