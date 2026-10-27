# Bashtransfer — Plan overview

Authored 2026-10-27 by agent-1 (claude-haiku-5-5, supervisor role). Folder: `docs/plans/001_plan-Bashtransfer/`.
Execution starts at [STATE.md](STATE.md); all live readiness and progress are there.

## Goal and reference scenario

Servizio relay a riga di comando, da deployare su una VM con Docker dietro nginx (TLS terminato da nginx). Il server non conserva file: riceve il body di un upload e lo inoltra in streaming al primo download che usa lo slug.

Scenario di riferimento (RS-1):
1. Mittente: `curl -T myfile.zip https://bashtransfer.mydomain.it` (istanza locale di test: `http://127.0.0.1:8080`).
2. Il server risponde subito con due comandi testuali (curl e wget) contenenti uno slug di 10 caratteri. Il mittente resta in attesa.
3. Ricevente: esegue `curl -o "myfile.zip" "<base>/<slug>"`. Il trasferimento parte; il file ricevuto ha sha256 uguale a quello inviato.
4. Un secondo download con lo stesso slug ottiene `410 Gone`.

Scenario di riferimento (RS-2, streaming da pipe; aggiunto in revisione 2 da Specifiche):
1. Mittente: `sudo tar -cpzf - mydir | curl -T - https://bashtransfer.mydomain.it/mydir.tgz`. Il body arriva da stdin con lunghezza ignota (`Transfer-Encoding: chunked`); il server non lo salva su disco.
2. Il server risponde subito con i comandi curl e wget per `mydir.tgz` (nome dal path) e il mittente resta in attesa mentre tar produce output.
3. Ricevente: `curl -o "mydir.tgz" "<base>/<slug>"`; sha256 del file ricevuto uguale al tar prodotto.

Forma esatta di RS-1 (da Specifiche): URL senza `/` finale (`https://bashtransfer.mydomain.it`). Verificata in R4 (0.3).

Esclusioni (fuori scope):
- Gestione certificati TLS (demandata a nginx).
- Persistenza dei file su disco o in database.
- Più destinatari per lo stesso slug.
- Push, deploy su host remoto, pubblicazione di immagini. Nessuna operazione verso rete esterna senza autorizzazione esplicita.
- Upload multipart/form o POST: solo PUT.
- Rate limiting e quote per IP (possibile lavoro futuro).

Vincoli: sandbox senza accesso di rete esterno (CLAUDE.md utente); Docker e nginx devono essere verificati localmente o dopo autorizzazione.

## Decisions

