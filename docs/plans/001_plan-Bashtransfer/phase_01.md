# Phase 0 — Probe locali e skeleton Go

Intent: chiudere le due verifiche locali che determinano il contratto di upload (R1 curl, R3 Go full duplex) e creare lo scheletro del modulo Go verificabile con `go vet`.
Prerequisites: none.
Phase closure: P0; review by agent-1 (claude-haiku-5-5, self-review dello stesso modello, vedi overview § Model-assignment summary).

## State and ownership contract
Read STATE.md §0–1 first for scope, ownership, recovery, checks and commit rules.
Open before edits; checkpoint at a recovery boundary; close with evidence. Il probe di 0.1 non scrive nel repository: solo `overview.md` (sezione R1) viene aggiornata. Nessun commit (planning e piano non committano).

## Local design context
Plan revision: 1.
- D4: il server risponde al `PUT` con i comandi prima di leggere il body. Vale solo se curl mostra la risposta durante l'upload (R1-A).
- D5: nome file da path `PUT /<name>`; se curl non invia il nome, default `download` e escalation (R1-C).
- R1: curl upload behaviour. Esito da registrare in overview.md dopo 0.1. Branch B (risposta visibile solo a fine upload) = STOP e escalation all'utente.
- R3: `http.ResponseController.EnableFullDuplex` deve esistere nella toolchain. Se assente: STOP, D4 va rivisto dal supervisore prima di 1.2.
- Nessuna variabile di configurazione o endpoint viene implementato in questa fase.

## Sub-phases

### 0.1 Probe R1: comportamento di curl con upload verso la radice
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** eseguire il probe e registrare l'esito in overview.md (R1). Review prima di scegliere il branch: agent-1 (claude-haiku-5-5), controlla log e conclusione.
- **Files:**
  - READ: `Specifiche.md` (solo riferimento al comando `curl -T`).
  - WRITE: `docs/plans/001_plan-Bashtransfer/overview.md`, solo la riga R1 e il paragrafo `### R1`.
  - NEW (fuori dal repository): directory di scratch `/tmp/claude-1000/-mnt-fabio-dati-Git-SperimentazioniAI-bashtransfer/909b046d-e29f-47a1-9fde-4c4d68b9a2b7/scratchpad/r1/` con `probe_server.py`, `make_payload.py` (o equivalente), `probe.log`, `payload.bin`.
- **Change:**
  Preconditions: nessuna. Stato atteso: repository senza commit, `Specifiche.md` non tracciato.
  Contract: il probe non deve collegarsi a nessun host diverso da `127.0.0.1`. Porta libera scelta dall'agente (es. 18080), registrata nel log.
  Scope: solo test di comportamento di curl locale; nessuna modifica al repository tranne overview R1.
  Steps:
  1. S1 — creare `payload.bin` casuale di 8 MiB (es. `os.urandom`); expected: file di dimensione 8388608 byte.
  2. S2 — avviare `probe_server.py` su `127.0.0.1:<porta>`. Per ogni `PUT`: loggare con timestamp monotono la request line, gli header e l'evento `PUT_START`; inviare subito `HTTP/1.1 200 OK` con `Content-Type: text/plain` e corpo `COMANDI-DI-PROVA`, flush; attendere 3 s; leggere tutto il body loggando `BODY_END` con byte totali; expected: log con sequenza `PUT_START` → risposta inviata → `BODY_END`.
  3. S3 — eseguire il client `curl -T payload.bin http://127.0.0.1:<porta>/` con stdout su pipe; registrare il tempo di arrivo della prima riga stdout rispetto a `BODY_END`. Expected: `COMANDI-DI-PROVA` arriva prima di `BODY_END` ⇒ R1-B è escluso (R1 ha almeno "visibile in corso"); se arriva solo dopo `BODY_END`, R1-B.
  4. S4 — eseguire `curl -T payload.bin http://127.0.0.1:<porta>/probe-name.zip` e verificare nel log il path `/probe-name.zip`; poi, separatamente, `curl -T payload.bin http://127.0.0.1:<porta>/` e registrare il path ricevuto. Expected: path ricevuto registrato in entrambi i casi (senza ipotesi sul risultato).
  5. S5 — chiudere il server; aggiornare overview R1 con: versione curl (`curl --version`), path ricevuti, tempi di S3, branch scelto (A, B o C) secondo le regole in overview § Research and evidence / R1.
  Expected: R1 passa da UNVERIFIED a CONFIRMED per le condizioni provate; annotare che il risultato vale per la versione di curl della sandbox.
  Recovery boundary: prima di S2 (processo in background): registrare PID e porta in STATE §6 e pulire il processo in S5.
  Failure handling: se la porta è occupata, sceglierne un'altra e registrarla. Se curl non esiste nel PATH, bloccare con evidenza e non inventare il comportamento. Branch B: STOP, `Scope result: BLOCKED`, escalation all'utente con log e richiesta di decisione (non sostituire D4 da soli).
  Nota operativa: l'hook del progetto reindirizza le chiamate `curl` nella shell Bash; se il comando `curl` viene bloccato, eseguire il probe tramite lo strumento di esecuzione sandbox (`ctx_execute` con linguaggio shell), mantenendo il traffico su `127.0.0.1`.
