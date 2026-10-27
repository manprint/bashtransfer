# Phase 4 — Deploy Docker, nginx e documentazione

Intent: il servizio gira con `docker compose` (nginx davanti, app dietro su rete interna) e la documentazione descrive installazione, uso, configurazione e limiti. Esecuzione finale di RS-1 attraverso nginx.
Prerequisites: P2 chiuso o 2.1 SKIPPED con motivazione approvata (vedi `phase_03.md`); P3 chiuso con G-BENCH PASS (`phase_04.md`). Artefatti: binario `bashtransfer` e script e2e.
Phase closure: P4 e final gate (ultima fase); review by agent-1 (claude-haiku-5-5, self-review dello stesso modello).

## State and ownership contract
Read STATE.md §0–1 first for scope, ownership, recovery, checks and commit rules.
Open before edits; checkpoint at a recovery boundary; close with evidence. 4.2 dipende da R2 (residuo `client_body_timeout`, S1) e dalla rete autorizzata per l'immagine nginx: non assumere la configurazione nginx come verificata finché S2 non passa.

## Local design context
Plan revision: 2 (RS-2 attraverso nginx; README con due scenari).
- D12: il server parla HTTP semplice; TLS è di nginx. Il server usa `X-Forwarded-Proto` e `Host` solo per la base URL dei comandi, quando `BASHT_PUBLIC_URL` non è impostato.
- D14: Dockerfile multi-stage, binario statico `CGO_ENABLED=0`, runtime `distroless/static:nonroot`, tag immagine pinnati alla versione verificata; `compose.yaml` con `nginx` e `app` su rete interna; `app` non pubblica porte.
- R2 (nginx): direttive richieste per streaming senza buffering e body illimitato: `client_max_body_size 0`, `proxy_request_buffering off`, `proxy_buffering off`, `proxy_http_version 1.1`, `proxy_read_timeout` e `proxy_send_timeout` = 3600s (≥ `BASHT_WAIT_TIMEOUT` default; motivazione in overview R2), header `X-Forwarded-Proto` e `Host`. Direttive confermate da nginx.org (overview R2); `client_body_timeout` ancora da leggere.
- Auth: come da esito di Q1 in `phase_03.md`.

## Sub-phases

