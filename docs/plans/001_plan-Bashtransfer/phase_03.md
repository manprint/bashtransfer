# Phase 2 — Authentication, limits and sanitization

Intent: the server rejects unauthorized uploads when auth is active, protects against abandoned sessions and dangerous file names, and the download receives the correct headers.
Prerequisites: P1 closed (`phase_02.md`). Artifacts: `internal/server/handler.go` with hook `Options.Authorize`; `internal/relay/registry.go` with `Create`, `Claim`, `expire`.
Phase closure: P2; review by agent-1 (claude-haiku-5-5, self-review by the same model).

## State and ownership contract
Read STATE.md §0–1 first for scope, ownership, recovery, checks and commit rules.
Open before edits; checkpoint at a recovery boundary; close with evidence. Unit 2.1 does not depend on open decisions: Q1 is resolved (auth only on PUT).

## Local design context
Plan revision: 1.
- D9: optional auth, pair `BASHT_AUTH_USER`/`BASHT_AUTH_PASSWORD`, only on the `PUT` (Q1 resolved). Constant-time comparison on SHA-256 digests (`subtle.ConstantTimeCompare` on the two digests). `401` response with `WWW-Authenticate: Basic realm="bashtransfer"`.
- D7: wait timeout already in the registry (1.1). The relay idle timeout is added here (see 2.2).
- D5: file name sanitization (rules in overview D5), default `download`.
- D6: receiver headers; `Content-Length` only if known.
- D8: phase 1 abort protocol unchanged.
- D11: session limit already in the registry; here the `503` code is verified.

Q1 resolved by the user: auth only on `PUT`; public download with only the slug.

## Sub-phases

### 2.1 Basic auth on the upload path (PUT only)
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** implementation; agent-1 review of the `Authorize` contract and of the timing test (no non-constant comparison).
- **Files:**
  - WRITE `internal/config/config.go` (verify the pair validation already present from 1.1; no change expected).
  - NEW `internal/server/auth.go`, `internal/server/auth_test.go`: `func BasicAuth(user, password string) func(*http.Request) bool`.
  - WRITE `cmd/bashtransfer/main.go`: wire `Options.Authorize` when the config has auth.
- **Change:**
  Preconditions: 1.2 closed. Q1 resolved by the user: auth only on `PUT` (D9). The `GET /<slug>` branch is not touched.
  Contract: `BasicAuth` computes `sha256(user)` and `sha256(password)` once at construction; for each request it reads `r.BasicAuth()`, computes the digests of the received values and compares **both** with `subtle.ConstantTimeCompare` (no early return between user and password). `ok` only if both are equal.
  Steps:
  1. S1 — `BasicAuth` with precomputed digests; expected: pure, testable function.
  2. S2 — wiring in `main.go` when `cfg.AuthUser != ""`; expected: without auth no behaviour changes (1.2 regression tests unchanged).
  3. S3 — regression test: `GET /<slug>` with auth active works without credentials; expected: same behaviour as 1.2.
  Recovery boundary: after S1 (checkpoint with created files).
  Failure handling: a test failure in 2.1 is escalated to agent-1; no dependency on 2.2.
- **Unit tests:**
  - `TestBasicAuthAcceptsCorrect`, `TestBasicAuthRejectsWrongUser`, `TestBasicAuthRejectsWrongPassword`, `TestBasicAuthRejectsMissingHeader` (401 with `WWW-Authenticate`), `TestBasicAuthConstantWork` (verifies that the code performs both comparisons: a test with a counting mock of the comparison function, or an agent-1 review of the diff recorded as evidence).
  - Server test: `TestUploadRequiresAuth` (PUT without credentials → 401, with credentials → 200 with commands).
  - Command: `go test ./internal/server -run 'TestBasicAuth|TestUploadRequiresAuth' -v -count=1 -race`; discovery: 6.
- **e2e tests:** `scripts/e2e.sh` with variables `BASHT_AUTH_USER`/`BASHT_AUTH_PASSWORD` set: upload with `-u` succeeds; upload without credentials → 401; download without credentials succeeds. Gate G-E2E2.
- **Done:** tests and e2e pass with count; agent-1 review recorded; unit closed.

