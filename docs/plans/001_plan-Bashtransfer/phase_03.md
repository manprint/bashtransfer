# Phase 2 — Autenticazione, limiti e sanitizzazione

Intent: il server rifiuta upload non autorizzati quando l'auth è attiva, protegge da sessioni abbandonate e da nomi file pericolosi, e il download riceve gli header corretti.
Prerequisites: P1 chiuso (`phase_02.md`). Artefatti: `internal/server/handler.go` con hook `Options.Authorize`; `internal/relay/registry.go` con `Create`, `Claim`, `expire`.
Phase closure: P2; review by agent-1 (claude-haiku-5-5, self-review dello stesso modello).

## State and ownership contract
Read STATE.md §0–1 first for scope, ownership, recovery, checks and commit rules.
Open before edits; checkpoint at a recovery boundary; close with evidence. Unità 2.1 non dipende da decisioni aperte: Q1 è risolta (auth solo su PUT).

## Local design context
Plan revision: 1.
- D9: auth opzionale, coppia `BASHT_AUTH_USER`/`BASHT_AUTH_PASSWORD`, solo sul `PUT` (Q1 risolta). Confronto costante su digest SHA-256 (`subtle.ConstantTimeCompare` sui due digest). Risposta `401` con `WWW-Authenticate: Basic realm="bashtransfer"`.
- D7: timeout di attesa già in registry (1.1). Qui si aggiunge il timeout di inattività del relay (vedi 2.2).
- D5: sanitizzazione nome file (regole in overview D5), default `download`.
- D6: header del ricevente; `Content-Length` solo se noto.
- D8: abort protocol di fase 1 invariato.
- D11: limite sessioni già in registry; qui si verifica il codice `503`.

Q1 risolta dall'utente: auth solo su `PUT`; download pubblico con solo lo slug.

## Sub-phases

### 2.1 Basic auth sul percorso di upload (solo PUT)
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** implementazione; review agent-1 sul contratto `Authorize` e sul test di timing (nessun confronto non costante).
- **Files:**
  - WRITE `internal/config/config.go` (verifica validazione coppia già presente da 1.1; nessuna modifica attesa).
  - NEW `internal/server/auth.go`, `internal/server/auth_test.go`: `func BasicAuth(user, password string) func(*http.Request) bool`.
  - WRITE `cmd/bashtransfer/main.go`: collegare `Options.Authorize` quando la config ha auth.
- **Change:**
  Preconditions: 1.2 chiuso. Q1 risolta dall'utente: auth solo su `PUT` (D9). Il ramo `GET /<slug>` non viene toccato.
  Contract: `BasicAuth` calcola `sha256(user)`, `sha256(password)` una volta alla costruzione; per ogni richiesta legge `r.BasicAuth()`, calcola i digest dei valori ricevuti e confronta con `subtle.ConstantTimeCompare` **entrambi** (nessun early return tra user e password). `ok` solo se entrambi uguali.
  Steps:
  1. S1 — `BasicAuth` con digest precalcolati; expected: funzione pura testabile.
  2. S2 — collegamento in `main.go` quando `cfg.AuthUser != ""`; expected: senza auth nessun comportamento cambia (test regressione 1.2 invariati).
  3. S3 — test di regressione: `GET /<slug>` con auth attiva funziona senza credenziali; expected: stesso comportamento di 1.2.
  Recovery boundary: dopo S1 (checkpoint con file creati).
  Failure handling: un fallimento di test di 2.1 va escalato a agent-1; nessuna dipendenza da 2.2.
- **Unit tests:**
  - `TestBasicAuthAcceptsCorrect`, `TestBasicAuthRejectsWrongUser`, `TestBasicAuthRejectsWrongPassword`, `TestBasicAuthRejectsMissingHeader` (401 con `WWW-Authenticate`), `TestBasicAuthConstantWork` (verifica che il codice calcola entrambi i confronti: test con mock di funzione di confronto contabile, oppure revisione del diff da agent-1 registrata come evidenza).
  - Test server: `TestUploadRequiresAuth` (PUT senza credenziali → 401, con credenziali → 200 con comandi).
  - Comando: `go test ./internal/server -run 'TestBasicAuth|TestUploadRequiresAuth' -v -count=1 -race`; discovery: 6.
- **e2e tests:** `scripts/e2e.sh` con variabili `BASHT_AUTH_USER`/`BASHT_AUTH_PASSWORD` impostate: upload con `-u` riesce; upload senza credenziali → 401; download senza credenziali riesce. Gate G-E2E2.
- **Done:** test e e2e pass con conteggio; review agent-1 registrata; unità chiusa.

