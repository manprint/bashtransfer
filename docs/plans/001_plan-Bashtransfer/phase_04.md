# Phase 3 — Misura di throughput e scelta del buffer

Intent: dimostrare con misura locale che il relay non perde più del 10% rispetto a un relay minimo equivalente con la stessa topologia, e scegliere il buffer di default con dati.
Prerequisites: P1 chiuso (`phase_02.md`). Non dipende da 2.1 né da 2.2: può partire in parallelo se la sessione lo consente; se parte dopo P2, nessun cambio di contratto.
Phase closure: P3; review by agent-1 (claude-haiku-5-5, self-review dello stesso modello) sul criterio D13.

## State and ownership contract
Read STATE.md §0–1 first for scope, ownership, recovery, checks and commit rules.
Open before edits; checkpoint at a recovery boundary; close with evidence. La misura non viene ripetuta senza cambio di codice o di ambiente (STATE §3).

## Local design context
Plan revision: 1.
- D13: buffer da `sync.Pool`, `io.CopyBuffer`, flush per blocco. Criterio di accettazione: relay ≥ 90% del baseline a parità di topologia.
- Topologia del baseline: stessa catena a due hop su loopback (mittente → relay → ricevente). Il baseline è un relay minimo in test (`net/http`, `io.CopyBuffer`, buffer uguale, flush per blocco) senza registry, senza slug, senza header extra. Confronto solo tra due hop equivalenti; un confronto con un singolo hop non misura il costo del relay.
- D2/I-1: il relay non conserva dati su disco; la misura usa generatori in memoria per il mittente e `io.Discard` per il ricevente, così il disco non entra nel risultato.
- Nessuna variazione di contratto HTTP: la fase cambia solo il valore di default di `BASHT_BUF_SIZE` se i dati lo giustificano.

## Sub-phases

### 3.1 Benchmark loopback e scelta del buffer
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** scrivere benchmark e script, eseguire, registrare risultati in STATE §7. Review agent-1: verifica che il baseline sia davvero a due hop e che il criterio sia applicato senza riscriverlo.
- **Files:**
  - NEW `internal/server/bench_test.go`: `BenchmarkRelayLoopback` (server reale via `httptest.NewServer` con `server.New`, mittente con generatore in memoria da 256 MiB, ricevente `io.Discard`) e `BenchmarkBaselineRelay` (stessa topologia con relay minimo nel file di test).
  - NEW `scripts/bench.sh`: esegue `go test -run '^$' -bench 'BenchmarkRelayLoopback|BenchmarkBaselineRelay' -benchtime 3x -count 5` e stampa `nproc`, `go version`, modello CPU da `/proc/cpuinfo`.
- **Change:**
  Preconditions: 1.2 chiuso (`server.New`, `relay.NewCopier`).
  Contract benchmark: il generatore produce byte in memoria a velocità non limitante (buffer ciclico di 1 MiB). Il misuratore riporta `b.SetBytes(256 MiB)` per iterazione. Il baseline usa lo stesso `BufSize` del relay sotto test.
  Criterio (D13): mediana dei 5 `-count` del relay ≥ 0.90 × mediana del baseline, stessa macchina, stesso momento. Il rapporto è registrato in STATE §7.
  Scelta buffer: eseguire `BenchmarkRelayLoopback` con `BASHT_BUF_SIZE` (o parametro di benchmark) 64 KiB, 256 KiB e 1 MiB. Se 256 KiB non è la mediana più alta (differenza > 5%), il supervisore decide se cambiare il default in `config.go`; decisione registrata come revisione D13. Senza dati, il default resta 256 KiB.
  Steps:
  1. S1 — scrivere i due benchmark; expected: `go vet ./internal/server` exit 0.
  2. S2 — `scripts/bench.sh`; expected: output con 5 misure per benchmark, unità `MB/s` (o `ns/op` convertito).
  3. S3 — confronto buffer (64 KiB / 256 KiB / 1 MiB) con stesso script; expected: tabella registrata in STATE §7.
  Recovery boundary: dopo S2 (risultati in STATE §6 se la sessione si interrompe).
  Failure handling: rapporto < 0.90 → nessuna modifica di criterio. Escalation a agent-1 con i numeri; ottimizzazioni non previste da D13 (es. `splice`, `sendfile`) richiedono revisione del design e non entrano in questa unità.
- **Unit tests:**
  - `TestBenchHarnessSmoke`: una iterazione di entrambi i benchmark con 1 MiB, verifica che il ricevente riceva 1 MiB esatti (protegge il benchmark da misurare zero byte).
  - Comando: `go test ./internal/server -run 'TestBenchHarnessSmoke' -v -count=1 -race`; discovery: 1.
- **e2e tests:** N/A — la misura è l'osservazione stessa; nessun comportamento utente cambia. Gate G-BENCH registrato con risultati numerici.
- **Done:** `TestBenchHarnessSmoke` pass; `scripts/bench.sh` eseguito; rapporto e tabella buffer in STATE §7; criterio D13 applicato (PASS se ≥ 0.90, altrimenti FAIL con escalation); review agent-1 registrata; unità chiusa.

## Phase gates and closure
- Required gates: G-U3.1 (`TestBenchHarnessSmoke`), G-BENCH (`scripts/bench.sh`, criterio D13), G-P3 = `go test -race -count=1 ./...` con conteggio non zero.
- Assertions: rapporto relay/baseline ≥ 0.90 con 5 campioni; default buffer confermato o revisionato con dati.
- README obligation: prestazioni misurate da citare in 4.3 solo con numeri da STATE §7 e con la macchina di misura. Responsabile 4.3.
- Every sub-phase DONE. Esegui G-P3.
- Supervisor agent-1 reviews: criterio D13, topologia del baseline, scelta del buffer.
- Chiusura P3: unità `P3` in STATE §4; fase DONE solo con G-BENCH PASS. Se FAIL: fase BLOCKED con escalation, non chiusa.
