# Bashtransfer — Implementation state

Read first at every session. Updated 2026-10-27 by agent-1 (claude-haiku-5-5), execute verify 001 (V001-C1..C3).
Plan ID: 001. Revision: 8 (rev 2: RS-2 from Specifiche, D16, R4, 0.3; rev 3: D17 tombstone 410; rev 4–5: deviations in 1.2 and Expect fix; rev 6: deviations in 2.1/2.2; rev 7: phase 4; rev 8: B-NGX, stream TLS). Repo root: /mnt/fabio/dati/Git/SperimentazioniAI/bashtransfer.
Plan baseline: no commit (HEAD not born on `main`); pre-existing changes: `Specifiche.md` untracked, owned by the user, not an output of the plan.
Roster: `agent-1:claude-haiku-5-5,agent-2:claude-haiku-5-5` (explicit user override: Haiku 5.5 only, no Sonnet or Opus). Active execution style: handoff.
State writer: none yet (plan not running); ownership: released.

## 0. Resume and completion protocol

1. Read this file. Identify readiness, active scope, role, unit/attempt, step, checkpoint, branch and baseline. A new session resumes only after the previous state writer has released ownership. A delegated worker stays in its assigned OPEN unit and owns neither state nor commits.
2. Inspect `git status` and the staged/working diff and the checkpoint postconditions (HEAD may not exist: baseline without commits). Preserve changes that are not yours, including hunks in shared files. Do not reconcile a unit before verifying the actual diff. An OPEN unit resumes from the first unverified step, even if the checkpoint is old.
3. Read the targeted phase/unit and the local contracts. Check the rows of the prerequisite sub-phases and the artifacts. Locate symbols; do not trust line numbers. A changed contract or a missing design goes to agent 1 (supervisor). External data: no web search without explicit authorization (user's CLAUDE.md).
4. Before editing, open: type, ID, attempt, OPEN, unit base, owned paths, step S1; mark sub-phase and phase IN_PROGRESS. Checkpoint in §6 before handoff, long or risky resources, blockers, or a likely pause. Ordinary steps and passing tests do not require state writes.
5. Focused tests at the end of each sub-phase, full gate at P<N>, confirming that the expected tests ran (non-zero count). A future gate is not pass. A required gate that is missing, blocked, zero-test or failed prevents DONE.
6. Close the unit: documentation, ledger update in §4, progress in §11, next eligible unit. Commit only if the policy requires it (see §3). Commit reference: `unit:<id>:<attempt>` with trailers `PEV-Plan`, `PEV-Unit`, `PEV-Attempt`, `PEV-Result: complete`. If commits are disabled: `uncommitted`, no staging and no commit.
7. Interruption before commit: an unresolved reference = pending closure; reconcile before new work. If already committed: resolve the reference, no double commit.
8. After the sub-phases: open P<N> for full gates and supervisor review. Record the phase as DONE.
9. Escalation: a worker asks the supervisor for missing decisions or after two failed attempts at the same problem. The supervisor may revise technical steps without changing requirements; record the revision in §8. Never weaken acceptance. New product choices, scope or external authority → user.
10. On handoff: save the exact next action, pending changes, evidence, and ownership release.

## 1. Current unit and scope

- Active scope: plan 001 whole, phases 0–4 (user request: "Fai tutte le fasi in autonomia", i.e. "do all the phases autonomously")
- Scope result: COMPLETE — plan 001 phases 0–4 closed; historical V001 audit FAIL, all findings FIXED or ACCEPTED (user decision 2026-10-27). Code unchanged since `2d91a0d`, so the P4 G-FINAL remains valid. See `verify/verify_001_2026-10-27.md`
- Type: correction
- ID / attempt: V001-C1 / 2 (completed)
- Status: DONE
- Intent: measure D13 over 4 sessions; criterion accepted by the user 2026-10-27
- Assigned: none; supervisor: agent-1 (claude-haiku-5-5)
- File / unit heading: none
- Current step: none
- Next action: none in scope. Out of scope, not authorized: push, real certificate for deployment, auth verification via nginx.
- Next eligible plan unit: none
- Unit base: `11f6d7c` (P3/2 closure); branch `plan/001-bashtransfer`; complete V001-C1 attempt 2 commit: see §4
- Repo state: HEAD = V001-C1 attempt 2 commit; `Specifiche.md` untracked and intact; no push; compose stack stopped and test network removed.

## 2. Feature context and readiness

Readiness: READY (plan rev 8). Cold review and self-review recorded in §7; revisions 2–8 in §8.
Handoff order: 0.1, 0.2, 0.3, 1.1, 1.2, 1.3, 2.1, 2.2, P2, 3.1, P3, 4.1, 4.2, 4.3, P4. All DONE.
Eligible now: none. Scope complete.
Open blockers: none. V001-F1..F3 closed with user acceptance 2026-10-27. R1–R4 CONFIRMED.

Goal: curl/wget relay for files and folders, without persistence, in Go, with docker compose and nginx. Exclusions in `overview.md` § Goal. Reference scenario RS-1 in `overview.md`.

Current decisions: D1–D14 in `overview.md`; D15 (inactivity timeout, added by 2.2 within the approved requirements); D16 (upload from stdin with unknown length, RS-2, rev 2).
Questions: Q1 resolved by the user — auth only on `PUT`; `GET /<slug>` public with only the slug.
Research evidence: `overview.md` § Research and evidence — R1 CONFIRMED (branch A, probe 0.1), R2 CONFIRMED (nginx.org; superseded by rev 8: `stream`), R3 CONFIRMED (`go doc`, go1.21), R4 CONFIRMED (probe 0.3).
Required unverified external claims: none. Blocker: §9 B-V001-F1..F3 (user decisions, not technical).
Required supervisor reviews cannot be silently replaced by weaker self-review: agent-1's review is a self-review by the same model (Haiku 5.5); it must not be recorded as a strong review.

## 3. Environment, settings, and gate registry

- Full autonomous: true (explicit user override, invocation `001 ... full-autonomous:true --wip-commit`)
- WIP commits: on (explicit user override)
- Completion policy: full autonomy true → local commit for each completed sub-phase and for each P<N> closure; WIP on → interruption commits with `PEV-Result: wip`. No push, no deploy, no remote branch. Network permissions: only nginx.org (docs) and pulls of pinned Docker images.
- Commit identity: `GIT_AUTHOR_NAME=fabio.pierri`, `GIT_AUTHOR_EMAIL=fabio.pierri@synclab.it` passed per command (no git config change). Attribution trailer: `Co-Authored-By: Claude Haiku 5.5 <noreply@anthropic.com>`.
- Scope/roster survive handoff. Explicit invocation overrides stored settings.
- Execution style: handoff (later sessions). No automatic dispatch to the supervisor: the supervisor is an agent of the same model in the same configuration; the review request is recorded in §7 as a self-review.
- Setup: Go (version from 0.2; no external dependencies); local curl (version from 0.1); Docker + base images (4.1, network); nginx (4.2, network). Scratch directory outside the repository for probes and temporary binaries. Free local ports chosen and recorded. Cleanup: terminate probe processes and remove only directories created by the script.
- Supervisor access: agent-1 (claude-haiku-5-5), separate session or same agent in the reviewer role; no stronger model is available to the project.

| Gate | Stage / active from | Exact command and cwd | Required assertions/discovery | Setup |
|------|---------------------|-----------------------|-------------------------------|-------|
| G-BASE | baseline, before 0.2 | `git status --short` (repo root cwd); `go version` | snapshot: `Specifiche.md` and `docs/plans/` untracked; Go version recorded | Go installed |
| G-U0.2 | sub-phase 0.2 | `go vet ./...` (repo root) | exit 0; `go list ./...` not empty | `go.mod` from 0.2 |
| G-R1 | sub-phase 0.1 | probe in scratch directory outside repo (`probe_server.py` on `127.0.0.1`) + log check | log with `PUT_START`, `BODY_END`, response before `BODY_END` (branch A) | local curl, free port |
| G-R4 | sub-phase 0.3 | probe in scratch directory outside repo (`scratch/r4`, `127.0.0.1`) + log check | case A: path with name `/<basename>`; case B: response line before stdin EOF, `Transfer-Encoding: chunked`, no `Content-Length`; case C: recorded path | local curl, free port |
| G-U1.1 | sub-phase 1.1 | `go test ./internal/config ./internal/slug ./internal/relay -run 'TestLoad|TestGenerate|TestRegistry' -v -count=1 -race` (root) | 12 tests: 3 config, 3 slug, 6 registry; none skipped | none |
| G-U1.2 | sub-phase 1.2 | `go test ./internal/relay ./internal/server -v -count=1 -race` (root) | copy: 3 tests; server: upload commands, round trip (4 subtests), 404, 410, abort, cleanup, 405 | none |
| G-E2E1 | sub-phase 1.3 | `bash scripts/e2e.sh` (root) | output `E2E OK`; identical sha256; 410; 404 | curl, free port, go build |
| G-U2.1 | sub-phase 2.1 | `go test ./internal/server -run 'TestBasicAuth|TestUploadRequiresAuth' -v -count=1 -race` | 6 tests | none (auth only on PUT) |
| G-U2.2 | sub-phase 2.2 | `go test ./internal/server ./internal/config ./internal/relay -v -count=1 -race` | sanitize (table), idle timeout, headers, idle config | none |
| G-E2E2 | end of P2 | `bash scripts/e2e.sh` with auth env | name with spaces; upload with auth succeeds, without auth 401, download without auth succeeds | none |
| G-P1 | P1 | `go test -race -count=1 ./...` (root) + G-E2E1 | non-zero count; no failures | none |
| G-P2 | P2 | `go test -race -count=1 ./...` + G-E2E2 | non-zero count | none |
| G-BENCH | sub-phase 3.1 | `bash scripts/bench.sh` (root) | 5 samples per benchmark; relay median ≥ 0.90 × baseline (D13) | stable machine, no other load |
| G-U3.1 | sub-phase 3.1 | `go test ./internal/server -run TestBenchHarnessSmoke -v -count=1 -race` | 1 test; 1 MiB received exactly | none |
| G-P3 | P3 | `go test -race -count=1 ./...` | non-zero count | none |
| G-D1 | sub-phase 4.1 | `docker compose -f compose.yaml config` (root); `docker build -t bashtransfer:test .` | config exit 0, no `ports:` on `app`; build pass **or** blocked (network) | Docker; local base images or authorized network |
| G-E2E-FINAL | sub-phase 4.2 (R2 residual `client_body_timeout`) | RS-1 via compose + nginx: `bash scripts/e2e.sh` pointed at nginx port; 256 MiB upload without truncation | identical sha256; 410 | 4.1 + 4.2 + network |
| G-FINAL | last P4 | `go test -race -count=1 ./...`; G-E2E-FINAL; G-BENCH already PASS in P3 | non-zero count; RS-1 complete | all of the above |

Rule: a gate with `blocked` does not count as pass. A gate `not-applicable-yet` does not count as pass.

## 4. Work ledger

| Type | ID / attempt | Plan revision | Agent | Changes | Evidence/review | Commit |
|------|--------------|---------------|-------|---------|-----------------|--------|
| sub-phase | 0.1 / 1 | 1 | agent-2 (claude-haiku-5-5), state writer current session | overview R1; STATE; plan folder (untracked before, included for provenance) | G-R1 pass branch A; self-review §7 | unit:0.1:1 (resolved from trailer) |
| plan | 001 / 1 | 1 | agent-1 (claude-haiku-5-5) | plan folder: overview, phase_01..phase_05, STATE | no execution; no code review | uncommitted (planning does not commit) |
| phase closure | P0 / 1 | 2 | agent-1 (claude-haiku-5-5), self-review | phase 0 complete; no product code | G-BASE, G-U0.2, G-R1, G-R4 pass; R1/R3/R4 CONFIRMED; self-review §7 | unit:P0:1 |
| sub-phase | 1.3 / 1 | 5 | agent-2 (claude-haiku-5-5), state writer current session | cmd/bashtransfer/main.go; scripts/e2e.sh (+x); handler.go Expect fix and test | G-E2E1 PASS; G-U1.2 with TestUploadExpectContinue PASS; self-review §7 | unit:1.3:1 |
| phase closure | P1 / 1 | 5 | agent-1 (claude-haiku-5-5), self-review | phase 1 complete; invariants I-1..I-5 covered (I-1 TestCopyAllocsBounded; I-2 TestRegistryClaimOnce/TestGetSecondClaim410; I-3 TestRegistryFinishOnce/TestReceiverAbortCleansSession; I-4 TestRelayRoundTripSHA/TestSenderAbortFailsReceiver; I-5 TestGenerateUnique/TestRegistrySlugCollision) | G-P1 PASS | unit:P1:1 |
| sub-phase | 1.2 / 1 | 4 | agent-2 (claude-haiku-5-5), state writer current session | internal/relay/copy.go(+test), internal/server/{receiver,handler}.go(+test); slug.Valid | G-U1.2 pass (-race, 5 repetitions); post-write self-review §7; deviations rev 4 | unit:1.2:1 |
| sub-phase | 1.1 / 1 | 3 | agent-2 (claude-haiku-5-5), state writer current session | internal/config, internal/slug, internal/relay/registry.go (+test) | G-U1.1 pass; self-review §7 | unit:1.1:1 |
| plan | rev 3 / 1 | 3 | agent-1 (claude-haiku-5-5) | D17, phase_02 1.1/1.2, STATE §8 | self-review | unit:plan-rev-3 |
| sub-phase | 0.3 / 1 | 2 | agent-2 (claude-haiku-5-5), state writer current session | probe in scratch (no repo file); overview R4; phase_05 README limit | G-R4 pass; self-review §7 | unit:0.3:1 |
| sub-phase | 0.2 / 1 | 2 | agent-2 (claude-haiku-5-5), state writer current session | go.mod, cmd/bashtransfer/main.go (placeholder), .gitignore; overview R3 | G-BASE, G-U0.2 pass; R3 CONFIRMED; self-review §7 | unit:0.2:1 |
| plan | 001 / 2 (RS-2 revision) | 2 | agent-1 (claude-haiku-5-5) | overview D16, R4, RS-2; phase_01 0.3; phase_02 stdin upload and tests; phase_05 RS-2 and README; STATE | specification verified against plan; self-review (Haiku, same model) | uncommitted → committed as plan rev 2 before 0.2 |
| plan | 001 / 1 (cold review) | 1 | agent-1 (claude-haiku-5-5) | fixes a–l in phase_02, phase_04, phase_05, STATE; Q1 and network recorded | cold review self (fresh Haiku, no planning context) | uncommitted |
| sub-phase | 2.1 + 2.2 / 1 | 6 | agent-2 (claude-haiku-5-5), state writer current session | internal/server/{auth,name}.go(+test), handler.go (Authorize, IdleTimeout, SanitizeName, 401), handler_test.go, internal/relay/copy.go (NewIdleReader), internal/config (IdleTimeout), cmd main.go, scripts/e2e.sh (auth, name with spaces) | G-U2.1 PASS, G-U2.2 PASS, G-E2E2 PASS; self-review §7 | unit:2.2:1 (`d64bffb`, single commit, rev 6) |
| phase closure | P2 / 1 | 6 | agent-1 (claude-haiku-5-5), self-review | phase 2 complete; I-1..I-5 unchanged; D9, D15, D5 covered | G-P2 PASS (35 PASS) | unit:P2:1 (`c385106`, STATE only) |
| sub-phase | 3.1 / 1 | 6 | agent-2 (claude-haiku-5-5), state writer current session | internal/server/bench_test.go (NEW), scripts/bench.sh (NEW, +x) | G-U3.1 PASS; G-BENCH PASS (ratios 1.02 / 0.956 / 0.991); self-review §7 | unit:3.1:1 (`2d91a0d`) |
| phase closure | P3 / 1 | 6 | agent-1 (claude-haiku-5-5), self-review | phase 3 complete; 256 KiB buffer confirmed by data | G-P3 PASS (36 PASS); G-BENCH PASS | unit:P3:1 (STATE-only commit) |
| reconciliation | V001-C2 / 1 | 7 | agent-1 (claude-haiku-5-5), state writer | completion references: 4.1 and 4.2 → `70c85be`; 2.1 → `d64bffb`; P4 → `70c85be` with ambiguity with `ae101a1` recorded (default, no commit rewrite) | `git log` trailer check; V001-F2, V001-F3 remain OPEN until user decision | be994da (trailer PEV-Unit: V001-C3; C2 in the same commit) |
| sub-phase | 4.1 / 1 | 7 | agent-2 (claude-haiku-5-5), state writer current session | Dockerfile, .dockerignore, compose.yaml (app, nginx), deploy/nginx/bashtransfer.conf | G-D1 PASS; self-review §7 | unit:4.2:1 wip (`e2ccd94`, with 4.2) |
| sub-phase | 4.2 / 1 | 7 | agent-2 (claude-haiku-5-5), state writer current session | deploy/nginx/bashtransfer.conf, nginx service, scripts/e2e.sh (external mode) | G-S2 PASS; G-E2E-FINAL FAIL (B-NGX) | unit:4.2:1 wip, `PEV-Result: blocked` (`e2ccd94`) |
| sub-phase | 4.3 / 1 | 7 | agent-2 (claude-haiku-5-5), state writer current session | README.md (Italian) | cross-check of variables with config.go; nginx section not verified | in `70c85be` (P4 closure) |
| correction | V001-C1 / 1 | 8+C1 | V001 session (claude-haiku-5-5) | README §Performance (medians 256 KiB 0.99, 1 MiB 0.98); STATE §7–§9 | G-BENCH V001-C1 measures 4 sessions; criterion NOT confirmed (proposal, not decision) | d1703fe (commit message with unverified user decisions: see attempt 2) |
| phase closure | P3 / 2 | 8+C1 | V001 session (claude-haiku-5-5) | P3 reopened by V001-F1; closure NOT done: depends on criterion confirmation | G-P3 attempt 2 pass (36/0/0) | none (BLOCKED) |
| correction | V001-C1 / 2 | 8+C1 | V001 session (claude-haiku-5-5) | reconciliation: 'user decisions' removed (not found in the record); F1–F3 pending; §1/§4/§7/§8/§9/§11 and verify/ aligned | — | wip (`PEV-Result: wip`, unit open until confirmation) |
| phase closure | P3 / 2 | 8+C1 | agent-1 (claude-haiku-5-5), self-review; criterion accepted by user 2026-10-27 | G-P3 attempt 2 pass (36/0/0); G-BENCH V001-C1 pass | unit:P3:2 |
| correction | V001-C1 / 2 | 8+C1 | agent-1 (claude-haiku-5-5), self-review; criterion accepted by user 2026-10-27 | full reconciliation: F1 FIXED, F2 and F3 ACCEPTED; §1/§2/§4/§7/§8/§9/§11 and verify/ aligned | unit:V001-C1:2 |

The rows are append-only. Commit reference: see §0.6.

## 5. Files and ownership

| Path/hunks | Existing changes to preserve | Unit changes | Owning unit |
|------------|------------------------------|--------------|-------------|
| `Specifiche.md` | untracked, owned by the user, do not modify | none | none (user) |
| `docs/plans/001_plan-Bashtransfer/**` | created in planning | only state updates and R-evidence from units | planning (001) |
| `go.mod`, `.gitignore`, `cmd/bashtransfer/main.go` (placeholder) | none | NEW | 0.2 |
| `internal/config/**` | — | NEW | 1.1 (extended in 2.2) |
| `internal/slug/**` | — | NEW | 1.1 |
| `internal/relay/**` | — | NEW | 1.1 (registry), 1.2 (copy), 2.2 (deadline) |
| `internal/server/**` | — | NEW | 1.2, 2.1 (auth), 2.2, 3.1 (bench) |
| `cmd/bashtransfer/main.go` | 0.2 placeholder | WRITE | 1.3 (main), 2.1 (auth wiring) |
| `scripts/e2e.sh`, `scripts/bench.sh` | — | NEW | 1.3, 3.1 |
| `Dockerfile`, `.dockerignore`, `compose.yaml` | — | NEW | 4.1 (4.2 adds nginx) |
| `deploy/nginx/nginx.conf` | — | NEW (rev 8; replaces `bashtransfer.conf`, committed in `e2ccd94` and removed in `70c85be`) | 4.2 |
| `README.md` | — | NEW | 4.3 |

## 6. In-flight checkpoint

none — no open unit. Planning did not write execution checkpoints.
Research checkpoint: N/A — web research not performed (network not authorized); R1/R3 to be executed in 0.1/0.2; R2 pending authorization.

## 7. Verification and reviews

| Gate/test | Command | Result | Test count/named evidence | Tested revision/diff | When |
|-----------|---------|--------|--------------------------|---------------------|------|
| G-R1 (0.1) | probe `curl -N -T` on `127.0.0.1`, probe server in scratch outside repo | pass (branch A) | S3: line `COMANDI-DI-PROVA` within 2 ms of the response; `BODY_END` +3 s, 8388608/8388608 bytes. S4: path `/probe-name.zip`, `/payload.bin` (basename). First pass with `Content-Length` response discarded (truncated body) | overview R1, working tree pre-commit | 2026-10-27 |

| Review | Reviewer | Plan revision / reviewed change | Invariants/assertions checked | Verdict |
|--------|----------|---------------------------------|------------------------------|---------|
| Review 0.1 (self-review, same model) | agent-1 (claude-haiku-5-5), current session | rev 1 / overview R1 + probe log | branch A result; path with name; D4 constraint (response open until end of relay) | PASS, self-review; not a strong review |

Possible results: pass, fail, blocked, not-run, not-applicable-yet. Only pass satisfies a required gate.

| Review | Reviewer | Plan revision / reviewed change | Invariants/assertions checked | Verdict |
|--------|----------|---------------------------------|------------------------------|---------|
| Plan cold review (self-review, fresh Haiku 5.5 agent, no planning context; not a strong review) | agent-1 (claude-haiku-5-5) | rev 1 / overview, phase_01..05, STATE | missing steps in 1.2; base URL rule; D11 placeholder; server test count 7; review timing (before `handler.go`); ownership; abort test mechanics; 503/401 bodies; baseline without HEAD; readiness and handoff order | findings a–l applied; no new decisions |
| G-BASE | `git status --short`; `go version` | pass | snapshot: `Specifiche.md` untracked, `docs/plans/` untracked → committed in rev 2; go1.27.3 | repo, rev 2 | 2026-10-27 |
| G-U0.2 (0.2) | `go vet ./...`; `go list ./...` | pass (exit 0; list = `bashtransfer/cmd/bashtransfer`) | 0 tests (N/A skeleton) | go.mod, main.go, .gitignore | 2026-10-27 |
| Review 0.2 (self-review, same model) | agent-1 (claude-haiku-5-5) | rev 2 / go.mod `go 1.21` | R3: signature present; `api/go1.21.txt` line 356; minimal choice 1.21 | PASS, self-review; not a strong review |
| Residual R2 execution check | — | rev 2 | `client_body_timeout` (4.2 S1) | superseded: closed by rev 7/8 (G-E2E-FINAL PASS via stream; see §8) |
| G-R4 (0.3) | probe `curl -N -T` on `127.0.0.1:18090` with `probe_server.py` in scratch `r4/`; `payload.bin` 8 MiB from `r1/`; stdin with `printf x; sleep 3; head -c 8388608` | pass | A: `PUT_START path=/payload.bin CL=8388608`, `BODY_END 8388608`. B: `PUT_START path=/mydir.tgz TE=chunked`, line at +3 ms, stdin EOF +3 s, `BODY_END 8388609`. C: `path=/`, `BODY_END 1024`. Listener closed | overview R4, working tree pre-commit | 2026-10-27 |
| G-U1.1 (1.1) | `go test ./internal/config -run TestLoad`; `go test ./internal/slug`; `go test ./internal/relay -run TestRegistry -race` | pass | 3 config (+5 subtests), 3 slug, 6 registry; none skipped | rev 3, working tree pre-commit | 2026-10-27 |
| Review 1.1 (self-review, same model) | agent-1 (claude-haiku-5-5) | rev 3 / registry.go Finish/Abort/Claim/expire + tombstone | I-2 (single claim), I-3 (Finish/expire once; close once via once/idempotence), I-5 (collision + tombstone not reassigned); no nested lock (Session.Abort/Err use reg.mu, Finish does not call it) | PASS, self-review; not a strong review |
| Review 0.3 (self-review, same model) | agent-1 (claude-haiku-5-5) | rev 2 / overview R4 + server log | outcome A/B/C; D16 valid; D5 limit for case C documented | PASS, self-review; not a strong review |
| G-U2.1 (2.1) | `go test ./internal/server -run 'TestBasicAuth|TestUploadRequiresAuth' -v -count=1 -race` | pass | 6 tests: 4 basic auth, 1 constant cost (behavioral), 1 PUT without credentials → 401 without session; with credentials → commands | rev 6, `d64bffb` | 2026-10-27 |
| G-U2.2 (2.2) | `go test ./internal/server ./internal/config ./internal/relay -v -count=1 -race`; `go test -race -count=3 -run 'Idle|Headers|SanitizeName|Auth' ./internal/server` | pass | sanitize (8-case table + 2 lengths); TestUploadIdleTimeout; TestDownloadHeaders; TestConfigIdleTimeout; none skipped | rev 6, `d64bffb` | 2026-10-27 |
| G-E2E2 (end of P2) | `bash scripts/e2e.sh` | pass (`E2E OK`) | RS-1 and RS-2 unchanged; PUT without credentials 401; wrong password 401; authorized upload `strano nome.zip` with correct `curl -o`/`wget -O` commands; public download with auth; sha256 unchanged | rev 6, `d64bffb` | 2026-10-27 |
| G-P2 (P2) | `go test -race -count=1 -v ./...` (count) + G-E2E2 | pass | 35 PASS, 0 FAIL, 0 SKIP | rev 6, `d64bffb` | 2026-10-27 |
| G-U3.1 (3.1) | `go test ./internal/server -run 'TestBenchHarnessSmoke' -v -count=1 -race` | pass | 1 test; 1 MiB exactly per side | rev 6, `2d91a0d` | 2026-10-27 |
| G-BENCH V001-C1 (4 sessions) | `scripts/bench.sh` × 4 sessions, same code (no change to `internal`/`cmd` after `2d91a0d`). Relay/baseline ratios per session (S1 original run; S2 V001 rerun; S3, S4 recorded by the previous session, not re-run in this one): 64 KiB 1.023 / 1.115 / 1.078 / 1.209 (median 1.097); 256 KiB 0.956 / 0.896 / 1.022 / 1.078 (median **0.989**, 3 of 4 ≥ 0.90); 1 MiB 0.991 / 1.013 / 0.978 / 0.887 (median 0.9845, 3 of 4 ≥ 0.90). Criterion 'median of sessions ≥ 0.90': **accepted by the user 2026-10-27**; gate D13 PASS. Relay in absolute MB/s: 256 KiB highest in all four sessions. Default 256 KiB confirmed. | rev 8+C1, code unchanged | 2026-10-27 |
| G-P3 V001-C1 attempt 2 | `go test -race -count=1 -v ./...` | pass | 36 PASS, 0 FAIL, 0 SKIP (top-level; 45 including subtests); `git diff 2d91a0d -- internal cmd go.mod` empty | rev 8+C1, code unchanged since `2d91a0d` | 2026-10-27 |
| G-BENCH (3.1) | `scripts/bench.sh` (nproc 8, go1.27.3, Intel Xeon; 256 MiB per op, -benchtime 3x -count 5, without -race) | pass | Medians MB/s (relay / baseline): 64 KiB 5337.68 / 5219.48 = **1.02**; 256 KiB 6548.81 / 6853.35 = **0.956**; 1 MiB 6033.41 / 6086.60 = **0.991**. Criterion D13 ≥ 0.90: PASS in all three. Default 256 KiB: highest relay (6548.81 vs 6033.41 and 5337.68, 8.5% difference over 1 MiB, no revision) | rev 6, `2d91a0d`; raw output in scratchpad `bench-run1.txt` | 2026-10-27 |
| G-P3 (P3) | `go test -race -count=1 -v ./...` (count) | pass | 36 PASS, 0 FAIL, 0 SKIP | rev 6, `2d91a0d` | 2026-10-27 |
| G-D1 (4.1) | `docker build -t bashtransfer:local .`; `docker compose -f compose.yaml config`; `docker compose up -d app` + `wget -qO- http://app:8080/` from the internal network | pass (build 11 s; config without `ports:` for app; usage text received) | base images pinned by digest; non-root user (`nonroot:nonroot`) | rev 7, `e2ccd94` | 2026-10-27 |
| G-S2 (4.2) | `nginx -t` in the `nginx` service with test `BASHT_CERTS_DIR` | pass (`configuration file ... test is successful`) | 1 config | rev 7, `e2ccd94` | 2026-10-27 |
| G-E2E-FINAL (4.2) rev 8 | compose stack (`nginx` stream TLS 443, `app`), `BASHT_PUBLIC_URL=https://127.0.0.1`: `BASHT_E2E_BASE=https://127.0.0.1 BASHT_E2E_MB=256 bash scripts/e2e.sh` | PASS (`E2E OK`, 2.3 s): RS-1 and RS-2 with 256 MiB, identical sha256, 410 and 404; commands with test base | — | rev 8, working tree (P4 commit) | 2026-10-27 |
| G-E2E-FINAL pre-rev 8 | config http proxy | FAIL: stall at 0.1–0.8 MB (historical, see B-NGX) | — | rev 7, `e2ccd94` | 2026-10-27 |
| Paired tests B-NGX | `scripts/pair.sh` (scratchpad): bare nginx http 18080 → upstream early (`WriteHeader`+Flush before body) vs late | early: FAIL (upstream body 3932160, EOF); late: PASS (8388608 in 3 s) | — | rev 8 | 2026-10-27 |
| G-FINAL (P4) | `go test -race -count=1 -v ./...` (count) + `bash scripts/e2e.sh` (local) + G-BENCH (P3, already PASS) + G-E2E-FINAL (RS-1/RS-2 via compose + nginx stream) | pass | 36 PASS, 0 FAIL, 0 SKIP; local `E2E OK`; compose `E2E OK` 256 MiB | rev 8, working tree (P4 commit) | 2026-10-27 |
| Advisor (Sonnet 5.5, on user request) | decision test for B-NGX (early/late pairs) and choice of `stream` | agreement on the diagnosis; recommended stream + verification before changing the contract | rev 8 | 2026-10-27 |
| Review 4.x (self-review, same model) | agent-1 (claude-haiku-5-5) | rev 8 / compose.yaml, deploy/nginx/nginx.conf, README | `BASHT_PUBLIC_URL` mandatory; no secret in repo (certificates from host); ports published only on nginx; external e2e checks the command base | PASS, self-review; not a strong review. Limit: test certificate, no public CA, auth not tested via nginx |
| G-S2 rev 8 | `nginx -t` in the stack; `docker compose config` with `BASHT_PUBLIC_URL` mandatory (fails without it) | pass | 1 config | rev 8 | 2026-10-27 |
| Review 4.1 (self-review, same model) | agent-1 (claude-haiku-5-5) | rev 7 / Dockerfile, compose | digest pinning verified with `docker images`; no secret in image (`.dockerignore`, env from variables); ports only on nginx | PASS, self-review |
| Review 3.1 (self-review, same model) | agent-1 (claude-haiku-5-5) | rev 6 / bench_test.go, scripts/bench.sh | two-hop baseline verified in code: client → `minimalRelay` (PUT) → receiver GET, same `BufSize`, flush per block, no registry/slug/header; exact bytes verified in `transfer`; criterion applied without rewriting; buffer chosen from data (256 KiB is the fastest relay) | PASS, self-review; not a strong review. Limit: single measurement, shared 8-CPU machine, variance between samples up to ~10% | 2026-10-27 |
| Review V001-C1 (self-review, same model) | agent-1 (claude-haiku-5-5), V001 session | rev 8+C1 / README §Performance, STATE §7–§9 | README numbers = medians of §7 (1 MiB corrected to 0.98); no code touched; criterion flagged as not confirmed | PASS on the numbers; NOT a strong review; criterion accepted by user 2026-10-27 |
| Review P3/2 (self-review, same model) | agent-1 (claude-haiku-5-5), V001 session | rev 8 / code `2d91a0d` unchanged (`git diff 2d91a0d -- internal cmd go.mod` empty) | D13 invariants (256 KiB median ≥ 0.90 over 4 sessions, criterion accepted); G-P3 36 PASS / 0 FAIL / 0 SKIP | PASS; NOT a strong review (same model) |
| Review 2.1+2.2 (self-review, same model) | agent-1 (claude-haiku-5-5) | rev 6 / auth.go, name.go, handler.go Options and 401 | I-5 (no directory from the name: `SanitizeName` cuts at `/`); D9 (only PUT protected, GET public); D15 (idle deadline before each Read, `ErrIdle`); constant cost of the comparison verified in diff, not only behaviorally; `WWW-Authenticate` on 401 | PASS, self-review; not a strong review |

## 8. Technical revisions and deviations

| Revision | Previous decision/step | Approved replacement and reason | Supervisor | Dependents/revalidation |
|----------|------------------------|---------------------------------|------------|------------------------|
| 8+C1 | D13 (relay/baseline ratio ≥ 0.90 on median of 5 samples): V001 rerun at 256 KiB = 0.896, original run 0.956 | Accepted by the user 2026-10-27: criterion applied to the median of sessions at 256 KiB (0.989 ≥ 0.90), 4 sessions. Threshold 0.90 unchanged. The P3 closure in commit `d1703fe` was premature; P3/2 closed in its own commit | user (acceptance 2026-10-27); agent-1 (proposal) | README §Performance; §7 G-BENCH V001-C1; §9 B-V001-F1 |
| 6 | Two execution deviations in 2.1/2.2 (no D1–D17 decision changed): (a) `SanitizeName` splits only on `/` (the `\` is removed as a forbidden character, so the contract result does not change; the test table stays `a"b\c.zip` → `abc.zip`); (b) 2.1 and 2.2 are in the same commit `d64bffb` instead of two separate commits, because `handler.go` and `handler_test.go` share changes and per-hunk separation was not verifiable without risk | agent-1 (self-review, Haiku 5.5), during execution of 2.2 | name.go, name_test.go; ledger §4 row 2.1+2.2; no test changed |
| 8 | B-NGX resolved (no D1–D17 decision changed, but D12/D14 and R2 updated): nginx HTTP proxy cannot forward the relay (D4: commands before body; paired test). Choice: TLS `stream` on 443 + `http` 80→443 redirect; `BASHT_PUBLIC_URL` mandatory in compose (no HTTP headers added); `client_body_timeout` no longer used (rev 7 is superseded for the http block). External e2e test verifies that the commands use the test base | agent-1 (supervisor decision, Haiku 5.5; user authorized Sonnet only as advisor: consulted, see §7); self-review | overview R2/rev 8, phase_05 4.2, README (nginx, limits, verification), compose.yaml, deploy/nginx/nginx.conf, scripts/e2e.sh (base check) |
| 7 | Phase 4 (rev 7): (a) `client_body_timeout 3600s` set in the 443 server (default 60s, read from nginx.org) so as not to truncate senders stalled before `BASHT_IDLE_TIMEOUT`; (b) `BASHT_E2E_BASE` and `BASHT_E2E_MB` added to `scripts/e2e.sh` for the external test; (c) 4.3 README written with unmet 4.2 precondition: content not dependent on nginx, nginx section declared unverified; (d) test ports 18080/18443 only as out-of-repo override for diagnosis; (e) Dockerfile and compose pinned by tag and digest; `deploy/` excluded from the Docker context beyond the plan's list | agent-1 (self-review, Haiku 5.5) | overview R2; phase_05 4.2 and 4.3; README; no change to D1–D17 |
| 5 | Technical correction found by e2e in 1.3 (no decision changed): with `Expect: 100-continue` (curl for body > 1 MiB) the `WriteHeader(200)` before reading the body makes the Go body close (`ErrBodyReadAfterClose`, `Connection: close`, sender reset, 0-byte download). Fix: explicit `100 Continue` before the commands when `Expect` is present and `ContentLength != 0` (`expectsContinue`). Test `TestUploadExpectContinue` | agent-1 (self-review), found by `scripts/e2e.sh` | handler.go; server tests 10 top-level; D4 unchanged |
| 4 | Deviations of 1.2 recorded after implementation (no D1–D17 decision changed): (a) `Allow: GET, PUT` on `/` and `/<seg>` instead of `Allow: PUT`, because `GET /` is the usage page; (b) the sender waiting without a body read is not detected as dropped from `r.Context()` (the Go server starts the background read only at end of body); the only closure is the D7 expiry (`BASHT_WAIT_TIMEOUT`), declared as a limit in 4.3; (c) name from path not sanitized until 2.2 (quoting of `"` and `\` not yet handled); (d) the pre-implementation review of `handler.go` required by the phase was not performed before writing: performed as self-review after writing, with abort and lifecycle tests; self-review limit declared | agent-1 (self-review, Haiku 5.5), after implementation of 1.2 | phase_02 1.2 (`Allow` contract, `Context` branch); 4.3 limits; 2.2 sanitize; tests unchanged |
| 3 | D6/RS-1 step 4 require `410` for a concluded slug; `Finish` that removed from the map gave `404`. New D17: 24h tombstone (constant), terminal slugs (concluded, claimed, expired) → `410`; beyond 24h → `404`. New `ErrGone` errors; tests `TestRegistryWaitTimeout` and `TestRegistryFinishOnce` expect `ErrGone`. No change to D1–D16 | agent-1 (self-review, Haiku 5.5), found while preparing 1.1 | overview D17; phase_02 1.1 (Claim/Finish/tombstone) and 1.2 (410 mapping); e2e step 6 unchanged |
| 2 | Reference specification: only RS-1 (upload from file with URL without stdin spec) | Added RS-2 from `Specifiche.md` (scenario 2 streaming from `tar`): D16 chunked upload with unknown length; R4 (curl without trailing `/` and from stdin) with sub-phase 0.3 before 1.2; RS-2 tests and e2e in 1.2/1.3; RS-2 through nginx in 4.2/G-FINAL; README with two scenarios. No decision of D1–D15 changed. Reason: the user specification is broader than plan rev 1 | agent-1 (self-review, Haiku 5.5); user asked "verify and adapt the plan" | overview, phase_01 (0.3), phase_02 (1.2 tests/e2e), phase_05 (4.2 and 4.3); 0.2 untouched; 0.1 unchanged |

Revisions planned but not yet done (not recorded as approved):
- D4/D8: contract correction in `phase_02.md` 1.2 already applied in planning (abort of the sender connection on expiry or copy error). Not a decision revision.
- D15: addition of the inactivity timeout in 2.2; to be approved by agent-1 when 2.2 is executed.

## 9. Blockers

| ID | Affected units | Evidence | Resolution owner | Exact next action |
|----|----------------|----------|------------------|-------------------|
| B-R1 | 0.1 (outcome), 1.2, 1.3 | R1 UNVERIFIED: curl behavior not proven in this sandbox | agent-2 with agent-1 review | run probe 0.1 in scratch directory; record branch A/B/C in overview |
| B-R4 | RESOLVED in 0.3 | R4 CONFIRMED: (A) path `/payload.bin` without slash; (B) line visible +3 ms, stdin EOF +3 s; (C) without name → `download` (limit in 4.3) | agent-2 | none |
| B-R3 | RESOLVED in 0.2 | `go doc` on go1.27.3 + `$GOROOT/api/go1.21.txt`: method present since go1.21; `go.mod` = `go 1.21` | agent-2 | none |
| B-Q1 | RESOLVED | User decision: auth only on `PUT`; `GET /<slug>` public with only the slug | user | none |
| B-R2 | RESOLVED (rev 7) | directives confirmed from nginx.org; `client_body_timeout` read: default 60s, for consecutive reads; 3600s chosen in `deploy/nginx/bashtransfer.conf` | agent-1 | none |
| B-DISC | LIMIT, non-blocking: 4.3 | Sender dropped before a receiver attaches is not detected before `BASHT_WAIT_TIMEOUT` (test: `newTestServerWait`) | agent-1 | declare in README (4.3); evaluate early body read with buffer in a later phase only if required |
| B-NET | RESOLVED (rev 7) | pull of `golang:1.27.3-bookworm`, `gcr.io/distroless/static-debian12:nonroot`, `nginx:1.27-alpine` succeeded (digests in Dockerfile and compose) | agent-2 | none |
| B-NGX | RESOLVED (rev 8): nginx HTTP proxy cuts the body when the upstream responds first (D4). Paired test: upstream early → stall at 3.9 MB + EOF; upstream late → 8 MiB OK. Solution: `stream` TLS on 443 | — | — |
| B-NGX-old | (historical) | Transfer through nginx stalls after about 0.1–0.8 MB (RS-1 and RS-2, 8 MiB and 256 MiB, then 8 MiB also on 18443). Reproduced with: compose stack; nginx with production configuration; minimal configuration with only `proxy_pass`; minimal Go upstream (response before or after reading the body); nginx host-network with local binary. The direct binary (without nginx) passes 8 MiB in 3 ms. Unproven hypotheses: sandbox interception on ports 80/443 (the minimal configuration on port 18080 forwarded the full 1 MiB, but the 8 MiB test on 18080 was not concluded). Excluded by experiment: `Expect: 100-continue`, upstream HTTP/1.0, `Connection` keep-alive, `proxy_buffer_size` 64k, TLS (same stall in HTTP), `proxy_buffering on` (breaks streaming). Socket queues not detected (`ss` no output). Experiments in scratchpad | user (decision) + agent-2 | (a) on a host without interception, or with nginx on non-standard ports, run `BASHT_E2E_BASE=https://<host> BASHT_E2E_MB=256 bash scripts/e2e.sh`; (b) if (a) passes, record G-E2E-FINAL and close 4.2/P4; if it does not pass, the problem is in the nginx path: next step is tcpdump/ss in-container, not yet done |
| B-V001-F1 | P3 attempt 2, scope, README §Performance | Rerun 2026-10-27: 256 KiB 0.896 (single session). 4 sessions: median 256 KiB 0.989. | user | RESOLVED 2026-10-27: median-of-sessions criterion accepted; P3 attempt 2 closed |
| B-V001-F2 | references 4.1, 4.2, P4 | `P4/1/complete` on `70c85be` and `ae101a1`; no commit `PEV-Unit: 4.1`; complete 4.2 absent. C2 applied | user | RESOLVED 2026-10-27: ACCEPTED with `70c85be`; `ae101a1` unchanged |
| B-V001-F3 | 2.1 | `d64bffb` has trailer `PEV-Unit: 2.2`, no own trailer for 2.1 | user | RESOLVED 2026-10-27: ACCEPTED with `d64bffb`; rev 6 deviation |
| B-SERENA | non-blocking | MCP `serena` not connected (CONNECTION_CLOSED) in this session; semantic search done with tokensave | user | restart or reconfigure serena |
| B-SUBAGENT | non-blocking | `CLAUDE_CODE_SUBAGENT_MODEL=claude-haiku-5-5` in `~/.claude/settings.json`: not verified in a new session | user | open a new session and check the subagent model |

## 10. Do-not-repeat
- Do not run `pkill -f <pattern>` when the pattern appears in the same command line: it kills the shell (exit 144). Use `pkill -x` or PID.
- Do not try again the nginx HTTP proxy for the relay: it fails with an upstream that responds before the body (rev 8). Use `stream`.
- In the sandbox environment `ss` is not installed; the port probe uses Python.
- The Bash hook blocks commands containing `curl`/`wget`: use `ctx_execute` or a script in a file.

none

## 11. Progress board

### Sub-phases
| ID | Phase file | Depends on | Status | Attempt | Evidence / reason |
|----|------------|------------|--------|---------|-------------------|
| 0.1 | phase_01.md | none | DONE | 1 | G-R1 pass branch A (curl 8.18.0); self-review §7 |
| P0 | phase_01.md | 0.1, 0.2, 0.3 | DONE | 1 | G-U0.2 pass; README N/A (no distributed behavior) |
| 0.2 | phase_01.md | 0.1 (R1 branch A) | DONE | 1 | G-BASE + G-U0.2 pass (go1.27.3, vet exit 0, list not empty); R3 CONFIRMED |
| 0.3 | phase_01.md | 0.1 (R1 branch A) | DONE | 1 | G-R4 pass: A path with name; B line before EOF; C `download` limit (overview R4) |
| 1.1 | phase_02.md | 0.2 | DONE | 1 | G-U1.1 pass: 3 config, 3 slug, 6 registry (`-race`); D17 tombstone rev 3; self-review §7 |
| 1.2 | phase_02.md | 1.1 | DONE | 1 | G-U1.2 pass (-race, 5 stable repetitions): 3 copy + 9 server; deviations rev 4 |
| 1.3 | phase_02.md | 1.2 | DONE | 1 | G-E2E1 PASS (`E2E OK`); Expect fix (rev 5) |
| 2.1 | phase_03.md | 1.2 | DONE | 1 | G-U2.1 PASS (6 auth tests, -race). Completion reference (V001-C2): `d64bffb`, trailer `PEV-Unit: 2.2` (2.1 has no own trailer; rev 6 deviation). ACCEPTED by the user 2026-10-27 (V001-F3). |
| 2.2 | phase_03.md | 1.2 | DONE | 1 | G-U2.2 PASS (sanitize table, idle timeout, headers, idle config); G-E2E2 PASS; commit `d64bffb` |
| 3.1 | phase_04.md | 1.2 | DONE | 1 | G-U3.1 PASS (smoke -race); G-BENCH: relay/baseline median 64 KiB 1.02, 256 KiB 0.956, 1 MiB 0.991 (criterion ≥ 0.90 PASS); default 256 KiB confirmed; commit `2d91a0d` |
| 4.1 | phase_05.md | P2 (or 2.1 SKIPPED), P3 | DONE | 1 | Completion reference (V001-C2): `70c85be` (P4 closure; no commit with `PEV-Unit: 4.1`). `e2ccd94` is WIP with trailer `PEV-Unit: 4.2` blocked. | build `bashtransfer:local` pass (11 s); `compose config` pass without `ports:` for app; `GET /` from internal network pass; commit `e2ccd94` (wip) |
| 4.2 | phase_05.md | 4.1; R2 (`client_body_timeout`, S1) | DONE | 2 | S1 R2 closed (`client_body_timeout` 60s default → 3600s); S2 `nginx -t` pass; transfer through nginx NOT verified: B-NGX; commit `e2ccd94` (wip, `PEV-Result: blocked`) |
| 4.3 | phase_05.md | 4.1; 4.2 | DONE | 1 | README.md (P4 closure, `70c85be`); cross-check of variables and commands; C3 bare-host name; C1 §Performance |

README obligation per phase: P0 → N/A (no distributed behavior; verified at P0 closure); P1 → pending on 4.3; P2 → pending on 4.3 (auth, limits); P3 → performance numbers in 4.3 only from §7; P4 → 4.3 complete.

### Phases
| ID | File | Closure unit | Status | Review / commit reference |
|----|------|--------------|--------|---------------------------|
| 0 | phase_01.md | P0 | DONE | P0 unit:P0:1 (self-review §7); R1, R3, R4 CONFIRMED; G-BASE, G-U0.2, G-R1, G-R4 pass |
| 1 | phase_02.md | P1 | DONE | G-P1 PASS: `go test -race -count=1 ./...` 34 PASS/0 FAIL/0 SKIP + G-E2E1 PASS; README pending on 4.3 (B-DISC, limits); self-review §7 |
| 2 | phase_03.md | P2 | DONE | G-P2 PASS: `go test -race -count=1 ./...` 35 PASS/0 FAIL/0 SKIP + G-E2E2 PASS (`E2E OK` with auth and name with spaces); README pending on 4.3 (auth, limits); self-review §7; commit `unit:P2:1` |
| 3 | phase_04.md | P3 | DONE (attempt 2, V001-C1) | Attempt 1 historical: G-BENCH on one session, commit `unit:P3:1`. Attempt 2: G-BENCH 4 sessions, median 256 KiB 0.989, criterion accepted by the user 2026-10-27; G-P3 `go test -race -count=1 -v ./...` 36 PASS/0 FAIL/0 SKIP; closure `unit:P3:2` |
| 4 | phase_05.md | P4 | DONE | G-D1, G-S2, G-E2E-FINAL PASS (256 MiB via compose stream 443); G-FINAL: see §7. Reference (V001-C2): `70c85be`. Ambiguity: `ae101a1` (state-only) has the same four trailers; choice recorded as default (not rewritten). ACCEPTED by the user 2026-10-27 (V001-F2). |

Statuses: TODO, IN_PROGRESS, IN_REVIEW, DONE, SKIPPED, BLOCKED.

### Tests
| ID/name | Owning unit | Gate | Status | Evidence |
|---------|-------------|------|--------|----------|
| TestLoadDefaults, TestLoadAuthPairRequired, TestLoadRejectsBadNumbers | 1.1 | G-U1.1 | PASS | `go test ./internal/config -run TestLoad -v -count=1` |
| TestGenerateLength, TestGenerateAlphabet, TestGenerateUnique | 1.1 | G-U1.1 | PASS | `go test ./internal/slug -v -count=1` |
| TestRegistryClaimOnce, TestRegistryFinishOnce, TestRegistryWaitTimeout, TestRegistryCapacity, TestRegistrySlugCollision, TestRegistryAbortKeepsFirst | 1.1 | G-U1.1 | PASS | `go test ./internal/relay -run TestRegistry -v -count=1 -race` |
| TestCopyFlushesEachWrite, TestCopyRoundTripBytes, TestCopyAllocsBounded | 1.2 | G-U1.2 | PASS | `go test ./internal/relay -run TestCopy -v -race` |
| TestUploadReturnsCommandsBeforeBody, TestRelayRoundTripSHA, TestGetUnknown404, TestGetSecondClaim410, TestSenderAbortFailsReceiver, TestReceiverAbortCleansSession, TestUploadMethodNotAllowed, TestUploadUnknownLength, TestUploadNameFromPath | 1.2 | G-U1.2 | PASS | `go test ./internal/server -v -race` (9 top-level; 4 subtests on sizes) |
| scripts/e2e.sh (E2E OK) | 1.3 | G-E2E1 | PASS | `bash scripts/e2e.sh` → `E2E OK` (RS-1 sha256, 410, 404, RS-2 mydir.tgz sha256) |
| TestUploadExpectContinue | 1.2 (added after e2e) | G-U1.2 | PASS | `go test ./internal/server -run TestUploadExpectContinue -race` |
| TestBasicAuth*, TestUploadRequiresAuth | 2.1 | G-U2.1 | PASS | `go test ./internal/server -run 'TestBasicAuth|TestUploadRequiresAuth' -v -count=1 -race` (6 tests) |
| TestSanitizeName, TestUploadIdleTimeout, TestDownloadHeaders, TestConfigIdleTimeout | 2.2 | G-U2.2 | PASS | `go test ./internal/server ./internal/config ./internal/relay -v -count=1 -race` |
| BenchmarkRelayLoopback, BenchmarkBaselineRelay, TestBenchHarnessSmoke | 3.1 | G-BENCH, G-U3.1 | PASS | `scripts/bench.sh` (V001-C1, 4 sessions); `TestBenchHarnessSmoke` PASS |
| compose config (app without ports) | 4.1 | G-D1 | PASS | `docker compose -f compose.yaml config` (rev 7/8) |
| docker build | 4.1 | G-D1 | PASS | `docker build` 11 s (B-NET resolved) |
| RS-1 and RS-2 via nginx | 4.2 | G-E2E-FINAL | PASS | `BASHT_E2E_BASE=https://127.0.0.1 BASHT_E2E_MB=256 bash scripts/e2e.sh` (stream 443; B-R2 and B-NGX resolved) |

### Documentation
| Document/sections | Owning unit | Status | Evidence |
|-------------------|-------------|--------|----------|
| README.md (whole) | 4.3 | DONE | C3 (scenario 1), C1 (§Performance); self-review |
| overview.md (R1, R3, R4 evidence) | 0.1, 0.2, 0.3 | DONE | R1, R3, R4 CONFIRMED |
| deploy/nginx/nginx.conf | 4.2 | DONE | rev 8 (stream); `nginx -t` PASS (G-S2) |

### Audits
| Report | Verdict | Current unresolved findings | Evidence |
|--------|---------|-----------------------------|----------|
| V001 | FAIL (historical); F1 FIXED, F2 and F3 ACCEPTED (user decision 2026-10-27), F4 and F5 FIXED | none | verify/index.md |

Audit finding details/statuses live in verify/index.md; phase progress lives here.

## 12. Suspended implementation (only while auditing unfinished work)

Snapshot state: none.
Reason/audit ID: none.
Previous current-unit record: none.
Previous checkpoint: none.
Previous plan revision / state writer: none.
Pending completion reference: none.
Evidence/review references: none.
Restoration: N/A.