### 2.2 Limiti di relay, sanitizzazione del nome e header del ricevente
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** implementazione dei punti sotto; review agent-1 sul timeout di inattività (lifecycle, rischio di goroutine bloccate).
- **Files:**
  - WRITE `internal/relay/copy.go`: deadline di inattività per lettura del body.
  - WRITE `internal/server/handler.go`: sanitizzazione del nome, header del ricevente, mapping errori.
  - NEW `internal/server/name.go`, `internal/server/name_test.go`: `func SanitizeName(raw string) string`.
  - WRITE `internal/config/config.go`, `config_test.go`: campo `IdleTimeout time.Duration`, env `BASHT_IDLE_TIMEOUT`, default `2m`, minimo `1s`. Aggiunta registrata come decisione D15 (vedi sotto).
- **Change:**
  Preconditions: 1.2 chiuso.
  D15 (nuova decisione del supervisore, dentro i requisiti approvati): il relay ha un timeout di inattività `BASHT_IDLE_TIMEOUT`: se il mittente non produce byte per questo intervallo durante il relay, la copia fallisce con `ErrIdle` (abort di mittente e ricevente, I-3, I-4). Motivo: senza questo, un mittente fermo lascia il ricevente bloccato su `Finished()` senza limite (rischio in overview § Risks). Implementazione: `http.NewResponseController(w).SetReadDeadline(time.Now().Add(idle))` prima di ogni `Read` del body nel mittente (tramite wrapper `deadlineReader` che il `Copier` usa tramite `io.Reader`). Non è una modifica del contratto HTTP pubblico.
  Contract sanitizzazione (`SanitizeName`): 1) prende solo l'ultimo elemento dopo `/` e `\`; 2) rimuove caratteri di controllo (`< 0x20`, `0x7f`) e `"`, `\`; 3) rimuove `..` come nome e spazi iniziali/finali; 4) tronca a 128 caratteri in rune; 5) se vuoto → `download`.
  Contract header ricevente: `Content-Disposition: attachment; filename="<SanitizeName>"`; il nome nel blocco comandi usa lo stesso valore sanitizzato (coerenza: stesso nome in comandi e header, D5).
  Contract errori: `ErrIdle` → abort; `ErrSlugCollision`/`ErrCapacity` → 503; `ErrClaimed` → 410; `ErrNotFound` → 404; path slug non valido → 404 senza accesso al registry.
  Steps:
  1. S1 — `SanitizeName` con tabella di casi; expected: test passano.
  2. S2 — `deadlineReader` + `ErrIdle`; expected: test di inattività passa entro il tempo atteso.
  3. S3 — header ricevente e nome coerente; expected: `TestDownloadHeaders`.
  Recovery boundary: dopo S2 (checkpoint).
  Failure handling: se `SetReadDeadline` non è supportato dall'adattatore in un test, usare un adattatore di test che implementa il metodo, senza cambiare il contratto di produzione.
- **Unit tests:**
  - `TestSanitizeName` (tabella): `../../etc/passwd` → `passwd`; `a"b\c.zip` → `abc.zip`; `\x01x` → `x`; stringa vuota → `download`; `..` → `download`; 200 caratteri → 128 rune; nome UTF-8 `città.zip` invariato.
  - `TestUploadIdleTimeout`: mittente invia 1 KiB, poi si ferma; con `BASHT_IDLE_TIMEOUT=200ms`, il ricevente riceve errore entro 3 s; registry `Len()==0`.
  - `TestDownloadHeaders`: con Content-Length dichiarato → header `Content-Length` presente; senza (-1) → assente; `Content-Disposition` coerente con nome nei comandi.
  - `TestConfigIdleTimeout`: default 2m, `0s` → errore, `500ms` → errore (minimo 1 s).
  - Comando: `go test ./internal/server ./internal/config ./internal/relay -v -count=1 -race`; discovery: 4 + 1 + 1 + 1 + tutti i test di fase 1 (nessuna regressione).
- **e2e tests:** `scripts/e2e.sh` esteso con upload di nome `"strano nome.zip"` (con spazi) → download con `-o` esplicito; il nome nei comandi è sanitizzato coerentemente. Gate G-E2E2.
- **Done:** test listati pass con `-race`; regressione di fase 1 invariata; revisione agent-1 registrata su D15; unità chiusa.

## Phase gates and closure
- Required gates: G-U2.2 (comando sopra), G-P2 = `go test -race -count=1 ./...` con conteggio non zero + `scripts/e2e.sh` esteso (G-E2E2).
- Assertions: D5 completo, D7 con inattività (D15), D9 solo su PUT.
- README obligation: comportamento auth e limiti da documentare in 4.3 (responsabile). Verifica di chiusura P2: obbligo pendente su 4.3 in STATE §11.
- Every sub-phase DONE. Se 2.1 o 2.2 fallisce un gate, P2 **non** chiude; 3.1 (indipendente) può procedere.
- Supervisor agent-1 reviews: D15 e lifecycle dell'inattività; contratto auth (2.1) prima della chiusura.
- Chiusura P2: unità `P2` in STATE §4 solo quando 2.1 e 2.2 sono DONE.