### 2.2 Relay limits, name sanitization and receiver headers
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** implementation of the points below; agent-1 review of the idle timeout (lifecycle, risk of blocked goroutines).
- **Files:**
  - WRITE `internal/relay/copy.go`: idle deadline for reading the body.
  - WRITE `internal/server/handler.go`: name sanitization, receiver headers, error mapping.
  - NEW `internal/server/name.go`, `internal/server/name_test.go`: `func SanitizeName(raw string) string`.
  - WRITE `internal/config/config.go`, `config_test.go`: field `IdleTimeout time.Duration`, env `BASHT_IDLE_TIMEOUT`, default `2m`, minimum `1s`. Addition recorded as decision D15 (see below).
- **Change:**
  Preconditions: 1.2 closed.
  D15 (new supervisor decision, within the approved requirements): the relay has an idle timeout `BASHT_IDLE_TIMEOUT`: if the sender produces no bytes for this interval during the relay, the copy fails with `ErrIdle` (abort of sender and receiver, I-3, I-4). Reason: without this, a stalled sender leaves the receiver blocked on `Finished()` without limit (risk in overview § Risks). Implementation: `http.NewResponseController(w).SetReadDeadline(time.Now().Add(idle))` before each `Read` of the body on the sender side (via a `deadlineReader` wrapper that the `Copier` uses through `io.Reader`). This is not a change to the public HTTP contract.
  Sanitization contract (`SanitizeName`): 1) takes only the last element after `/` and `\`; 2) removes control characters (`< 0x20`, `0x7f`) and `"`, `\`; 3) removes `..` as a name and leading/trailing spaces; 4) truncates to 128 characters in runes; 5) if empty → `download`.
  Receiver header contract: `Content-Disposition: attachment; filename="<SanitizeName>"`; the name in the command block uses the same sanitized value (consistency: same name in commands and headers, D5).
  Error contract: `ErrIdle` → abort; `ErrSlugCollision`/`ErrCapacity` → 503; `ErrClaimed` → 410; `ErrNotFound` → 404; invalid slug path → 404 without accessing the registry.
  Steps:
  1. S1 — `SanitizeName` with a table of cases; expected: tests pass.
  2. S2 — `deadlineReader` + `ErrIdle`; expected: idle test passes within the expected time.
  3. S3 — receiver header and consistent name; expected: `TestDownloadHeaders`.
  Recovery boundary: after S2 (checkpoint).
  Failure handling: if `SetReadDeadline` is not supported by the adapter in a test, use a test adapter that implements the method, without changing the production contract.
- **Unit tests:**
  - `TestSanitizeName` (table): `../../etc/passwd` → `passwd`; `a"b\c.zip` → `abc.zip`; `\x01x` → `x`; empty string → `download`; `..` → `download`; 200 characters → 128 runes; UTF-8 name `città.zip` unchanged.
  - `TestUploadIdleTimeout`: sender sends 1 KiB, then stops; with `BASHT_IDLE_TIMEOUT=200ms`, the receiver gets an error within 3 s; registry `Len()==0`.
  - `TestDownloadHeaders`: with declared Content-Length → `Content-Length` header present; without (-1) → absent; `Content-Disposition` consistent with the name in the commands.
  - `TestConfigIdleTimeout`: default 2m, `0s` → error, `500ms` → error (minimum 1 s).
  - Command: `go test ./internal/server ./internal/config ./internal/relay -v -count=1 -race`; discovery: 4 + 1 + 1 + 1 + all phase 1 tests (no regression).
- **e2e tests:** `scripts/e2e.sh` extended with an upload named `"strano nome.zip"` (“strange name.zip”, with spaces) → download with explicit `-o`; the name in the commands is sanitized consistently. Gate G-E2E2.
- **Done:** listed tests pass with `-race`; phase 1 regression unchanged; agent-1 review recorded on D15; unit closed.

## Phase gates and closure
- Required gates: G-U2.2 (command above), G-P2 = `go test -race -count=1 ./...` with non-zero count + extended `scripts/e2e.sh` (G-E2E2).
- Assertions: D5 complete, D7 with idle timeout (D15), D9 only on PUT.
- README obligation: auth behaviour and limits to be documented in 4.3 (owner). P2 closure check: obligation pending on 4.3 in STATE §11.
- Every sub-phase DONE. If 2.1 or 2.2 fails a gate, P2 **does not** close; 3.1 (independent) may proceed.
- Supervisor agent-1 reviews: D15 and idle lifecycle; auth contract (2.1) before closure.
- Closure of P2: unit `P2` in STATE §4 only when 2.1 and 2.2 are DONE.