### 4.1 Dockerfile, .dockerignore e compose (solo servizio app)
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** scrivere i file; review agent-1 su pinning e assenza di segreti nell'immagine.
- **Files:**
  - NEW `Dockerfile`: stage `builder` (immagine Go pinnata alla versione di `go.mod` o successiva compatibile) con `CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/bashtransfer ./cmd/bashtransfer`; stage finale `gcr.io/distroless/static:nonroot` (tag pinnato), `COPY --from=builder`, `USER nonroot`, `EXPOSE 8080`, `ENTRYPOINT ["/bashtransfer"]`.
  - NEW `.dockerignore`: `.git`, `docs/`, `bin/`, `scripts/`, `*.test`, `Specifiche.md`.
  - NEW `compose.yaml`: servizio `app` con `build: .`, `environment` da variabili di config (nessun segreto in chiaro: `BASHT_AUTH_PASSWORD` da `env_file` o variabile d'host), `expose: ["8080"]`, `networks: [internal]`, `restart: unless-stopped`. Il servizio `nginx` è in 4.2.
- **Change:**
  Preconditions: P3 chiuso con G-BENCH PASS; binario compila (`go build ./cmd/bashtransfer`).
  Contract: l'immagine non contiene file del repository tranne il binario; utente non root; nessuna porta pubblicata dall'host per `app`.
  Steps:
  1. S1 — scrivere `Dockerfile` e `.dockerignore`; expected: `docker build` produce immagine (**richiede immagini base locali o rete autorizzata**).
  2. S2 — scrivere `compose.yaml` (solo `app`); expected: `docker compose -f compose.yaml config` exit 0 e nessun `ports:` per `app`.
  3. S3 — `docker run --rm` del binario con `BASHT_LISTEN=:8080` e `GET /` su rete interna del container di test; expected: testo d'uso.
  Recovery boundary: dopo S1.
  Failure handling: se `docker build` fallisce per rete o immagini base mancanti → G-P4 BUILD = blocked, registrato come blocker, non come pass. S2 resta eseguibile offline.
- **Unit tests:** N/A — infrastruttura. Verifica di discovery: `docker compose config` mostra il servizio `app`; grep di `ports:` nel servizio `app` vuoto.
- **e2e tests:** `docker build` + `docker run` + `scripts/e2e.sh` puntato alla porta del container; blocked finché la rete o le immagini locali non sono disponibili. Gate G-D1.
- **Done:** `compose config` pass; `Dockerfile` scritto; build pass oppure blocker registrato con motivazione; review agent-1 registrata; unità chiusa.

### 4.2 nginx davanti ad app (R2 confermato in larga parte; rete autorizzata)
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** scrivere la configurazione draft; review agent-1 solo dopo R2 confermato.
- **Files:**
  - NEW `deploy/nginx/nginx.conf` (rev 8): blocco `stream` con `listen 443 ssl` (certificati montati da host in `/etc/nginx/certs/`) e `proxy_pass app:8080`, `proxy_timeout 3600s`; blocco `http` solo per il redirect 80→443. Il proxy HTTP non è usato per il relay (B-NGX, overview R2 rev 8).
  - WRITE `compose.yaml`: servizio `nginx` (immagine pinnata), volume `deploy/nginx` e certificati host, porte 80 e 443 pubblicate, rete `internal`.
- **Change:**
  Preconditions: 4.1 chiuso; `client_body_timeout` letto da nginx.org (S1); immagine nginx pinnata scaricabile (rete autorizzata).
  Contract: nessuna direttiva non confermata; ogni direttiva del draft riporta la fonte in STATE §2.
  Steps:
  1. S1 — leggere `client_body_timeout` da https://nginx.org/en/docs/http/ngx_http_core_module.html e, dal modulo proxy, verificare che con `proxy_request_buffering off` un body `Transfer-Encoding: chunked` del client (RS-2) venga inoltrato all'upstream senza bufferizzazione; registrare entrambi in R2 con valore, contesto e versione; expected: valori e fonte in overview.
  2. S2 — scrivere `bashtransfer.conf` e il servizio `nginx` in compose; expected: `nginx -t` pass se il binario nginx è locale, altrimenti `docker compose config` pass.
  3. S3 — verifica RS-1 attraverso nginx (in 4.3 / G-FINAL).
  Failure handling: `client_body_timeout` ostacola upload lenti → escalation a agent-1 con test e numeri; nessuna modifica del contratto di attesa D7.
- **Unit tests:** N/A — configurazione. Verifica: `nginx -t` su configurazione in container o locale quando disponibile.
- **e2e tests:** RS-1 attraverso nginx con upload da 256 MiB senza troncamento (sha256 identico); RS-2 (`cat file | curl -T - .../mydir.tgz`) attraverso nginx con sha256 identico; blocked con 4.2.
- **Done:** config scritta con i parametri di R2; `nginx -t` pass (immagine o binario locale); e2e attraverso nginx pass con upload di 256 MiB senza troncamento (sha256 identico); review agent-1 registrata.

### 4.3 Documentazione
- **Model:** agent-2 (claude-haiku-5-5)
- **Assignment:** scrivere `README.md` (documentazione sostanziale, sub-phase separata giustificata). Review agent-1 su accuratezza rispetto al comportamento verificato (`scripts/e2e.sh`, test).
- **Files:**
  - NEW `README.md` (italiano, come il progetto).
  - READ: `Specifiche.md`, `docs/plans/001_plan-Bashtransfer/overview.md` (decisioni), `internal/config/config.go` (variabili), `scripts/e2e.sh`.
- **Change:**
  Preconditions: 4.1 chiuso; 4.2 chiuso o SKIPPED con motivazione; 2.1 DONE o SKIPPED (per la sezione auth).
  Contract del README: sezioni in ordine: cosa fa; comandi di mittente e ricevente per RS-1 (upload da file, URL senza `/` finale come in Specifiche) e RS-2 (`sudo tar -cpzf - dir | curl -T - https://<host>/dir.tgz`), curl e wget, esempi dal blocco reale; `curl -N` solo per output su pipe (R1); configurazione (tabella variabili da `config.go`, con default); autenticazione (scope da Q1); deploy con `docker compose`; nginx (solo se 4.2 chiuso); limiti e sicurezza: lo slug è il segreto, nessun nome nel path → file `download` (R4 caso C: `tar ... | curl -T - https://host` senza `/nome`), nessun TLS senza nginx, upload anonimi se auth disattivata, un solo ricevente per slug, attesa massima `BASHT_WAIT_TIMEOUT`, inattività `BASHT_IDLE_TIMEOUT`, nessuna persistenza; prestazioni: solo numeri da STATE §7 con la macchina di misura.
  Steps:
  1. S1 — scrivere README con la struttura sopra; expected: ogni variabile in tabella esiste in `config.go`.
  2. S2 — verifica incrociata: ogni comando del README è eseguito dallo script e2e o marcato come non verificato.
  Failure handling: una affermazione non verificata va scritta come tale, non come fatto.
- **Unit tests:** N/A — documentazione. Controllo: `grep` delle variabili di config nel README; nessuna variabile documentata che non esista.
- **e2e tests:** N/A — nessun comportamento nuovo.
- **Done:** README scritto; verifica incrociata registrata in STATE §7; review agent-1 registrata; unità chiusa.

## Phase gates and closure
- Required gates: G-D1 (compose config, build se disponibile), G-E2E-FINAL (RS-1 attraverso nginx, blocked con 4.2), G-FINAL (ultima fase: `go test -race -count=1 ./...`, `scripts/e2e.sh`, `scripts/bench.sh` già PASS in P3, RS-1 attraverso compose+nginx).
- Assertions: RS-1 e RS-2 completi (upload/comandi/download sha256 identico/410; RS-2 con nome `mydir.tgz`). Nessuna direttiva nginx non confermata.
- README obligation: 4.3.
- Every sub-phase DONE o SKIPPED con motivazione. Se 4.2 BLOCKED: P4 non chiude e `Scope result` resta RUNNING/BLOCKED; mai COMPLETE.
- Supervisor agent-1 reviews: direttive nginx (4.2), accuratezza README (4.3), chiusura finale.
- Chiusura P4 e final: unità `P4` in STATE §4; `Scope result: COMPLETE` solo se G-FINAL PASS, altrimenti `BLOCKED` con blocker tecnico registrato in STATE §9.
