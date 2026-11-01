# Phase 0 — Local probes and Go skeleton

Intent: close the two local checks that determine the upload contract (R1 curl, R3 Go full duplex) and create the Go module skeleton verifiable with `go vet`.
Prerequisites: none.
Phase closure: P0; review by agent-1 (claude-haiku-5-5, self-review by the same model, see overview § Model-assignment summary).

## State and ownership contract
Read STATE.md §0–1 first for scope, ownership, recovery, checks and commit rules.
Open before edits; checkpoint at a recovery boundary; close with evidence. The 0.1 probe does not write to the repository: only `overview.md` (R1 section) is updated. No commits (planning and the plan do not commit).

## Local design context
Plan revision: 2 (0.3 and R4 added for RS-2; see STATE §8).
- D4: the server answers the `PUT` with the commands before reading the body. Holds only if curl shows the response during the upload (R1-A).
- D5: file name from the path `PUT /<name>`; if curl does not send the name, default `download` and escalation (R1-C).
- R1: curl upload behaviour. Outcome to be recorded in overview.md after 0.1. Branch B (response visible only at the end of the upload) = STOP and escalation to the user.
- R3: `http.ResponseController.EnableFullDuplex` must exist in the toolchain. If absent: STOP, D4 must be reviewed by the supervisor before 1.2.
- No configuration variable or endpoint is implemented in this phase.

## Sub-phases

### 0.1 Probe R1: curl behaviour when uploading to the root
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** run the probe and record the outcome in overview.md (R1). Review before choosing the branch: agent-1 (claude-haiku-5-5), checks the log and the conclusion.
- **Files:**
  - READ: `Specifiche.md` (only the reference to the `curl -T` command).
  - WRITE: `docs/plans/001_plan-Bashtransfer/overview.md`, only the R1 row and the `### R1` paragraph.
  - NEW (outside the repository): scratch directory `/tmp/claude-1000/-mnt-fabio-dati-Git-SperimentazioniAI-bashtransfer/909b046d-e29f-47a1-9fde-4c4d68b9a2b7/scratchpad/r1/` containing `probe_server.py`, `make_payload.py` (or equivalent), `probe.log`, `payload.bin`.
- **Change:**
  Preconditions: none. Expected state: repository without commits, `Specifiche.md` untracked.
  Contract: the probe must not connect to any host other than `127.0.0.1`. Free port chosen by the agent (e.g. 18080), recorded in the log.
  Scope: only a local curl behaviour test; no repository changes except overview R1.
  Steps:
  1. S1 — create a random 8 MiB `payload.bin` (e.g. `os.urandom`); expected: file of 8388608 bytes.
  2. S2 — start `probe_server.py` on `127.0.0.1:<porta>`. For each `PUT`: log, with a monotonic timestamp, the request line, the headers and the `PUT_START` event; immediately send `HTTP/1.1 200 OK` with `Content-Type: text/plain` and body `COMANDI-DI-PROVA` (probe commands), flush; wait 3 s; read the whole body, logging `BODY_END` with the total bytes; expected: log sequence `PUT_START` → response sent → `BODY_END`.
  3. S3 — run the client `curl -T payload.bin http://127.0.0.1:<porta>/` with stdout piped; record the arrival time of the first stdout line relative to `BODY_END`. Expected: `COMANDI-DI-PROVA` arrives before `BODY_END` ⇒ R1-B is excluded (R1 has at least "visible in progress"); if it arrives only after `BODY_END`, R1-B.
  4. S4 — run `curl -T payload.bin http://127.0.0.1:<porta>/probe-name.zip` and verify in the log the path `/probe-name.zip`; then, separately, `curl -T payload.bin http://127.0.0.1:<porta>/` and record the path received. Expected: received path recorded in both cases (no assumption about the result).
  5. S5 — close the server; update overview R1 with: curl version (`curl --version`), received paths, S3 timings, chosen branch (A, B or C) according to the rules in overview § Research and evidence / R1.
  Expected: R1 moves from UNVERIFIED to CONFIRMED for the conditions tested; note that the result applies to the sandbox's curl version.
  Recovery boundary: before S2 (background process): record PID and port in STATE §6 and clean up the process in S5.
  Failure handling: if the port is busy, choose another and record it. If curl is not on the PATH, block with evidence and do not invent the behaviour. Branch B: STOP, `Scope result: BLOCKED`, escalation to the user with the log and a request for a decision (do not replace D4 on your own).
  Operational note: the project hook redirects `curl` calls in the Bash shell; if the `curl` command is blocked, run the probe through the sandbox execution tool (`ctx_execute` with shell language), keeping traffic on `127.0.0.1`.
- **Unit tests:** N/A — the probe is not product code. Discovery check: the log contains at least the `PUT_START` and `BODY_END` events for each request (verified with grep of the log, count ≥ 2 PUTs for S3 and S4 combined).
- **e2e tests:** N/A — no observable product behaviour; the probe is itself the observation.
- **Done:** overview R1 updated with local evidence and branch; server process terminated; no file in the repository except overview R1; agent-1 review recorded in STATE §7; unit closed in STATE §4 with `uncommitted`.

### 0.2 Go skeleton and R3 check
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** create the module skeleton and record `go version` and the output of `go doc` in overview R3. Review: agent-1 checks that the chosen minimum version is consistent with R3.
- **Files:**
  - READ: no project file (only `go doc`).
  - WRITE: `docs/plans/001_plan-Bashtransfer/overview.md` (R3, `### R3` paragraph).
  - NEW: `go.mod` at the root (`module bashtransfer`, `go` directive = minimum version fixed here).
  - NEW: `cmd/bashtransfer/main.go` — only `package main` and `func main() {}` with a comment saying it is a placeholder until 1.3.
  - NEW: `.gitignore` at the root with `bin/` and `*.test`.