- **Unit tests:** N/A — il probe non è codice di prodotto. Controllo di discovery: il log contiene almeno gli eventi `PUT_START` e `BODY_END` per ogni richiesta (verificato con grep del log, conteggio ≥ 2 PUT per S3 e S4 combinati).
- **e2e tests:** N/A — nessun comportamento osservabile di prodotto; il probe è l'osservazione stessa.
- **Done:** overview R1 aggiornato con evidenza locale e branch; processo server terminato; nessun file nel repository tranne overview R1; review agent-1 registrata in STATE §7; unità chiusa in STATE §4 con `uncommitted`.

### 0.2 Skeleton Go e verifica R3
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** creare lo scheletro del modulo e registrare `go version` e l'output di `go doc` in overview R3. Review: agent-1 controlla che la versione minima scelta sia coerente con R3.
- **Files:**
  - READ: nessun file del progetto (solo `go doc`).
  - WRITE: `docs/plans/001_plan-Bashtransfer/overview.md` (R3, paragrafo `### R3`).
  - NEW: `go.mod` alla radice (`module bashtransfer`, direttiva `go` = versione minima fissata qui).
  - NEW: `cmd/bashtransfer/main.go` — solo `package main` e `func main() {}` con commento che dice che è segnaposto fino a 1.3.
  - NEW: `.gitignore` alla radice con `bin/` e `*.test`.
- **Change:**
  Preconditions: 0.1 chiuso o non bloccante (0.2 non dipende dai risultati di curl).
  Contract: `go.mod` deve dichiarare una versione `go` ≥ quella che espone `EnableFullDuplex` (verificata con `go doc`). Nessuna dipendenza esterna (nessun `require`).
  Scope: solo scheletro; nessun codice di prodotto. Nessuna chiamata di rete (nessun `go mod download`).
  Steps:
  1. S1 — eseguire `go version` e registrare la versione esatta; expected: versione stampata. Se Go manca: STOP, blocker `G-BASE` non eseguibile, escalation (installazione richiede autorizzazione).
  2. S2 — eseguire `go doc net/http.ResponseController.EnableFullDuplex`; registrare la firma e l'eventuale messaggio di versione. Expected: firma presente; se assente → STOP e escalation (D4 da rivedere).
  3. S3 — creare `go.mod` con `module bashtransfer` e `go <versione minima>`, dove la versione minima è la prima che espone il metodo (dichiarata in overview R3 con fonte locale). Expected: `go.mod` valido.
  4. S4 — creare `cmd/bashtransfer/main.go` e `.gitignore`. Expected: `go vet ./...` esce con codice 0 e `go list ./...` elenca `bashtransfer/cmd/bashtransfer`.
  Recovery boundary: nessuno (tutte le modifiche sono piccole e reversibili; l'unità si chiude in una sessione).
  Failure handling: errore di `go vet` → correggere solo lo scheletro; nessun test richiesto.
- **Unit tests:** N/A — scheletro senza logica. Gate G-BASE sostituisce: `go vet ./...` exit 0 e `go list ./...` output non vuoto.
- **e2e tests:** N/A — nessun comportamento osservabile.
- **Done:** `go.mod`, `cmd/bashtransfer/main.go`, `.gitignore` presenti; G-BASE pass registrato in STATE §7 con versione Go; R3 CONFIRMED (fonte locale `go doc`, versione toolchain); review agent-1 in STATE §7; unità chiusa in STATE §4 con `uncommitted`.

## Phase gates and closure
- Required gates: G-BASE (registrato in STATE §3), G-U0.2 = `go vet ./...` (exit 0).
- Assertions: R1 e R3 non più UNVERIFIED; branch R1 scelto e registrato.
- README obligation: nessun comportamento distribuito in questa fase; README non ancora creato; verificato come N/A con motivazione in STATE §11 "Documentation".
- Every sub-phase DONE or SKIPPED with justification. Esegui il gate di fase ora.
- Supervisor agent-1 (claude-haiku-5-5) reviews: evidenze R1 e R3, branch scelto. Review registrata come self-review dello stesso modello.
- Chiusura P0: unità `P0` in STATE §4, fase DONE in STATE §11 solo se R1 CONFIRMED con branch A e R3 CONFIRMED. Con branch B o R3 assente: fase BLOCKED.
