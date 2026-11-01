# Phase 3 — Throughput measurement and buffer choice

Intent: demonstrate with a local measurement that the relay loses no more than 10% compared with an equivalent minimal relay with the same topology, and choose the default buffer with data.
Prerequisites: P1 closed (`phase_02.md`). Does not depend on 2.1 or 2.2: it can start in parallel if the session allows; if it starts after P2, no contract change.
Phase closure: P3; review by agent-1 (claude-haiku-5-5, self-review by the same model) on the D13 criterion.

## State and ownership contract
Read STATE.md §0–1 first for scope, ownership, recovery, checks and commit rules.
Open before edits; checkpoint at a recovery boundary; close with evidence. The measurement is not repeated without a change of code or environment (STATE §3).

## Local design context
Plan revision: 1.
- D13: buffer from `sync.Pool`, `io.CopyBuffer`, flush per block. Acceptance criterion: relay ≥ 90% of the baseline with the same topology.
- Baseline topology: the same two-hop chain on loopback (sender → relay → receiver). The baseline is a minimal relay in a test (`net/http`, `io.CopyBuffer`, same buffer, flush per block) without registry, without slug, without extra headers. Comparison only between two equivalent hops; a comparison with a single hop does not measure the cost of the relay.
- D2/I-1: the relay stores no data on disk; the measurement uses in-memory generators for the sender and `io.Discard` for the receiver, so the disk does not enter the result.
- No change to the HTTP contract: the phase only changes the default value of `BASHT_BUF_SIZE` if the data justifies it.

## Sub-phases

### 3.1 Loopback benchmark and buffer choice
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** write the benchmarks and the script, run them, record results in STATE §7. Review by agent-1: verify that the baseline really has two hops and that the criterion is applied without being rewritten.
- **Files:**
  - NEW `internal/server/bench_test.go`: `BenchmarkRelayLoopback` (real server via `httptest.NewServer` with `server.New`, sender with an in-memory generator of 256 MiB, receiver `io.Discard`) and `BenchmarkBaselineRelay` (same topology with a minimal relay in the test file).
  - NEW `scripts/bench.sh`: runs `go test -run '^$' -bench 'BenchmarkRelayLoopback|BenchmarkBaselineRelay' -benchtime 3x -count 5` and prints `nproc`, `go version`, CPU model from `/proc/cpuinfo`.
- **Change:**
  Preconditions: 1.2 closed (`server.New`, `relay.NewCopier`).
  Benchmark contract: the generator produces bytes in memory at a non-limiting speed (1 MiB cyclic buffer). The meter reports `b.SetBytes(256 MiB)` per iteration. The baseline uses the same `BufSize` as the relay under test.
  Criterion (D13): median of the 5 `-count` runs of the relay ≥ 0.90 × median of the baseline, same machine, same time. The ratio is recorded in STATE §7.
  Buffer choice: run `BenchmarkRelayLoopback` with `BASHT_BUF_SIZE` (or a benchmark parameter) at 64 KiB, 256 KiB and 1 MiB. If 256 KiB is not the highest median (difference > 5%), the supervisor decides whether to change the default in `config.go`; the decision is recorded as a revision of D13. Without data, the default stays 256 KiB.
  Steps:
  1. S1 — write the two benchmarks; expected: `go vet ./internal/server` exit 0.
  2. S2 — `scripts/bench.sh`; expected: output with 5 measurements per benchmark, unit `MB/s` (or `ns/op` converted).
  3. S3 — buffer comparison (64 KiB / 256 KiB / 1 MiB) with the same script; expected: table recorded in STATE §7.
  Recovery boundary: after S2 (results in STATE §6 if the session is interrupted).
  Failure handling: ratio < 0.90 → no change to the criterion. Escalation to agent-1 with the numbers; optimisations not foreseen by D13 (e.g. `splice`, `sendfile`) require a design revision and are not part of this unit.
- **Unit tests:**
  - `TestBenchHarnessSmoke`: one iteration of both benchmarks with 1 MiB, verifies that the receiver gets exactly 1 MiB (protects the benchmark from measuring zero bytes).
  - Command: `go test ./internal/server -run 'TestBenchHarnessSmoke' -v -count=1 -race`; discovery: 1.
- **e2e tests:** N/A — the measurement is itself the observation; no user-facing behaviour changes. Gate G-BENCH recorded with numerical results.
- **Done:** `TestBenchHarnessSmoke` pass; `scripts/bench.sh` run; ratio and buffer table in STATE §7; D13 criterion applied (PASS if ≥ 0.90, otherwise FAIL with escalation); agent-1 review recorded; unit closed.

## Phase gates and closure
- Required gates: G-U3.1 (`TestBenchHarnessSmoke`), G-BENCH (`scripts/bench.sh`, D13 criterion), G-P3 = `go test -race -count=1 ./...` with non-zero count.
- Assertions: relay/baseline ratio ≥ 0.90 with 5 samples; buffer default confirmed or revised with data.
- README obligation: performance measured to be cited in 4.3 only with numbers from STATE §7 and with the measurement machine. Owner 4.3.
- Every sub-phase DONE. Run G-P3.
- Supervisor agent-1 reviews: D13 criterion, baseline topology, buffer choice.
- Closure of P3: unit `P3` in STATE §4; phase DONE only with G-BENCH PASS. If FAIL: phase BLOCKED with escalation, not closed.