- **Change:**
  Preconditions: 0.1 closed or non-blocking (0.2 does not depend on the curl results).
  Contract: `go.mod` must declare a `go` version ≥ the one that exposes `EnableFullDuplex` (verified with `go doc`). No external dependencies (no `require`).
  Scope: skeleton only; no product code. No network calls (no `go mod download`).
  Steps:
  1. S1 — run `go version` and record the exact version; expected: version printed. If Go is missing: STOP, blocker `G-BASE` not runnable, escalation (installation requires authorization).
  2. S2 — run `go doc net/http.ResponseController.EnableFullDuplex`; record the signature and any version message. Expected: signature present; if absent → STOP and escalation (D4 to be reviewed).
  3. S3 — create `go.mod` with `module bashtransfer` and `go <minimum version>`, where the minimum version is the first one that exposes the method (declared in overview R3 with a local source). Expected: valid `go.mod`.
  4. S4 — create `cmd/bashtransfer/main.go` and `.gitignore`. Expected: `go vet ./...` exits with code 0 and `go list ./...` lists `bashtransfer/cmd/bashtransfer`.
  Recovery boundary: none (all changes are small and reversible; the unit closes in one session).
  Failure handling: `go vet` error → fix only the skeleton; no tests required.
- **Unit tests:** N/A — skeleton without logic. Gate G-BASE replaces them: `go vet ./...` exit 0 and `go list ./...` output non-empty.
- **e2e tests:** N/A — no observable behaviour.
- **Done:** `go.mod`, `cmd/bashtransfer/main.go`, `.gitignore` present; G-BASE pass recorded in STATE §7 with the Go version; R3 CONFIRMED (local source `go doc`, toolchain version); agent-1 review in STATE §7; unit closed in STATE §4 with `uncommitted`.

### 0.3 Probe R4: upload without a trailing `/` and from stdin (RS-1, RS-2)
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** run the probe and record R4 in overview. Review by agent-1 before choosing the branch (self-review, see STATE §7).
- **Files:**
  - READ: `Specifiche.md` (scenarios 1 and 2).
  - WRITE: `docs/plans/001_plan-Bashtransfer/overview.md` (R4 row and `### R4`).
  - NEW (outside the repository): `scratch/r4/` in the session scratch directory, with a probe server that decodes the chunked body (hex size lines, data, chunk 0) and reuses the chunked response of `scratch/r1/probe_server2.py`.
- **Change:**
  Preconditions: 0.1 closed (DONE).
  Contract: probe only on `127.0.0.1`; no repository changes.
  Steps:
  1. S1 — start the probe server on a free port; for each `PUT` log the path, the `Content-Length` and `Transfer-Encoding` headers, `PUT_START`, `RESPONSE_CHUNK_SENT`; after 3 s read the body up to the declared length or chunk 0 (`BODY_END bytes=<n>`); expected: server listening.
  2. S2 — case A: `curl -N -T payload.bin http://127.0.0.1:<porta>` (URL without `/`, 8 MiB payload from scratch/r1); expected: path recorded in the log.
  3. S3 — case B: `(printf x; sleep 3; head -c 8388608 /dev/urandom) | curl -N -T - http://127.0.0.1:<porta>/mydir.tgz > out.txt`, with a timestamp on the response line; expected: response line at `PUT_START` + 2 s (before the end of stdin), `Transfer-Encoding: chunked`, no `Content-Length`, `BODY_END bytes` equal to the bytes sent.
  4. S4 — case C: `head -c 1024 /dev/urandom | curl -N -T - http://127.0.0.1:<porta>`; expected: path recorded (expected `/` with no name).
  5. S5 — close the server (no leftover listener); update R4 with evidence and the branch according to the rule in overview R4.
  Recovery boundary: before S2 (PID and port recorded in STATE §6).
  Failure handling: case A without a name, or case B blocked until EOF → STOP, `Scope result: BLOCKED`, escalation to the user with the log. Do not change D4/D5/D16 on your own. A curl error in case B caused by stdin → retry once, then escalation.
  Operational note: as in 0.1, `curl` in Bash is blocked by the hook; run it through the sandbox execution tool, traffic on `127.0.0.1`.
- **Unit tests:** N/A — probe. Discovery: log with at least 3 `PUT_START`.
- **e2e tests:** N/A — the probe is itself the observation.
- **Done:** overview R4 with evidence and branch; server terminated; self-review in STATE §7; unit closed in STATE §4 with commit.

## Phase gates and closure
- Required gates: G-BASE (recorded in STATE §3), G-U0.2 = `go vet ./...` (exit 0).
- Assertions: R1, R3 and R4 no longer UNVERIFIED; branches for R1 and R4 chosen and recorded.
- README obligation: no distributed behaviour in this phase; README not yet created; verified as N/A with justification in STATE §11 "Documentation".
- Every sub-phase DONE or SKIPPED with justification. Run the phase gate now.
- Supervisor agent-1 (claude-haiku-5-5) reviews: evidence for R1 and R3, chosen branch. Review recorded as a self-review by the same model.
- Closure of P0: unit `P0` in STATE §4; phase DONE in STATE §11 only if R1 and R4 are CONFIRMED with branch A and R3 is CONFIRMED. With branch B, R4 blocking or R3 absent: phase BLOCKED.