| ID | Decision and consequence | Authority/source | Supersedes |
|----|--------------------------|------------------|------------|
| D1 | Linguaggio **Go** (toolchain minimo fissato in 0.2, non sotto la versione con `http.ResponseController.EnableFullDuplex`, vedi R3). Server a binario statico, `CGO_ENABLED=0`. Il relay è limitato dalla rete, Go con `net/http` satura la banda con poco codice. Rust scartato: prestazioni comparabili per un relay I/O-bound, costo implementativo più alto per lo stesso scope; decisione reversibile. | Supervisor, delega utente ("deci tu il migliore") | — |
| D2 | Relay in memoria: una `session` per slug; il body viene letto in buffer fissi e scritto al ricevente con flush per blocco. Nessun file su disco. Un solo ricevente per slug. | Supervisor, da Specifiche ("solo relay") | — |
| D3 | Slug: 10 caratteri dall'alfabeto `0-9A-Za-z` (62 simboli), generati con `crypto/rand` e rejection sampling (nessun bias modulo). In caso di collisione con una sessione viva: rigenerazione fino a 5 tentativi, poi 503. | Supervisor | — |
| D4 | Protocollo mittente: `PUT /` oppure `PUT /<name>`. Il server crea la sessione e scrive subito la risposta `text/plain` con il blocco comandi (vedi Interfacce), usando full-duplex: risposta scritta e flushata prima di leggere il body. Il body viene letto solo quando un ricevente si aggancia. Il mittente non riceve esito finale oltre alla chiusura della connessione. | Supervisor; dipende da R1 (branch A) | — |
| D5 | Nome file: segmento `<name>` del path se presente, altrimenti `download`. Sanitizzazione: solo basename, rimozione di caratteri di controllo, `"` e `\`, massimo 128 caratteri. Se R1 mostra che curl non invia il nome nel path con `curl -T f https://host/`, vale il branch B di R1 (vedi phase_02 e overview R1). | Supervisor; dipende da R1 | — |
| D6 | Ricevente: `GET /<slug>`. Primo claim vince (operazione atomica nel registry). Risposta `200` con `Content-Type: application/octet-stream`, `Content-Disposition: attachment; filename="<name>"`; `Content-Length` inoltrato solo se il mittente lo ha dichiarato. Slug sconosciuto: `404`. Slug già reclamato o concluso: `410`. | Supervisor | — |
| D7 | Attesa del ricevente: se nessun ricevente si aggancia entro `BASHT_WAIT_TIMEOUT` (default `1h`), la sessione viene rimossa e la connessione del mittente chiusa. | Supervisor | — |
| D8 | Fallimenti: se il mittente cade durante il relay, la risposta al ricevente viene interrotta senza chiusura regolare del chunk finale (il client vede errore, nessun successo fittizio). Se il ricevente cade, la copia lato mittente fallisce e la sessione viene rimossa. Ogni sessione termina una sola volta (I-3). | Supervisor | — |
| D9 | Basic auth opzionale, solo su `PUT`: `BASHT_AUTH_USER` e `BASHT_AUTH_PASSWORD` devono essere entrambi impostati o entrambi assenti (altrimenti errore all'avvio). Confronto con `subtle.ConstantTimeCompare` su digest SHA-256. Risposta `401` con `WWW-Authenticate: Basic`. Download `GET` sempre pubblico con slug. | Utente (Q1: solo PUT), supervisor (meccanismo) | — |
| D10 | Configurazione solo da variabili d'ambiente; nessun file di configurazione. | Supervisor | — |
| D11 | Limite sessioni vive `BASHT_MAX_SESSIONS` (default `1000`); oltre il limite il `PUT` riceve `503`. | Supervisor | — |
| D12 | Il server parla HTTP semplice; TLS è di nginx. Il server usa `X-Forwarded-Proto` e `Host` solo per costruire la base URL dei comandi, quando `BASHT_PUBLIC_URL` non è impostato. | Supervisor | — |
| D13 | Performance: buffer da `sync.Pool` di 256 KiB, `io.CopyBuffer`, flush per blocco, nessuna compressione, nessuna allocazione per chunk. Criterio di accettazione: throughput del relay ≥ 90% del baseline `io.CopyBuffer` diretto con stesso buffer, misurato su loopback con lo stesso host. | Supervisor | — |
| D14 | Deploy: Dockerfile multi-stage, runtime `distroless/static:nonroot`, tag di immagine fissati. `compose.yaml` con `nginx` e `app` su rete interna; `app` non pubblica porte sull'host. | Supervisor | — |
| D16 | Upload da stdin (RS-2): `PUT` con `Transfer-Encoding: chunked` e lunghezza ignota (`ContentLength == -1`), stesso protocollo di D4–D6, nome dal path (`PUT /mydir.tgz`). Nessun limite di dimensione lato server; la durata è limitata solo dall'inattività (D15). Vincolo: il blocco comandi arriva prima della fine di stdin (R4 caso B). | Utente (Specifiche, scenario 2), supervisor | — |

Config (D10) in forma completa: `BASHT_LISTEN` (default `:8080`), `BASHT_PUBLIC_URL` (default: derivata dalla richiesta), `BASHT_AUTH_USER`, `BASHT_AUTH_PASSWORD`, `BASHT_WAIT_TIMEOUT` (default `1h`), `BASHT_MAX_SESSIONS` (default `1000`), `BASHT_BUF_SIZE` (default `262144`, valore minimo 4096).

## Open questions

- **Q1 — risolta dall'utente:** auth solo su `PUT` (upload con `-T`). Il download `GET /<slug>` è pubblico: lo slug è il segreto.
- **Autorizzazioni dell'utente:** accesso di rete per documentazione nginx ufficiale (R2) e pull delle immagini base Docker pinnate (4.1, 4.2).

No open product question. Technical choices are recorded as D1–D15.

## Architecture

```
mittente ──PUT /<name>──▶ handler upload ─┐ crea session(slug, name, clen?)  registry (map slug→session, mutex)
                          (full-duplex)   │ scrive comandi, attende claim    ▲
                                          │                                 │ claim atomico (1 vincitore)
ricevente ──GET /<slug>──▶ handler download ┘ consegna il proprio writer ────┘
                          │
                          └─ relay: io.CopyBuffer(writer, uploadBody) con buffer da sync.Pool, Flush per blocco
```

Ownership: il registry è l'unico punto che muta lo stato delle sessioni. La goroutine del mittente possiede il body di upload e la copia; la goroutine del ricevente aspetta il segnale di fine. Il timeout di attesa è una goroutine per sessione con `time.Timer` gestito dal registry.

Stati sessione: `waiting` → `claimed` → `done` | `aborted`. Transizioni solo tramite metodi del registry, ognuna avvenuta una sola volta (I-3).

Package layout (tutti NEW, dettagli in phase file):
- `cmd/bashtransfer/main.go`: bootstrap, lettura config, avvio `http.Server`.
- `internal/config/`: parsing env e validazione.
- `internal/slug/`: generazione slug.
- `internal/relay/`: registry, sessioni, pool buffer, relay.
- `internal/server/`: handler HTTP, auth, risposta testo con comandi.
- `scripts/e2e.sh`, `scripts/bench.sh`.
- `Dockerfile`, `compose.yaml`, `deploy/nginx/bashtransfer.conf`, `README.md`, `go.mod`.

## Interfaces and compatibility

| Surface | Exact name/type | Default | Errors/conflicts | Compatibility |
|---------|-----------------|---------|------------------|---------------|
| HTTP upload | `PUT /` e `PUT /<name>`, body stream, `Content-Length` opzionale; da stdin senza lunghezza = `Transfer-Encoding: chunked` (D16) | — | `401` auth, `503` limite sessioni o collisioni slug, `405` altri metodi su path di upload | nuovo |
| HTTP risposta upload | `200 text/plain`, due righe: `curl -o "<name>" "<base>/<slug>"` e `wget -O "<name>" "<base>/<slug>"` | — | — | nuovo; formato a righe fisse per script |
| HTTP download | `GET /<slug>` | — | `404` slug ignoto, `410` già reclamato, concluso o scaduto (D7) | nuovo |
| HTTP root | `GET /` testo d'uso | — | — | nuovo |
| Env | vedi D10 | vedi D10 | config invalida → exit 1 all'avvio con messaggio su stderr | nuovo |
| Binario | `bashtransfer` senza argomenti; SIGINT/SIGTERM → shutdown ordinato | — | — | — |

Errori standard: corpi di errore in testo semplice con una riga; nessun dettaglio interno (stack, path) esposto.

## Phase map

| Logical phase | File | Sub-phase IDs | Depends on | Assignment |
|---------------|------|---------------|------------|------------|
| 0 | [phase_01.md](phase_01.md) | 0.1, 0.2, 0.3 | none | agent-2 (probe, skeleton); agent-1 review |
| 1 | [phase_02.md](phase_02.md) | 1.1, 1.2, 1.3 | 0 (0.1 per R1, 0.2 per R3) | agent-2; review agent-1 |
| 2 | [phase_03.md](phase_03.md) | 2.1, 2.2 | 1 | agent-2; review agent-1 |
| 3 | [phase_04.md](phase_04.md) | 3.1 | 1 | agent-2; review agent-1 |
| 4 | [phase_05.md](phase_05.md) | 4.1, 4.2 (residuo R2 e rete), 4.3 docs | 2, 3 | agent-2; review agent-1 |

No live status in this table.

## Reuse map

Greenfield: il repository non ha codice (nessun commit, solo `Specifiche.md` non tracciato). Nessun simbolo da riusare. Riferimenti esterni alla libreria standard sono verificati localmente con `go doc` (vedi R3) e non vanno reimplementati.

| Need | Path and symbol | Contract to preserve |
|------|-----------------|----------------------|
| Streaming HTTP | `net/http`, `http.ResponseController.EnableFullDuplex`, `http.Flusher` | D4, D6 |
| Copia con buffer riusabile | `io.CopyBuffer`, `sync.Pool` | D13 |
| Slug sicuro | `crypto/rand.Int` | D3 |
| Confronto costante | `crypto/subtle.ConstantTimeCompare` | D9 |

## Research and evidence

Ricerca esterna (B2) limitata: la sandbox non può accedere alla rete esterna senza autorizzazione esplicita (regole utente in CLAUDE.md). Le claim che dipendono da fonti esterne restano UNVERIFIED. Quelle verificabili localmente (curl installato, toolchain Go, binari locali) sono verificate con probe isolati fuori dal repository.

| ID | Question / required or optional | Evidence state | Affected decisions / units / tests |
|----|---------------------------------|----------------|------------------------------------|
| R1 | `curl -T <file> https://host/` (URL senza nome file): (a) il server riceve il nome nel path? (b) curl stampa la risposta del server prima della fine dell'upload? Required. | CONFIRMED (branch A) — probe locale 0.1, curl 8.18.0 | D4, D5; 0.1, 1.2, 1.3 |
| R2 | Direttive nginx per upload/download in streaming senza buffering e body illimitato (`proxy_request_buffering off`, `proxy_buffering off`, `client_max_body_size 0`, timeout di lettura/scrittura lunghi, `proxy_http_version 1.1`). Required per 4.2. | CONFIRMED per le direttive di buffering, body e versione HTTP; client_body_timeout non ancora verificato (vedi ### R2). Timeout di proxy da portare ≥ BASHT_WAIT_TIMEOUT. | D12, D14; 4.2 sbloccata, con parametri timeout |
| R3 | `http.ResponseController.EnableFullDuplex` disponibile dalla versione Go minima; comportamento sul body durante la scrittura della risposta. Required per D4. | CONFIRMED — fonte locale: `go doc` su go1.27.3; introduzione in go1.21 (`$GOROOT/api/go1.21.txt`). `go.mod`: `go 1.21`. | D1, D4; 0.2, 1.2 |
| R4 | Due casi di curl: (a) `curl -T file https://host` senza `/` finale (forma di RS-1) produce path con nome del file? (b) `tar ... \| curl -T - https://host/x.tgz` stampa il blocco comandi prima della fine di stdin? Required per RS-1 e RS-2 (D16). | CONFIRMED — probe locale 0.3, curl 8.18.0: (a) path `/payload.bin` (branch A: nome preservato); (b) riga di risposta 3 ms dopo `PUT_START`, EOF stdin +3 s (branch B-OK); (c) stdin senza nome → path `/` (limitazione D5, non bloccante) | D4, D5, D16; 0.3, 1.2, 1.3 |

### R1 — curl upload behaviour
- Sources: local `curl --help all`/`man curl` (versione installata) e probe locale.
- Applicability: versione curl installata nella sandbox; il comportamento dei client può variare tra versioni.
- Dates: non applicabile (verifica locale).
- Documented fact: nessuno ancora.
- Supervisor inference: il progetto deve funzionare con `curl` standard della macchina del mittente; la versione della sandbox è solo un indizio.
- Local check (0.1, curl 8.18.0, 2026-10-27): probe in directory di scratch fuori dal repo, server su `127.0.0.1` che invia la risposta come chunk (senza chunk finale), attende 3 s, poi legge il body (8 MiB casuali).
  - S3 `curl -N -T payload.bin http://127.0.0.1:<porta>/`: la riga `COMANDI-DI-PROVA` arriva entro 2 ms dalla risposta del server; `BODY_END` a +3 s con 8388608/8388608 byte. Branch A.
  - Senza `-N` la riga su pipe arriva solo a fine upload: buffering stdio di curl, non del protocollo. Il README (4.3) deve citare `curl -N` solo per output su pipe; su terminale la riga compare subito.
  - S4: `PUT /probe-name.zip` → path `/probe-name.zip`. `PUT /` con URL che termina in `/` → curl aggiunge il basename locale: path `/payload.bin`. Il nome è quindi disponibile in entrambi i casi (D5 valido).
  - Prima passata scartata: risposta con `Content-Length` completa. curl chiude l'upload subito dopo la risposta (body ricevuto 576 KiB su 8 MiB). Non rappresenta D4.
  - Vincolo implementativo (D4): la risposta al `PUT` deve restare aperta (nessun chunk finale) fino alla fine del relay. Chiudere la risposta prima di aver letto il body fa interrompere l'upload al mittente.
- Esito: **branch A**. Decision impact: D4 e D5 valgono come scritti. Branch B (curl stampa solo a fine upload): **stop** e escalation all'utente, perché il mittente non vedrebbe i comandi prima del relay; nessuna scelta tecnica sostitutiva è autorizzata senza decisione. Branch C (path senza nome): D5 usa `download` come nome di default e il nome è dichiarato limitazione, o l'utente sceglie un'alternativa (es. `curl -T f -H` — non nella spec). Escalation in ogni caso.
- Unresolved/replacement: nessuno dopo 0.1.

### R2 — nginx streaming directives
- Sources: https://nginx.org/en/docs/http/ngx_http_proxy_module.html e https://nginx.org/en/docs/http/ngx_http_core_module.html (fetch 2026-10-27, autorizzato dall'utente; indicizzati con context-mode).
- Applicability: documentazione corrente di nginx.org, non legata a una versione pinnata. La versione dell'immagine va pinnata in 4.2 e confrontata con le note di versione (`proxy_request_buffering` esiste da 1.7.11).
- Documented facts (confermati):
  - `proxy_request_buffering off`: il body è inoltrato all'upstream appena arriva (default `on`, introdotta in 1.7.11). Con HTTP/1.1 verso upstream (default `proxy_http_version 1.1`) l'opzione è rispettata.
  - `proxy_buffering off`: la risposta è passata al client senza bufferizzazione; nginx legge al massimo `proxy_buffer_size` per volta. Il header `X-Accel-Buffering: no` ha lo stesso effetto ma non va usato come sostituto.
  - `proxy_http_version 1.1` è il default.
  - `client_max_body_size 0` disabilita il controllo della dimensione del body (default `1m`).
  - `proxy_read_timeout` e `proxy_send_timeout` default `60s`; si applicano tra due operazioni di lettura/scrittura successive, non all'intera trasmissione.
- Supervisor inference (non garanzia della fonte): durante l'attesa del ricevente l'app non legge il body e non emette byte dopo i comandi. Quindi nginx vede un upstream fermo: `proxy_send_timeout` chiude la connessione verso l'app dopo 60s di blocco in scrittura, e `proxy_read_timeout` la chiude dopo 60s senza byte di risposta. Entrambi devono essere ≥ `BASHT_WAIT_TIMEOUT` (default 1h = 3600s). Per il relay in corso, il silenzio del mittente è limitato da `BASHT_IDLE_TIMEOUT` (D15), che deve essere < `proxy_read_timeout`.
- Local check: non eseguito. Verifica di sintassi con `nginx -t` in container o locale in 4.2 (immagine pinnata, rete autorizzata).
- Decision impact: 4.2 sbloccata. Parametri nginx: `client_max_body_size 0`, `proxy_request_buffering off`, `proxy_buffering off`, `proxy_http_version 1.1`, `proxy_read_timeout` e `proxy_send_timeout` = 3600s (≥ default `BASHT_WAIT_TIMEOUT`); `proxy_read_timeout` > `BASHT_IDLE_TIMEOUT`. Scelta reversibile; alternativa (timeout più corti) rifiutata perché rompe D7.
- Unresolved/replacement: inoltro del body chunked del client (RS-2) con `proxy_request_buffering off` da verificare nella stessa fonte. `client_body_timeout` (default 60s, tra due letture successive del body dal client) non ancora letto: da verificare in 4.2 con la stessa fonte prima di dichiarare la configurazione corretta. Resta aperto un punto: se nginx non legge il body mentre l'app attende, `client_body_timeout` può scattare sul mittente; da verificare con test e2e lento (upload che attende > 60s prima del download).

### R3 — Go full duplex
- Sources: `go doc net/http.ResponseController.EnableFullDuplex` sulla toolchain installata; `$GOROOT/api/go1.21.txt` riga 356 (`pkg net/http, method (*ResponseController) EnableFullDuplex() error #57786`). Fonte locale, nessuna ricerca web.
- Local check (0.2, 2026-10-27): `go version` = `go1.27.3 linux/amd64`; `go doc` stampa la firma `func (c *ResponseController) EnableFullDuplex() error` e la descrizione (HTTP/1: lettura del body interleaved con la scrittura della risposta). `go vet ./...` exit 0; `go list ./...` = `bashtransfer/cmd/bashtransfer`.
- Decision impact: il metodo esiste dalla go1.21; `go.mod` dichiara `go 1.21` (minimo). D4 valido. La builder image in 4.1 usa una toolchain ≥ 1.21 (la scelta pinnata va registrata in 4.1). Non è una ricerca web.

### R4 — curl upload senza `/` finale e da stdin
- Sources: probe locale con curl installato (versione registrata in 0.3), fuori dal repo.
- Applicability: curl della sandbox; il comportamento del mittente reale può variare.
- Documented fact: nessuno ancora.
- Local check (0.3, curl 8.18.0, 2026-10-27; server di probe in scratch `r4/probe_server.py` su `127.0.0.1:18090`, chiuso dopo il probe, nessun listener residuo; log in `scratchpad/r4/server.log`):
  - (A) `curl -N -T r1/payload.bin http://127.0.0.1:18090` (URL senza `/`, 8388608 byte, `CL=8388608`): `PUT_START path=/payload.bin`; `BODY_END bytes=8388608`. Il nome è nel path anche senza slash finale.
  - (B) `{ printf x; sleep 3; head -c 8388608 /dev/urandom; } | curl -N -T - http://127.0.0.1:18090/mydir.tgz`: `PUT_START path=/mydir.tgz CL=- TE=chunked` a .714694; riga `COMANDI-DI-PROVA` visibile a .717428 (+3 ms); EOF stdin a .731 (+3 s dopo la riga); `BODY_END bytes=8388609` (1 byte `x` + 8388608). Nessun `Content-Length`.
  - (C) `head -c 1024 /dev/urandom | curl -N -T - http://127.0.0.1:18090`: `PUT_START path=/` (nessun nome). Risposta visibile; `BODY_END bytes=1024`.
  - Osservazione: curl invia `Expect: 100-continue` in tutti i casi; il server non risponde 100 e curl procede comunque (nessun blocco osservato).
- Decision impact: (A) path con basename → RS-1 invariato, D5 valido. (B) risposta visibile prima dell'EOF → D16 valido, nessun escalation. (C) stdin senza nome: D5 usa `download`; per RS-2 il nome è sempre nel path (`/mydir.tgz`), quindi non bloccante. Limitazione da documentare in 4.3: `tar ... | curl -T - https://host` senza nome produce `download`.
- Esito: **branch A/B-OK/C-limit** → R4 CONFIRMED, nessuna escalation. Unresolved/replacement: nessuno.

## Invariants

| ID | Meaning | Guarding test |
|----|---------|---------------|
| I-1 | Il server non scrive il body su disco e usa al più un buffer per relay attivo. | `TestRelayUsesPooledBuffer` (1.2); revisione di codice in 1.2 e P1 |
| I-2 | Per uno slug c'è al massimo un ricevente; ogni altro claim riceve `410`. | `TestRegistryClaimOnce`, `TestGetSecondClaim410` (1.1, 1.2) |
| I-3 | Ogni sessione transita a un terminale (`done`/`aborted`) e viene rimossa dal registry esattamente una volta. | `TestRegistryRemoveOnce`, `TestReceiverAbortCleansSession` (1.1, 1.2) |
| I-4 | Il byte stream ricevuto è identico a quello inviato, oppure il ricevente vede un errore di trasferimento. Mai troncamento silenzioso. | `TestRelayRoundTripSHA` con sha256 (1.2); `TestSenderAbortFailsReceiver` (1.2) |
| I-5 | Gli slug vivi sono unici; lo slug ha 10 caratteri dall'alfabeto D3. | `TestGenerateLength`, `TestGenerateUnique` (1.1) |

## Risks

| Risk | Design mitigation | Verification |
|------|-------------------|--------------|
| curl non mostra la risposta prima della fine dell'upload (R1-B) | Stop e escalation, nessuna implementazione del relay prima dell'esito di 0.1 | esito registrato in overview R1 |
| Goroutine e connessioni lasciate aperte da sender abbandonati | D7 timeout, I-3, `http.Request.Context` su ogni attesa | `TestRegistryWaitTimeout`, `TestSenderAbortFailsReceiver` |
| Molti mittenti in attesa consumano risorse (istanza pubblica) | D11 limite sessioni; ogni sessione usa una goroutine e nessun buffer finché non c'è un ricevente | `TestRegistryMaxSessions` |
| Buffering nginx annulla lo streaming | R2, 4.2 non chiudibile finché la config non è verificata; test di regressione con nginx in compose | 4.2 e G-P4 |
| Upload pubblico anonimo abusato | D9 auth opzionale; Q1; rate limiting fuori scope | documentato in 4.3 |
| Filename con caratteri pericolosi nell'header | D5 sanitizzazione | `TestFilenameSanitize` (2.2) |

## Verification strategy

- Baseline (G-BASE): repository senza codice; registrare `git status --short` e `go version` prima della prima modifica.
- Unit (per sub-phase): solo i test del pacchetto modificato, con nome e conteggio atteso in STATE §3.
- Phase closure P<N>: `go test -race -count=1 ./...` con conteggio test non zero e i test e2e della fase (`scripts/e2e.sh`).
- Benchmark in 3.1 e P3: `scripts/bench.sh` su loopback; verdetto secondo D13.
- Final (G-FINAL, ultima fase): RS-1 e RS-2 eseguiti su istanza locale tramite `compose.yaml` e nginx, dopo autorizzazione rete per pull delle immagini (4.1–4.2).

I comandi esatti sono in STATE.md §3 (registro gate), non ripetuti nelle fasi.

## Model-assignment summary

Roster: `agent-1:claude-haiku-5-5,agent-2:claude-haiku-5-5` (override esplicito dell'utente: solo Haiku 5.5, nessun Sonnet o Opus).

| Unit(s) | Implementer | Required supervisor review |
|---------|-------------|----------------------------|
| 0.1, 0.2, 0.3 | agent-2 (claude-haiku-5-5) | agent-1 (claude-haiku-5-5), dopo 0.1: verifica dell'esito R1 e branch scelto |
| 1.1, 1.2, 1.3 | agent-2 | agent-1, revisione ad alto rischio prima di scrivere `handler.go` (concorrenza e lifecycle) e chiusura P1 |
| 2.1, 2.2 | agent-2 | agent-1 su 2.1 (contratto auth) e P2 |
| 3.1 | agent-2 | agent-1 su criterio D13 e P3 |
| 4.1, 4.2, 4.3 | agent-2 | agent-1 su 4.2 (direttive) e P4 |

Limitazione dichiarata: con un solo modello, la "revisione del supervisore" è una **self-review di un altro agente dello stesso modello**. Non è una revisione da modello più forte. Ogni record di review in STATE.md riporta questo limite e non va presentato come approvazione forte.
