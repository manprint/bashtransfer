# Phase 1 — Relay core: configurazione, slug, registry, upload/download in streaming

Intent: il server locale esegue RS-1 senza autenticazione: upload con comandi immediati, download con sha256 identico, secondo download con `410`.
Prerequisites: P0 chiuso con R1 branch A e R3 CONFIRMED (fase 0, `phase_01.md`). Artefatti: `go.mod` con `module bashtransfer`, `cmd/bashtransfer/main.go` placeholder.
Phase closure: P1; review by agent-1 (claude-haiku-5-5, self-review dello stesso modello), con revisione ad alto rischio prima di scrivere `handler.go` (dopo `copy.go` e registry; concorrenza e lifecycle).

## State and ownership contract
Read STATE.md §0–1 first for scope, ownership, recovery, checks and commit rules.
Open before edits; checkpoint at a recovery boundary; close with evidence. Il worker (agent-2, implementazione) esegue l'unità OPEN; la sessione esecutrice è l'unico scrittore di STATE.md; design mancante o test in conflitto → agent-1 (supervisore).

## Local design context
Plan revision: 2 (upload da stdin D16; test e e2e RS-2; prerequisito R4 da 0.3).
- D1: Go, `net/http`, nessuna dipendenza esterna.
- D2: relay in memoria, un solo ricevente per slug, nessun file su disco.
- D3: slug 10 caratteri da alfabeto 62, `crypto/rand`, rejection sampling con soglia 248 (= 62·4): byte `b` accettato solo se `b < 248`, carattere = `alphabet[b % 62]`. Collisione con sessione viva: fino a 5 tentativi, poi errore `ErrSlugCollision` (→ 503).
- D4: il `PUT` abilita full duplex (`http.ResponseController.EnableFullDuplex`), scrive `200 text/plain` con i comandi e fa flush prima di leggere il body. Il body viene letto solo dopo l'attach di un ricevente.
- D5: nome file = segmento path, sanitizzato (basename, niente caratteri di controllo, `"`, `\`, max 128); default `download`. Branch R1 da 0.1 già scelto (A).
- D6: primo claim vince; `200` con `Content-Type: application/octet-stream`, `Content-Disposition: attachment; filename="<name>"`, `Content-Length` solo se noto; slug ignoto `404`; già claimed o concluso `410`.
- D7: sessione in attesa scade dopo `WaitTimeout`: rimossa dal registry; il mittente riceve un'interruzione (abort).
- D8: fallimento a metà relay → il ricevente viene abortito con `panic(http.ErrAbortHandler)` (chunk finale non inviato, il client vede errore). Non usare `return` normale in questo caso: Go chiuderebbe in modo pulito il body troncato.
- D13: buffer da pool; flush dopo ogni `Write` al ricevente.
- I-1 (no disco, un buffer per relay), I-2 (un ricevente per slug), I-3 (rimozione una sola volta), I-4 (sha256 o errore), I-5 (slug unici tra sessioni vive).

Abort protocol (vincolante per 1.1 e 1.2):
- `Session.Abort(err)`: imposta `err` solo alla prima chiamata, non chiude nulla.
- `Registry.Finish(s)`: idempotente (`sync.Once`); rimuove `s` dalla mappa se è ancora quella registrata, ferma il timer, chiude `Finished()`. Chiamata **solo dalla goroutine del mittente, dopo l'ultima scrittura verso il ricevente**. Mai dal ricevente. Questo evita che il ricevente ritorni mentre il mittente sta ancora scrivendo nel suo `ResponseWriter`.
- Il ricevente attende `Finished()`; alla cancellazione del contesto della sua richiesta chiama `Abort(ErrReceiverGone)` e continua ad attendere `Finished()`.
- Il mittente, se termina senza relay completo (ctx annullato, errore di copia, scadenza), chiama `Abort(...)` prima di `Finish`.

## Sub-phases

### 1.1 Config, slug e registry (logica pura, senza HTTP)
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** implementazione seguendo il contratto sotto; review agent-1 sulla correttezza di `Finish`/`Abort` (I-3) prima di chiudere.
- **Files:**
  - NEW `internal/config/config.go`, `internal/config/config_test.go`.
  - NEW `internal/slug/slug.go`, `internal/slug/slug_test.go`.
  - NEW `internal/relay/registry.go`, `internal/relay/registry_test.go`.
- **Change:**
  Preconditions: P0 chiuso.
  Contract config: `func Load(getenv func(string) string) (Config, error)`. `Config{Listen string; PublicURL string; AuthUser, AuthPassword string; WaitTimeout time.Duration; MaxSessions int; BufSize int}`. Default: `:8080`, `""`, `""`, `""`, `1h`, `1000`, `262144`. Errori: `BASHT_AUTH_USER` senza password o viceversa; `BASHT_WAIT_TIMEOUT` non parsabile o ≤ 0; `BASHT_MAX_SESSIONS` < 1; `BASHT_BUF_SIZE` < 4096 o > 4194304. Messaggio di errore nomina la variabile.
  Contract slug: `func New() (string, error)` come D3. Alfabeto esatto `0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz`.
  Contract registry:
  - `type Session struct{ Slug, Name string; Length int64 }` (Length -1 se ignota) con metodi `Attached() <-chan Receiver`, `Expired() <-chan struct{}`, `Finished() <-chan struct{}`, `Err() error`, `Abort(err error)`.
  - `type Receiver interface{ io.Writer; Flush() }`, dichiarata in `registry.go`, usata da 1.2.
  - `func NewRegistry(maxSessions int, waitTimeout time.Duration) *Registry` con campo interno `gen func() (string, error)` (default `slug.New`, sostituibile nei test).
  - `Create(name string, length int64) (*Session, error)`: `ErrCapacity` se mappa piena (D11), `ErrSlugCollision` dopo 5 tentativi; avvia `time.AfterFunc(waitTimeout, expire)`.
  - `Claim(slug string, rcv Receiver) (*Session, error)`: `ErrNotFound` se slug non in mappa; `ErrClaimed` se già claimed; altrimenti marca claimed, ferma il timer, invia `rcv` su `Attached()` (canale buffered 1, mai bloccante).
  - `expire(s)`: se ancora in attesa (non claimed) rimuove dalla mappa e chiude `Expired()`; idempotente.
  - `Finish(s)`: come da abort protocol.
  - `Len() int` per i test (numero sessioni in mappa).
  Steps:
  1. S1 — `config.Load` con validazione; expected: test di default e di errori passano.
  2. S2 — `slug.New` con rejection sampling; expected: lunghezza 10, alfabeto corretto.
  3. S3 — `Registry` con mutex unico per mappa e stato sessione; `Claim` e `expire` sotto lo stesso lock; `Finish` con `sync.Once`; expected: nessun campo di sessione acceduto fuori lock tranne i canali.
  Recovery boundary: dopo S3 (logica registry completa), checkpoint in STATE §6 con elenco file creati.
  Failure handling: se il test di concorrenza rivela doppio claim, non allentare il test: sistemare il lock e registrare il fallimento in STATE §10 (do-not-repeat).
- **Unit tests:**
  - `internal/config`: `TestLoadDefaults` (nessuna variabile → valori default D10), `TestLoadAuthPairRequired` (solo user → errore con nome variabile; solo password → errore), `TestLoadRejectsBadNumbers` (table test: timeout `abc`, `0s`; sessions `0`; buf `1024`, `8388609`). Comando: `go test ./internal/config -run 'TestLoad' -v -count=1`; discovery atteso: 3 test.
  - `internal/slug`: `TestGenerateLength` (len 10), `TestGenerateAlphabet` (ogni carattere nell'alfabeto, 1000 campioni), `TestGenerateUnique` (20000 campioni, nessun duplicato). Comando: `go test ./internal/slug -v -count=1`; discovery 3.
  - `internal/relay`: `TestRegistryClaimOnce` (due goroutine chiamano `Claim` con due receiver fittizi: esattamente una `nil` error, l'altra `ErrClaimed`; I-2), `TestRegistryFinishOnce` (`Finish` due volte, nessun panic, `Len()==0`, `Finished()` chiuso; I-3), `TestRegistryWaitTimeout` (waitTimeout 50 ms, `Expired()` chiuso entro 2 s, poi `Claim` → `ErrNotFound`; I-3), `TestRegistryCapacity` (max 2: terzo `Create` → `ErrCapacity`; dopo `Finish` di uno, `Create` riesce), `TestRegistrySlugCollision` (gen fittizio che restituisce sempre lo stesso slug: secondo `Create` ottiene `ErrSlugCollision` dopo 5 tentativi; I-5), `TestRegistryAbortKeepsFirst` (due `Abort` → `Err()` è il primo). Comando: `go test ./internal/relay -run 'TestRegistry' -v -count=1 -race`; discovery 6.
- **e2e tests:** N/A — nessun comportamento HTTP in questa unità.
- **Done:** file NEW presenti; discovery attesa raggiunta (3 + 3 + 6 test); `go vet ./...` exit 0; G-U1.1 pass in STATE §7 con conteggio e revisione; review agent-1 su `Finish`/`Abort` in STATE §7; unità chiusa in STATE §4 con `uncommitted`.

### 1.2 Relay HTTP: upload con comandi immediati, download in streaming
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** implementazione del contratto di D4, D6, D8. Revisione ad alto rischio da agent-1 **prima** dell'implementazione di `handler.go` (concorrenza e lifecycle del ricevente) e dopo, sul diff. Registrare entrambe le revisioni in STATE §7.
- **Files:**
  - READ: `internal/relay/registry.go` (1.1), `internal/config/config.go` (1.1).
  - NEW `internal/relay/copy.go`, `internal/relay/copy_test.go`: `type Copier struct{ pool sync.Pool; size int }`, `func NewCopier(size int) *Copier`, `func (c *Copier) Copy(dst Receiver, src io.Reader) (int64, error)` con `io.CopyBuffer` su buffer da pool e `dst.Flush()` dopo ogni `Write` riuscita (implementato come wrapper `flushWriter`).
  - NEW `internal/server/handler.go`, `internal/server/handler_test.go`: `type Options struct{ PublicURL string; Authorize func(*http.Request) bool }` (nil = nessun controllo; 2.1 lo collega), `func New(reg *relay.Registry, copier *relay.Copier, opts Options) http.Handler`.
  - NEW `internal/server/receiver.go`: adattatore `http.ResponseWriter` → `relay.Receiver` (`Write`, `Flush` via `http.NewResponseController(w).Flush()`), più il flag `started`.
- **Change:**
  Preconditions: 1.1 chiuso (`Registry`, `Session` API definiti come sopra).
  Steps:
  1. S1 — `copy.go` (`Copier`, `flushWriter`); expected: `TestCopyFlushesEachWrite`, `TestCopyRoundTripBytes`, `TestCopyAllocsBounded` pass con `-race`. Recovery boundary: checkpoint in STATE §6.
  2. S2 — `receiver.go` (adattatore con `started`, `WriteHeader`, `Write`, `Flush`); expected: compila; nessun test dedicato, coperto da `TestGetSecondClaim410` e `TestRelayRoundTripSHA`.
  3. S3 — `handler.go` ramo upload (`PUT`): auth hook, full duplex, `Create`, blocco comandi, select su `Attached`/`Expired`/contesto; expected: `TestUploadReturnsCommandsBeforeBody`, `TestUploadMethodNotAllowed` pass. Recovery boundary: checkpoint dopo S3.
  4. S4 — `handler.go` ramo download (`GET`): `Claim`, attesa `Finished`, abort; expected: `TestGetUnknown404`, `TestGetSecondClaim410`, `TestRelayRoundTripSHA` pass.
  5. S5 — test di abort `TestSenderAbortFailsReceiver`, `TestReceiverAbortCleansSession` (meccanica sotto); expected: pass entro i tempi indicati. Recovery boundary: checkpoint dopo S5.
  Upload da stdin (D16, RS-2): `Transfer-Encoding: chunked`, `r.ContentLength == -1` ammesso; nessun limite di dimensione; durata limitata solo da D15; nome dal path.
  Contract upload (`PUT /` e `PUT /<name>`; altri path → 404; altri metodi → 405 con `Allow: PUT`):
  - Se `Authorize` non nil e restituisce false → 401 con corpo `unauthorized` (`WWW-Authenticate` aggiunto in 2.1; la logica di 2.1 riusa questo punto).
  - `rc := http.NewResponseController(w)`; `rc.EnableFullDuplex()` errore → 500 (non dovrebbe accadere, R3 verificato).
  - `length := r.ContentLength` (-1 se ignota; 0 accettato). `s, err := reg.Create(name, length)`: `ErrCapacity`/`ErrSlugCollision` → 503 con corpo `service unavailable`, nessuna sessione.
  - `ok := false; defer func(){ if !ok { s.Abort(ErrSenderGone) }; reg.Finish(s) }()`. Errori sentinella `ErrSenderGone`, `ErrReceiverGone`, `ErrLengthMismatch` aggiunti a `registry.go` in questa unità (WRITE).
  - Header `Content-Type: text/plain; charset=utf-8`; scrivere il blocco comandi (formato in `overview.md` § Interfaces: `curl -o "<name>" "<base>/<slug>"` e `wget -O "<name>" "<base>/<slug>"`, dove `<base>` = `PublicURL` se impostato; altrimenti schema = valore di `X-Forwarded-Proto` se è `http` o `https` (altri valori ignorati), altrimenti `https` se `r.TLS != nil`, altrimenti `http`; host = `r.Host`); `rc.Flush()`.
  - `select`:
    - `rcv := <-s.Attached()`: impostare header del ricevente (`Content-Type: application/octet-stream`, `Content-Disposition: attachment; filename="<name>"`, `Content-Length` se `s.Length >= 0`), `WriteHeader(200)` sul ricevente (tramite `rcv` adattatore), poi `n, err := copier.Copy(rcv, r.Body)`. Se `err != nil` oppure (`s.Length >= 0` e `n != s.Length`): `s.Abort(err)` (o `ErrLengthMismatch`) e `panic(http.ErrAbortHandler)` nel mittente, che abortisce la connessione del mittente (`curl` esce con errore). Solo con copia completa e valida: `ok = true`, ritorno normale.
    - `<-s.Expired()`: nessun ricevente entro `WaitTimeout`: `panic(http.ErrAbortHandler)` nel mittente (D7); la connessione cade e `curl` esce con errore.
    - `<-r.Context().Done()`: mittente caduto; ritorno (`ok` false → `Abort(ErrSenderGone)`).
  - Lo slug e il ricevente tornano conclusi nella goroutine del mittente tramite `defer` (abort protocol).
  Contract download (`GET /<slug>`; path diverso da un segmento → 404; slug non valido per alfabeto/lunghezza → 404):
  - `rcv := &receiver{w: w}`; `s, err := reg.Claim(slug, rcv)`: `ErrNotFound` → 404; `ErrClaimed` → 410.
  - attendere `<-s.Finished()`; alla cancellazione di `r.Context()` chiamare `s.Abort(ErrReceiverGone)` e continuare ad attendere `Finished()`.
  - Dopo `Finished()`: se `s.Err() != nil`: se `rcv.started` → `panic(http.ErrAbortHandler)` (D8); altrimenti `503` con testo `transfer aborted`.
  - Se `s.Err() == nil`: ritorno normale (Go chiude il body chunked correttamente).
  Failure handling:
  - Il mittente in scadenza o con errore di copia riceve la chiusura abortita della connessione (`panic(http.ErrAbortHandler)` nel suo handler), non un `200` completo: nessun esito di successo fittizio (I-4).
  - Scritture al ricevente che falliscono (client chiuso) → errore di `Copy` → `Abort` → `Finish`: nessun panic nel mittente.
  - Nessun `panic` a meno di `http.ErrAbortHandler` nel ricevente.
  - Copia: il buffer torna al pool anche in caso di errore (`defer c.pool.Put`).
  Recovery boundary: dopo `copy.go` (checkpoint in STATE §6), dopo `handler.go` upload, dopo download.
- **Unit tests:** nomi e oracoli:
  - `internal/relay/copy_test.go`: `TestCopyFlushesEachWrite` (receiver fittizio conta Flush = numero di Write), `TestCopyRoundTripBytes` (input random 1 MiB, output identico), `TestCopyAllocsBounded` (`testing.AllocsPerRun` su 16 MiB in buffer 256 KiB ≤ 20 allocazioni; I-1).
  - `internal/server/handler_test.go` (usare `httptest.NewServer`, client HTTP reale su loopback):
    - `TestUploadReturnsCommandsBeforeBody`: `io.Pipe` come body; il client legge la prima riga della risposta entro 2 s **prima** di scrivere nel pipe; la riga contiene `curl -o "` e `wget -O "` (D4).
    - `TestRelayRoundTripSHA`: sottotest con dimensioni 0, 1, 1 MiB, 16 MiB; sha256 del download = sha256 dell'upload; (I-4).
    - `TestGetUnknown404`: slug valido ma inesistente → 404.
    - `TestGetSecondClaim410`: due GET sullo stesso slug, il secondo ottiene 410 (I-2).
    - `TestSenderAbortFailsReceiver`: meccanica: connessione TCP raw (`net.Dial`) verso il server di test; invio di `PUT /x.bin HTTP/1.1` con `Content-Length: 16777216`; lettura della risposta con i comandi; avvio di `GET /<slug>` in goroutine; scrittura di 1 MiB e chiusura della connessione. Oracolo: il ricevente riceve meno di 16 MiB **e** `io.ReadAll` restituisce errore non `nil` (`io.ErrUnexpectedEOF` o errore di rete), mai `nil` (I-4).
    - `TestReceiverAbortCleansSession`: meccanica: il mittente scrive da `io.Pipe` in blocchi da 64 KiB finché una scrittura fallisce; il ricevente legge 1 MiB poi annulla il contesto della propria richiesta. Oracolo: entro 5 s la scrittura del mittente fallisce (o la richiesta PUT termina con errore) e `reg.Len()==0` (I-3). Il registry è creato dal test e passato a `server.New`.
    - `TestUploadMethodNotAllowed`: `POST /` → 405 con `Allow: PUT`.
    - `TestUploadUnknownLength`: `PUT /mydir.tgz` con `Transfer-Encoding: chunked` da `io.Pipe` (lunghezza ignota), 1 MiB poi 16 MiB; sha256 del download = sha256 dell'upload; il ricevente non riceve `Content-Length`.
    - `TestUploadNameFromPath`: `PUT /mydir.tgz` → blocco comandi contiene `curl -o "mydir.tgz"` e `wget -O "mydir.tgz"`.
    - Comando: `go test ./internal/relay ./internal/server -v -count=1 -race`; discovery: 3 test copy + 9 test server (sottotest sui sizes dentro `TestRelayRoundTripSHA`).
- **e2e tests:** N/A in questa unità (coperto da `scripts/e2e.sh` in 1.3). La copertura dei test di handler è completa per il contratto HTTP.
- **Done:** tutti i test listati eseguiti e passati con `-race`, nessun test saltato (`-v` mostra `PASS` per ciascuno); revisione agent-1 pre-implementazione e post-diff registrate in STATE §7; I-1..I-4 coperti; unità chiusa in STATE §4 con `uncommitted`.

### 1.3 Bootstrap, shutdown e script e2e locale
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** main e script e2e; review agent-1 sul diff e sull'esito di `scripts/e2e.sh`.
- **Files:**
  - WRITE `cmd/bashtransfer/main.go` (sostituisce il placeholder di 0.2).
  - NEW `scripts/e2e.sh` (eseguibile, `set -euo pipefail`).
- **Change:**
  Preconditions: 1.1 e 1.2 chiusi.
  Contract main: carica `config.Load(os.Getenv)`; errore → messaggio su stderr e exit 1. Crea `relay.NewRegistry`, `relay.NewCopier(cfg.BufSize)`, `server.New`. `http.Server{Addr: cfg.Listen, Handler, ReadHeaderTimeout: 10 s, IdleTimeout: 120 s}`; **nessun** `ReadTimeout` né `WriteTimeout` (bloccherebbero il relay). Ascolta SIGINT/SIGTERM; `Shutdown` con contesto di 10 s, poi `Close`. Log su stderr con `log/slog`.
  Contract script e2e (`scripts/e2e.sh`):
  1. crea directory temporanea (fuori dal repository), file payload di 8 MiB casuale, sha256;
  2. compila il binario in quella directory (`go build -o`);
  3. avvia il binario su porta libera di `127.0.0.1` (`BASHT_LISTEN=127.0.0.1:<porta>`), attende che `GET /` risponda;
  4. avvia upload in background con stdout su file: `curl -sS -T payload.bin http://127.0.0.1:<porta>/e2e.bin > upload.out &`; attende la prima riga, estrae lo slug con regex `[0-9A-Za-z]{10}`;
  5. scarica con `curl -sS -o received.bin http://127.0.0.1:<porta>/<slug>`; confronta sha256;
  5b. RS-2: `cat payload.bin | curl -sS -T - http://127.0.0.1:<porta>/mydir.tgz > upload2.out &` (lunghezza ignota); slug da `upload2.out`; comando di download con `-o "mydir.tgz"`; expected: nome `mydir.tgz` nel blocco comandi, sha256 identico;
  6. secondo download stesso slug → HTTP 410 (`curl -s -o /dev/null -w '%{http_code}'`);
  7. slug sconosciuto → 404;
  8. termina il binario e l'upload, pulisce directory temporanea (solo quella creata dallo script).
  Il comportamento di curl (R1) è già CONFIRMED in 0.1: lo script non deve dipendere da ipotesi sul nome nel path: il nome atteso è `e2e.bin` solo nel comando `curl -o`, il server usa il nome del path (D5).
  Steps:
  1. S1 — scrivere `main.go`; expected: `go build ./cmd/bashtransfer` exit 0.
  2. S2 — scrivere `scripts/e2e.sh`; expected: `bash -n scripts/e2e.sh` exit 0.
  3. S3 — eseguire `scripts/e2e.sh`; expected: exit 0 e riga `E2E OK`.
  Recovery boundary: prima di S3 (processo server in background): registrare PID e directory temporanea in STATE §6.
  Failure handling: se l'upload non ottiene lo slug entro 10 s → STOP, esito `FAIL` registrato con log del server; non cambiare il contratto per far passare lo script.
- **Unit tests:** N/A per il binario; la copertura è nei test di 1.1/1.2. Verifica di discovery per il gate: `go test ./... -count=1 -v` lista i pacchetti `config`, `slug`, `relay`, `server` con conteggi come sopra.
- **e2e tests:** `scripts/e2e.sh` (oracolo: exit 0, sha256 identico, 410 al secondo download, 404 per slug ignoto). Gate G-E2E1.
- **Done:** `go build` pass; `bash -n` pass; `scripts/e2e.sh` pass con `E2E OK` registrato in STATE §7; nessun processo residuo (`pgrep -f bashtransfer` vuoto); unità chiusa in STATE §4.

## Phase gates and closure
- Required gates: G-U1.1, G-U1.2, G-E2E1 (STATE §3), più `go test -race -count=1 ./...` come G-P1.
- Assertions: I-1..I-5 coperti; D4 verificato da `TestUploadReturnsCommandsBeforeBody`; D8 verificato da `TestSenderAbortFailsReceiver`.
- README obligation: il comportamento distribuito è l'upload/download; README non ancora scritto. Responsabile: unità 4.3. Verifica di chiusura P1: README assente, obbligo registrato come pendente su 4.3 in STATE §11.
- Every sub-phase DONE. Esegui G-P1 ora.
- Supervisor agent-1 (claude-haiku-5-5) reviews: revisione ad alto rischio di `Finish`/`Abort` e di `handler.go` (lifecycle del ricevente) prima di scrivere `handler.go`; revisione di chiusura di P1 con elenco degli invarianti.
- Chiusura P1: unità `P1` in STATE §4, fase DONE in STATE §11 solo se G-P1 pass con conteggio non zero.
