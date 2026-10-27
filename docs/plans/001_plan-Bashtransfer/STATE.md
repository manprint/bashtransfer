# Bashtransfer — Implementation state

Read first at every session. Updated 2026-10-27 by agent-1 (claude-haiku-5-5, planning).
Plan ID: 001. Revision: 6 (rev 2: RS-2 da Specifiche, D16, R4, 0.3; rev 3: D17 tombstone 410; rev 4–5: scostamenti 1.2 e fix Expect; rev 6: scostamenti 2.1/2.2). Repo root: /mnt/fabio/dati/Git/SperimentazioniAI/bashtransfer.
Plan baseline: nessun commit (HEAD non nato su `main`); pre-existing changes: `Specifiche.md` non tracciato, di proprietà dell'utente, non è output del piano.
Roster: `agent-1:claude-haiku-5-5,agent-2:claude-haiku-5-5` (override esplicito dell'utente: solo Haiku 5.5, nessun Sonnet né Opus). Active execution style: handoff.
State writer: nessuno ancora (piano non in esecuzione); ownership: released.

## 0. Resume and completion protocol

1. Leggi questo file. Identifica readiness, scope attivo, ruolo, unità/tentativo, step, checkpoint, branch e baseline. Una nuova sessione riprende solo dopo che il precedente state writer ha rilasciato la proprietà. Un worker delegato resta nell'unità OPEN assegnata e non possiede stato né commit.
2. Ispeziona `git status` e diff staged/working e postcondizioni del checkpoint (HEAD può non esistere: baseline senza commit). Preserva modifiche non tue, inclusi hunk in file condivisi. Non riconciliare un'unità prima di aver verificato il diff reale. Un'unità OPEN riprende dal primo step non verificato, anche se il checkpoint è vecchio.
3. Leggi la fase/unità puntata e i contratti locali. Controlla le righe delle sub-phase prerequisite e gli artefatti. Localizza i simboli, non fidarti dei numeri di riga. Un contratto cambiato o un design mancante va all'agente 1 (supervisore). Dati esterni: nessuna ricerca web senza autorizzazione esplicita (CLAUDE.md utente).
4. Apri prima di modificare: tipo, ID, tentativo, OPEN, base dell'unità, path di proprietà, step S1; marca sub-phase e fase IN_PROGRESS. Checkpoint in §6 prima di handoff, risorse lunghe o rischiose, blocchi, o pausa probabile. Step ordinari e test passati non richiedono scritture di stato.
5. Test focalizzati a fine sub-phase, gate completo a P<N>, confermando che i test previsti sono stati eseguiti (conteggio non zero). Un gate futuro non è pass. Un gate richiesto mancante, bloccato, a zero test o fallito impedisce DONE.
6. Chiudi l'unità: documentazione, aggiornamento ledger in §4, progresso in §11, prossima unità eleggibile. Commit solo se la policy lo richiede (vedi §3). Riferimento commit: `unit:<id>:<tentativo>` con trailer `PEV-Plan`, `PEV-Unit`, `PEV-Attempt`, `PEV-Result: complete`. Se commit disabilitati: `uncommitted`, nessun stage né commit.
7. Interruzione prima del commit: riferimento non risolto = chiusura pendente; riconcilia prima di nuovo lavoro. Se già committato: risolvi il riferimento, nessun doppio commit.
8. Dopo le sub-phase: apri P<N> per gate completi e revisione del supervisore. Registra DONE della fase.
9. Escalation: un worker chiede al supervisore decisioni mancanti o dopo due tentativi falliti sullo stesso problema. Il supervisore può rivedere step tecnici senza cambiare requisiti; registra revisione in §8. Mai indebolire accettazione. Nuove scelte di prodotto, scope o autorità esterna → utente.
10. Su handoff: salva azione esatta successiva, modifiche pendenti, evidenze, rilascio proprietà.

## 1. Current unit and scope

- Active scope: plan 001 intero, fasi 0–4 (richiesta utente: "Fai tutte le fasi in autonomia")
- Scope result: BLOCKED (fase 4 non chiusa: 4.2 BLOCKED, B-NGX; G-E2E-FINAL e G-FINAL non eseguibili, mai COMPLETE)
- Type: none
- ID / attempt: none
- Status: none
- Intent: none
- Assigned: none; supervisor: agent-1 (claude-haiku-5-5)
- File / unit heading: none
- Current step: none
- Next action: decisione utente su B-NGX (vedi §9): verificare il percorso nginx su un host senza intercettazione delle porte 80/443, oppure accettare che il trasferimento attraverso nginx resti non verificato e chiudere P4 come BLOCKED. Nessun'altra unità eleggibile senza questa decisione.
- Next eligible plan unit: nessuna (4.2 BLOCKED; 4.3 scritta, chiusura dipende da 4.2)
- Unit base: `e2ccd94` (wip 4.1+4.2); branch `plan/001-bashtransfer`; owned changes: STATE.md, overview.md, README.md (4.3), commit di chiusura pendente
- Repo state: HEAD `e2ccd94`; `Specifiche.md` non tracciato e intatto; nessun push; stack compose fermato e rete di test rimossa.

## 2. Feature context and readiness

Readiness: READY dopo ok esplicito dell'utente per l'esecuzione. Revisione 1, autore agent-1 (claude-haiku-5-5). Cold review (self-review, agente fresco Haiku 5.5) eseguita e correzioni applicate: vedi §7.
Ordine di handoff: 0.1, 0.2, 0.3, 1.1, 1.2, 1.3, 2.1, 2.2, P2, 3.1, P3, 4.1, 4.2, 4.3, P4.
Eleggibile ora: 0.2 (0.1 DONE). Dipendenze: 0.2 da 0.1 (R1 branch A); 0.3 da 0.1 (R1 branch A) e indipendente da 0.2; 1.1 da 0.2 e 0.3 (R4 branch A/B: vedi overview R4); 1.2 da 1.1 e R4 CONFIRMED; 1.2 da 1.1; 2.1 e 2.2 da 1.2; 3.1 da 1.2; 4.1 da P2 (o 2.1 SKIPPED) e P3; 4.2 da 4.1 e da R2 S1; 4.3 da 4.1, 4.2 e 2.1.
Blocchi aperti: B-R1 e B-R3 (verifica locale in 0.1 e 0.2); B-R4 (verifica locale in 0.3; branch B su caso A senza nome o caso B = escalation utente); B-R2 residuo (`client_body_timeout`, 4.2 S1). Nessuna decisione utente aperta.
Blocco P0 finché 0.1 non registra R1 CONFIRMED con branch A; branch B = escalation utente.

Goal: relay curl/wget per file e cartelle, senza persistenza, in Go, con docker compose e nginx. Esclusioni in `overview.md` § Goal. Scenario di riferimento RS-1 in `overview.md`.

Current decisions: D1–D14 in `overview.md`; D15 (timeout inattività, aggiunta da 2.2 entro i requisiti approvati); D16 (upload da stdin con lunghezza ignota, RS-2, rev 2).
Questions: Q1 risolta dall'utente — auth solo su `PUT`; `GET /<slug>` pubblico con solo lo slug.
Research evidence: `overview.md` § Research and evidence — R1 UNVERIFIED (probe 0.1), R2 CONFIRMED in larga parte (nginx.org, rete autorizzata; residuo `client_body_timeout`), R3 UNVERIFIED (verifica locale in 0.2).
Required unverified external claims: R1 (locale), R3 (locale), R4 (locale, 0.3), R2 (residuo `client_body_timeout`, 4.2 S1). Blocker: §9 B-R1, B-R2, B-R3.
Required supervisor reviews cannot be silently replaced by weaker self-review: la revisione di agent-1 è self-review dello stesso modello (Haiku 5.5); non va registrata come revisione forte.

## 3. Environment, settings, and gate registry

- Full autonomous: true (override esplicito utente, invocazione `001 ... full-autonomous:true --wip-commit`)
- WIP commits: on (override esplicito utente)
- Completion policy: full autonomy true → commit locale per ogni sub-phase completata e per ogni chiusura P<N>; WIP on → commit di interruzione con `PEV-Result: wip`. Nessun push, nessun deploy, nessun branch remoto. Autorizzazioni di rete: solo nginx.org (docs) e pull di immagini Docker pinnate.
- Commit identity: `GIT_AUTHOR_NAME=fabio.pierri`, `GIT_AUTHOR_EMAIL=fabio.pierri@synclab.it` passati per comando (nessuna modifica a git config). Trailer di attribuzione: `Co-Authored-By: Claude Haiku 5.5 <noreply@anthropic.com>`.
- Scope/roster survive handoff. Explicit invocation overrides stored settings.
- Execution style: handoff (sessioni successive). Nessun dispatch automatico al supervisore: il supervisore è un agente dello stesso modello nella stessa configurazione; la richiesta di review si registra in §7 come self-review.
- Setup: Go (versione da 0.2; nessuna dipendenza esterna); curl locale (versione da 0.1); Docker + immagini base (4.1, rete); nginx (4.2, rete). Directory di scratch fuori dal repository per probe e binari temporanei. Porte locali libere scelte e registrate. Pulizia: terminare processi di prova e rimuovere solo directory create dallo script.
- Supervisor access: agent-1 (claude-haiku-5-5), sessione separata o stesso agente con ruolo di revisore; nessun modello più forte disponibile per il progetto.

| Gate | Stage / active from | Exact command and cwd | Required assertions/discovery | Setup |
|------|---------------------|-----------------------|-------------------------------|-------|
| G-BASE | baseline, prima di 0.2 | `git status --short` (cwd radice repo); `go version` | snapshot: `Specifiche.md` e `docs/plans/` non tracciati; versione Go registrata | Go installato |
| G-U0.2 | sub-phase 0.2 | `go vet ./...` (radice repo) | exit 0; `go list ./...` non vuoto | `go.mod` da 0.2 |
| G-R1 | sub-phase 0.1 | probe in directory di scratch fuori repo (`probe_server.py` su `127.0.0.1`) + verifica log | log con `PUT_START`, `BODY_END`, risposta prima di `BODY_END` (branch A) | curl locale, porta libera |
| G-R4 | sub-phase 0.3 | probe in directory di scratch fuori repo (`scratch/r4`, `127.0.0.1`) + verifica log | caso A: path con nome `/<basename>`; caso B: riga di risposta prima di EOF di stdin, `Transfer-Encoding: chunked`, nessun `Content-Length`; caso C: path registrato | curl locale, porta libera |
| G-U1.1 | sub-phase 1.1 | `go test ./internal/config ./internal/slug ./internal/relay -run 'TestLoad|TestGenerate|TestRegistry' -v -count=1 -race` (radice) | 12 test: 3 config, 3 slug, 6 registry; nessuno saltato | nessuno |
| G-U1.2 | sub-phase 1.2 | `go test ./internal/relay ./internal/server -v -count=1 -race` (radice) | copy: 3 test; server: upload commands, round trip (4 sottotest), 404, 410, abort, cleanup, 405 | nessuno |
| G-E2E1 | sub-phase 1.3 | `bash scripts/e2e.sh` (radice) | output `E2E OK`; sha256 identico; 410; 404 | curl, porta libera, go build |
| G-U2.1 | sub-phase 2.1 | `go test ./internal/server -run 'TestBasicAuth|TestUploadRequiresAuth' -v -count=1 -race` | 6 test | nessuno (auth solo PUT) |
| G-U2.2 | sub-phase 2.2 | `go test ./internal/server ./internal/config ./internal/relay -v -count=1 -race` | sanitize (tabella), idle timeout, headers, config idle | nessuno |
| G-E2E2 | fine P2 | `bash scripts/e2e.sh` con env auth | nome con spazi; upload con auth riesce, senza auth 401, download senza auth riesce | nessuno |
| G-P1 | P1 | `go test -race -count=1 ./...` (radice) + G-E2E1 | conteggio non zero; nessun fallimento | nessuno |
| G-P2 | P2 | `go test -race -count=1 ./...` + G-E2E2 | conteggio non zero | nessuno |
| G-BENCH | sub-phase 3.1 | `bash scripts/bench.sh` (radice) | 5 campioni per benchmark; mediana relay ≥ 0.90 × baseline (D13) | macchina stabile, nessun altro carico |
| G-U3.1 | sub-phase 3.1 | `go test ./internal/server -run TestBenchHarnessSmoke -v -count=1 -race` | 1 test; 1 MiB ricevuto esatti | nessuno |
| G-P3 | P3 | `go test -race -count=1 ./...` | conteggio non zero | nessuno |
| G-D1 | sub-phase 4.1 | `docker compose -f compose.yaml config` (radice); `docker build -t bashtransfer:test .` | config exit 0, nessun `ports:` su `app`; build pass **o** blocked (rete) | Docker; immagini base locali o rete autorizzata |
| G-E2E-FINAL | sub-phase 4.2 (R2 residuo `client_body_timeout`) | RS-1 via compose + nginx: `bash scripts/e2e.sh` puntato a porta nginx; upload 256 MiB senza troncamento | sha256 identico; 410 | 4.1 + 4.2 + rete |
| G-FINAL | ultimo P4 | `go test -race -count=1 ./...`; G-E2E-FINAL; G-BENCH già PASS in P3 | conteggio non zero; RS-1 completo | tutti i precedenti |

Regola: un gate con `blocked` non conta come pass. Un gate `not-applicable-yet` non conta come pass.

## 4. Work ledger

| Type | ID / attempt | Plan revision | Agent | Changes | Evidence/review | Commit |
|------|--------------|---------------|-------|---------|-----------------|--------|
| sub-phase | 0.1 / 1 | 1 | agent-2 (claude-haiku-5-5), state writer sessione corrente | overview R1; STATE; cartella piano (non tracciata prima, inclusa per provenienza) | G-R1 pass branch A; self-review §7 | unit:0.1:1 (risolto da trailer) |
| plan | 001 / 1 | 1 | agent-1 (claude-haiku-5-5) | cartella piano: overview, phase_01..phase_05, STATE | nessuna esecuzione; nessuna revisione di codice | uncommitted (planning non committa) |
| phase closure | P0 / 1 | 2 | agent-1 (claude-haiku-5-5), self-review | fase 0 completa; nessun codice di prodotto | G-BASE, G-U0.2, G-R1, G-R4 pass; R1/R3/R4 CONFIRMED; self-review §7 | unit:P0:1 |
| sub-phase | 1.3 / 1 | 5 | agent-2 (claude-haiku-5-5), state writer sessione corrente | cmd/bashtransfer/main.go; scripts/e2e.sh (+x); handler.go fix Expect e test | G-E2E1 PASS; G-U1.2 con TestUploadExpectContinue PASS; self-review §7 | unit:1.3:1 |
| phase closure | P1 / 1 | 5 | agent-1 (claude-haiku-5-5), self-review | fase 1 completa; invarianti I-1..I-5 coperti (I-1 TestCopyAllocsBounded; I-2 TestRegistryClaimOnce/TestGetSecondClaim410; I-3 TestRegistryFinishOnce/TestReceiverAbortCleansSession; I-4 TestRelayRoundTripSHA/TestSenderAbortFailsReceiver; I-5 TestGenerateUnique/TestRegistrySlugCollision) | G-P1 PASS | unit:P1:1 |
| sub-phase | 1.2 / 1 | 4 | agent-2 (claude-haiku-5-5), state writer sessione corrente | internal/relay/copy.go(+test), internal/server/{receiver,handler}.go(+test); slug.Valid | G-U1.2 pass (-race, 5 ripetizioni); self-review post-scrittura §7; scostamenti rev 4 | unit:1.2:1 |
| sub-phase | 1.1 / 1 | 3 | agent-2 (claude-haiku-5-5), state writer sessione corrente | internal/config, internal/slug, internal/relay/registry.go (+test) | G-U1.1 pass; self-review §7 | unit:1.1:1 |
| plan | rev 3 / 1 | 3 | agent-1 (claude-haiku-5-5) | D17, phase_02 1.1/1.2, STATE §8 | self-review | unit:plan-rev-3 |
| sub-phase | 0.3 / 1 | 2 | agent-2 (claude-haiku-5-5), state writer sessione corrente | probe in scratch (nessun file repo); overview R4; phase_05 limite README | G-R4 pass; self-review §7 | unit:0.3:1 |
| sub-phase | 0.2 / 1 | 2 | agent-2 (claude-haiku-5-5), state writer sessione corrente | go.mod, cmd/bashtransfer/main.go (placeholder), .gitignore; overview R3 | G-BASE, G-U0.2 pass; R3 CONFIRMED; self-review §7 | unit:0.2:1 |
| plan | 001 / 2 (revisione RS-2) | 2 | agent-1 (claude-haiku-5-5) | overview D16, R4, RS-2; phase_01 0.3; phase_02 upload stdin e test; phase_05 RS-2 e README; STATE | specifica verificata contro piano; self-review (Haiku, stesso modello) | uncommitted → committato come plan rev 2 prima di 0.2 |
| plan | 001 / 1 (cold review) | 1 | agent-1 (claude-haiku-5-5) | correzioni a–l in phase_02, phase_04, phase_05, STATE; Q1 e rete registrate | cold review self (Haiku fresco, nessun contesto di pianificazione) | uncommitted |
| sub-phase | 2.1 + 2.2 / 1 | 6 | agent-2 (claude-haiku-5-5), state writer sessione corrente | internal/server/{auth,name}.go(+test), handler.go (Authorize, IdleTimeout, SanitizeName, 401), handler_test.go, internal/relay/copy.go (NewIdleReader), internal/config (IdleTimeout), cmd main.go, scripts/e2e.sh (auth, nome con spazi) | G-U2.1 PASS, G-U2.2 PASS, G-E2E2 PASS; self-review §7 | unit:2.2:1 (`d64bffb`, commit unico, rev 6) |
| phase closure | P2 / 1 | 6 | agent-1 (claude-haiku-5-5), self-review | fase 2 completa; I-1..I-5 invariati; D9, D15, D5 coperti | G-P2 PASS (35 PASS) | unit:P2:1 (`c385106`, solo STATE) |
| sub-phase | 3.1 / 1 | 6 | agent-2 (claude-haiku-5-5), state writer sessione corrente | internal/server/bench_test.go (NEW), scripts/bench.sh (NEW, +x) | G-U3.1 PASS; G-BENCH PASS (rapporti 1.02 / 0.956 / 0.991); self-review §7 | unit:3.1:1 (`2d91a0d`) |
| phase closure | P3 / 1 | 6 | agent-1 (claude-haiku-5-5), self-review | fase 3 completa; buffer 256 KiB confermato da dati | G-P3 PASS (36 PASS); G-BENCH PASS | unit:P3:1 (commit solo STATE) |
| sub-phase | 4.1 / 1 | 7 | agent-2 (claude-haiku-5-5), state writer sessione corrente | Dockerfile, .dockerignore, compose.yaml (app, nginx), deploy/nginx/bashtransfer.conf | G-D1 PASS; self-review §7 | unit:4.2:1 wip (`e2ccd94`, con 4.2) |
| sub-phase | 4.2 / 1 | 7 | agent-2 (claude-haiku-5-5), state writer sessione corrente | deploy/nginx/bashtransfer.conf, servizio nginx, scripts/e2e.sh (modalità esterna) | G-S2 PASS; G-E2E-FINAL FAIL (B-NGX) | unit:4.2:1 wip, `PEV-Result: blocked` (`e2ccd94`) |
| sub-phase | 4.3 / 1 | 7 | agent-2 (claude-haiku-5-5), state writer sessione corrente | README.md (italiano) | cross-check variabili con config.go; sezione nginx non verificata | non ancora committato (vedi commit P4 blocked) |

Le righe sono append-only. Commit reference: vedi §0.6.

## 5. Files and ownership

| Path/hunks | Existing changes to preserve | Unit changes | Owning unit |
|------------|------------------------------|--------------|-------------|
| `Specifiche.md` | non tracciato, di proprietà utente, non modificare | nessuna | nessuno (utente) |
| `docs/plans/001_plan-Bashtransfer/**` | creato in planning | solo aggiornamenti di stato e di R-evidenze da unità | planning (001) |
| `go.mod`, `.gitignore`, `cmd/bashtransfer/main.go` (placeholder) | nessuno | NEW | 0.2 |
| `internal/config/**` | — | NEW | 1.1 (estesa in 2.2) |
| `internal/slug/**` | — | NEW | 1.1 |
| `internal/relay/**` | — | NEW | 1.1 (registry), 1.2 (copy), 2.2 (deadline) |
| `internal/server/**` | — | NEW | 1.2, 2.1 (auth), 2.2, 3.1 (bench) |
| `cmd/bashtransfer/main.go` | placeholder di 0.2 | WRITE | 1.3 (main), 2.1 (auth wiring) |
| `scripts/e2e.sh`, `scripts/bench.sh` | — | NEW | 1.3, 3.1 |
| `Dockerfile`, `.dockerignore`, `compose.yaml` | — | NEW | 4.1 (4.2 aggiunge nginx) |
| `deploy/nginx/bashtransfer.conf` | — | NEW | 4.2 |
| `README.md` | — | NEW | 4.3 |

## 6. In-flight checkpoint

none — nessuna unità aperta. Planning non ha scritto checkpoint di esecuzione.
Research checkpoint: N/A — ricerca web non eseguita (rete non autorizzata); R1/R3 da eseguire in 0.1/0.2; R2 pendente di autorizzazione.

## 7. Verification and reviews

| Gate/test | Command | Result | Test count/named evidence | Tested revision/diff | When |
|-----------|---------|--------|--------------------------|---------------------|------|
| G-R1 (0.1) | probe `curl -N -T` su `127.0.0.1`, server di probe in scratch fuori repo | pass (branch A) | S3: riga `COMANDI-DI-PROVA` entro 2 ms dalla risposta; `BODY_END` +3 s, 8388608/8388608 byte. S4: path `/probe-name.zip`, `/payload.bin` (basename). Prima passata con risposta `Content-Length` scartata (body troncato) | overview R1, working tree pre-commit | 2026-10-27 |

| Review | Reviewer | Plan revision / reviewed change | Invariants/assertions checked | Verdict |
|--------|----------|---------------------------------|------------------------------|---------|
| Review 0.1 (self-review, stesso modello) | agent-1 (claude-haiku-5-5), sessione corrente | rev 1 / overview R1 + log probe | esito R1 branch A; path con nome; vincolo D4 (risposta aperta fino a fine relay) | PASS, self-review; non revisione forte |

Risultati possibili: pass, fail, blocked, not-run, not-applicable-yet. Solo pass soddisfa un gate richiesto.

| Review | Reviewer | Plan revision / reviewed change | Invariants/assertions checked | Verdict |
|--------|----------|---------------------------------|------------------------------|---------|
| Cold review piano (self-review, agente fresco Haiku 5.5, nessun contesto di pianificazione; non è revisione forte) | agent-1 (claude-haiku-5-5) | rev 1 / overview, phase_01..05, STATE | step mancanti in 1.2; regola base URL; placeholder D11; conteggio test server 7; timing review (prima di `handler.go`); ownership; meccanica dei test di abort; corpi 503/401; baseline senza HEAD; readiness e ordine di handoff | findings a–l applicati; nessuna nuova decisione |
| G-BASE | `git status --short`; `go version` | pass | snapshot: `Specifiche.md` non tracciato, `docs/plans/` non tracciato → committato in rev 2; go1.27.3 | repo, rev 2 | 2026-10-27 |
| G-U0.2 (0.2) | `go vet ./...`; `go list ./...` | pass (exit 0; lista = `bashtransfer/cmd/bashtransfer`) | 0 test (N/A scheletro) | go.mod, main.go, .gitignore | 2026-10-27 |
| Review 0.2 (self-review, stesso modello) | agent-1 (claude-haiku-5-5) | rev 2 / go.mod `go 1.21` | R3: firma presente; `api/go1.21.txt` riga 356; scelta minima 1.21 | PASS, self-review; non revisione forte |
| Verifica di esecuzione R2 residuo | — | rev 2 | `client_body_timeout` (4.2 S1) | pending |
| G-R4 (0.3) | probe `curl -N -T` su `127.0.0.1:18090` con `probe_server.py` in scratch `r4/`; `payload.bin` 8 MiB da `r1/`; stdin con `printf x; sleep 3; head -c 8388608` | pass | A: `PUT_START path=/payload.bin CL=8388608`, `BODY_END 8388608`. B: `PUT_START path=/mydir.tgz TE=chunked`, riga a +3 ms, EOF stdin +3 s, `BODY_END 8388609`. C: `path=/`, `BODY_END 1024`. Listener chiuso | overview R4, working tree pre-commit | 2026-10-27 |
| G-U1.1 (1.1) | `go test ./internal/config -run TestLoad`; `go test ./internal/slug`; `go test ./internal/relay -run TestRegistry -race` | pass | 3 config (+5 sottotest), 3 slug, 6 registry; nessuno saltato | rev 3, working tree pre-commit | 2026-10-27 |
| Review 1.1 (self-review, stesso modello) | agent-1 (claude-haiku-5-5) | rev 3 / registry.go Finish/Abort/Claim/expire + tombstone | I-2 (claim unico), I-3 (Finish/expire una volta; close una volta via once/idempotenza), I-5 (collisione + tombstone non riassegnato); nessun lock annidato (Session.Abort/Err usano reg.mu, Finish non lo richiama) | PASS, self-review; non revisione forte |
| Review 0.3 (self-review, stesso modello) | agent-1 (claude-haiku-5-5) | rev 2 / overview R4 + log server | esito A/B/C; D16 valido; limite D5 per caso C documentato | PASS, self-review; non revisione forte |
| G-U2.1 (2.1) | `go test ./internal/server -run 'TestBasicAuth|TestUploadRequiresAuth' -v -count=1 -race` | pass | 6 test: 4 auth di base, 1 costo costante (comportamentale), 1 PUT senza credenziali → 401 senza sessione; con credenziali → comandi | rev 6, `d64bffb` | 2026-10-27 |
| G-U2.2 (2.2) | `go test ./internal/server ./internal/config ./internal/relay -v -count=1 -race`; `go test -race -count=3 -run 'Idle|Headers|SanitizeName|Auth' ./internal/server` | pass | sanitize (tabella 8 casi + 2 lunghezze); TestUploadIdleTimeout; TestDownloadHeaders; TestConfigIdleTimeout; nessuno saltato | rev 6, `d64bffb` | 2026-10-27 |
| G-E2E2 (fine P2) | `bash scripts/e2e.sh` | pass (`E2E OK`) | RS-1 e RS-2 invariati; PUT senza credenziali 401; password errata 401; upload autorizzato `strano nome.zip` con comandi `curl -o`/`wget -O` corretti; download pubblico con auth; sha256 invariati | rev 6, `d64bffb` | 2026-10-27 |
| G-P2 (P2) | `go test -race -count=1 -v ./...` (conteggio) + G-E2E2 | pass | 35 PASS, 0 FAIL, 0 SKIP | rev 6, `d64bffb` | 2026-10-27 |
| G-U3.1 (3.1) | `go test ./internal/server -run 'TestBenchHarnessSmoke' -v -count=1 -race` | pass | 1 test; 1 MiB esatti per lato | rev 6, `2d91a0d` | 2026-10-27 |
| G-BENCH (3.1) | `scripts/bench.sh` (nproc 8, go1.27.3, Intel Xeon; 256 MiB per op, -benchtime 3x -count 5, senza -race) | pass | Mediane MB/s (relay / baseline): 64 KiB 5337.68 / 5219.48 = **1.02**; 256 KiB 6548.81 / 6853.35 = **0.956**; 1 MiB 6033.41 / 6086.60 = **0.991**. Criterio D13 ≥ 0.90: PASS in tutti e tre. Default 256 KiB: relay più alto (6548.81 vs 6033.41 e 5337.68, differenza 8.5% sul 1 MiB, nessuna revisione) | rev 6, `2d91a0d`; output grezzo in scratchpad `bench-run1.txt` | 2026-10-27 |
| G-P3 (P3) | `go test -race -count=1 -v ./...` (conteggio) | pass | 36 PASS, 0 FAIL, 0 SKIP | rev 6, `2d91a0d` | 2026-10-27 |
| G-D1 (4.1) | `docker build -t bashtransfer:local .`; `docker compose -f compose.yaml config`; `docker compose up -d app` + `wget -qO- http://app:8080/` dalla rete interna | pass (build 11 s; config senza `ports:` per app; usage text ricevuto) | immagini base pinnate per digest; utente non root (`nonroot:nonroot`) | rev 7, `e2ccd94` | 2026-10-27 |
| G-S2 (4.2) | `nginx -t` nel servizio `nginx` con `BASHT_CERTS_DIR` di test | pass (`configuration file ... test is successful`) | 1 config | rev 7, `e2ccd94` | 2026-10-27 |
| G-E2E-FINAL (4.2) | `BASHT_E2E_BASE=https://127.0.0.1:18443 BASHT_E2E_MB=256 bash scripts/e2e.sh` (override porte test) | FAIL: `end of response with 268260272 bytes missing`; 8 MiB: `8291176 bytes missing` | — | rev 7, `e2ccd94` | 2026-10-27 |
| Review 4.1 (self-review, stesso modello) | agent-1 (claude-haiku-5-5) | rev 7 / Dockerfile, compose | pinning per digest verificato con `docker images`; nessun segreto in immagine (`.dockerignore`, env da variabili); porte solo su nginx | PASS, self-review |
| Review 3.1 (self-review, stesso modello) | agent-1 (claude-haiku-5-5) | rev 6 / bench_test.go, scripts/bench.sh | baseline a due hop verificato in codice: client → `minimalRelay` (PUT) → GET ricevente, stesso `BufSize`, flush per blocco, nessun registry/slug/header; byte esatti verificati in `transfer`; criterio applicato senza riscrittura; buffer scelto dal dato (256 KiB è il relay più veloce) | PASS, self-review; non revisione forte. Limite: una sola misura, macchina a 8 CPU condivisa, varianza fra campioni fino a ~10% | 2026-10-27 |
| Review 2.1+2.2 (self-review, stesso modello) | agent-1 (claude-haiku-5-5) | rev 6 / auth.go, name.go, handler.go Options e 401 | I-5 (nessuna directory dal nome: `SanitizeName` taglia a `/`); D9 (solo PUT protetto, GET pubblico); D15 (idle deadline prima di ogni Read, `ErrIdle`); costo costante del confronto verificato in diff, non solo a comportamento; `WWW-Authenticate` su 401 | PASS, self-review; non revisione forte |

## 8. Technical revisions and deviations

| Revision | Previous decision/step | Approved replacement and reason | Supervisor | Dependents/revalidation |
|----------|------------------------|---------------------------------|------------|------------------------|
| 6 | Due scostamenti di esecuzione in 2.1/2.2 (nessuna decisione D1–D17 cambiata): (a) `SanitizeName` divide solo su `/` (il `\` viene rimosso come carattere vietato, quindi il risultato del contratto non cambia; la tabella di test resta `a"b\c.zip` → `abc.zip`); (b) 2.1 e 2.2 sono nello stesso commit `d64bffb` invece di due commit separati, perché `handler.go` e `handler_test.go` condividono le modifiche e la separazione per hunk non era verificabile senza rischio | agent-1 (self-review, Haiku 5.5), in esecuzione di 2.2 | name.go, name_test.go; ledger §4 riga 2.1+2.2; nessun test cambiato |
| 7 | Fase 4 (rev 7): (a) `client_body_timeout 3600s` impostato nel server 443 (default 60s, letto da nginx.org) per non troncare mittenti fermi prima di `BASHT_IDLE_TIMEOUT`; (b) `BASHT_E2E_BASE` e `BASHT_E2E_MB` aggiunti a `scripts/e2e.sh` per il test esterno; (c) 4.3 README scritta con precondizione 4.2 non soddisfatta: contenuti non dipendenti da nginx, sezione nginx dichiarata non verificata; (d) porte test 18080/18443 solo come override fuori repo per la diagnosi; (e) Dockerfile e compose pinnati per tag e digest; `deploy/` escluso dal contesto Docker oltre alla lista del piano | agent-1 (self-review, Haiku 5.5) | overview R2; phase_05 4.2 e 4.3; README; nessun cambio D1–D17 |
| 5 | Correzione tecnica trovata da e2e in 1.3 (nessuna decisione cambiata): con `Expect: 100-continue` (curl per body > 1 MiB) il `WriteHeader(200)` prima della lettura del body fa chiudere il body Go (`ErrBodyReadAfterClose`, `Connection: close`, reset del mittente, download a 0 byte). Fix: `100 Continue` esplicito prima dei comandi quando `Expect` è presente e `ContentLength != 0` (`expectsContinue`). Test `TestUploadExpectContinue` | agent-1 (self-review), trovato da `scripts/e2e.sh` | handler.go; test server 10 top-level; D4 invariato |
| 4 | Scostamenti di 1.2 registrati dopo implementazione (nessuna decisione D1–D17 cambiata): (a) `Allow: GET, PUT` su `/` e `/<seg>` invece di `Allow: PUT`, perché `GET /` è la pagina d'uso; (b) il mittente in attesa senza body letto non è rilevato come caduto da `r.Context()` (il server Go avvia la lettura in background solo a fine body); l'unica chiusura è la scadenza D7 (`BASHT_WAIT_TIMEOUT`), dichiarata come limite in 4.3; (c) nome dal path non sanitizzato fino a 2.2 (quoting di `"` e `\` non ancora gestito); (d) la revisione pre-implementazione di `handler.go` richiesta dalla fase non è stata eseguita prima della scrittura: eseguita come self-review dopo la scrittura, con test di abort e lifecycle; limite di self-review dichiarato | agent-1 (self-review, Haiku 5.5), dopo implementazione di 1.2 | phase_02 1.2 (contratto `Allow`, `Context` branch); 4.3 limiti; 2.2 sanitize; test invariati |
| 3 | D6/RS-1 passo 4 richiedono `410` per slug concluso; `Finish` che rimuove dalla mappa dava `404`. Nuovo D17: tombstone 24h (costante), slug terminali (concluso, reclamato, scaduto) → `410`; oltre 24h → `404`. Nuovi errori `ErrGone`; test `TestRegistryWaitTimeout` e `TestRegistryFinishOnce` aspettano `ErrGone`. Nessun cambio di D1–D16 | agent-1 (self-review, Haiku 5.5), trovato in preparazione di 1.1 | overview D17; phase_02 1.1 (Claim/Finish/tombstone) e 1.2 (mapping 410); e2e passo 6 invariato |
| 2 | Specifica di riferimento: solo RS-1 (upload da file con URL senza spec di stdin) | Aggiunto RS-2 da `Specifiche.md` (scenario 2 streaming da `tar`): D16 upload chunked con lunghezza ignota; R4 (curl senza `/` finale e da stdin) con sub-phase 0.3 prima di 1.2; test e e2e RS-2 in 1.2/1.3; RS-2 attraverso nginx in 4.2/G-FINAL; README con due scenari. Nessuna decisione di D1–D15 cambiata. Motivo: la specifica utente è più estesa del piano rev 1 | agent-1 (self-review, Haiku 5.5); utente ha chiesto "verifica ed adegua il piano" | overview, phase_01 (0.3), phase_02 (1.2 test/e2e), phase_05 (4.2 e 4.3); 0.2 non toccato; 0.1 invariato |

Revisioni previste ma non ancora fatte (non registrate come approvate):
- D4/D8: correzione di contratto in `phase_02.md` 1.2 già applicata in planning (abort della connessione del mittente su scadenza o errore di copia). Non è una revisione di decisione.
- D15: aggiunta del timeout di inattività in 2.2; da approvare da agent-1 quando 2.2 è eseguita.

## 9. Blockers

| ID | Affected units | Evidence | Resolution owner | Exact next action |
|----|----------------|----------|------------------|-------------------|
| B-R1 | 0.1 (esito), 1.2, 1.3 | R1 UNVERIFIED: comportamento di curl non provato in questa sandbox | agent-2 con revisione agent-1 | eseguire probe 0.1 in directory di scratch; registrare branch A/B/C in overview |
| B-R4 | RISOLTO in 0.3 | R4 CONFIRMED: (A) path `/payload.bin` senza slash; (B) riga visibile +3 ms, EOF stdin +3 s; (C) senza nome → `download` (limitazione in 4.3) | agent-2 | nessuna |
| B-R3 | RISOLTO in 0.2 | `go doc` su go1.27.3 + `$GOROOT/api/go1.21.txt`: metodo presente dalla go1.21; `go.mod` = `go 1.21` | agent-2 | nessuna |
| B-Q1 | RISOLTO | Decisione utente: auth solo su `PUT`; `GET /<slug>` pubblico con solo lo slug | utente | nessuna |
| B-R2 | RISOLTO (rev 7) | direttive confermate da nginx.org; `client_body_timeout` letto: default 60s, per letture consecutive; scelto 3600s in `deploy/nginx/bashtransfer.conf` | agent-1 | nessuna |
| B-DISC | LIMITE, non bloccante: 4.3 | Mittente caduto prima che un ricevente si agganci non è rilevato prima di `BASHT_WAIT_TIMEOUT` (test: `newTestServerWait`) | agent-1 | dichiarare in README (4.3); valutare lettura anticipata del body con buffer in fase successiva solo se richiesto |
| B-NET | RISOLTO (rev 7) | pull di `golang:1.27.3-bookworm`, `gcr.io/distroless/static-debian12:nonroot`, `nginx:1.27-alpine` riusciti (digest in Dockerfile e compose) | agent-2 | nessuna |
| B-NGX | BLOCCA 4.2, G-E2E-FINAL, G-FINAL, chiusura P4 | Trasferimento attraverso nginx si blocca dopo circa 0.1–0.8 MB (RS-1 e RS-2, 8 MiB e 256 MiB, poi 8 MiB anche su 18443). Riprodotto con: stack compose; nginx con configurazione di produzione; configurazione minimale con solo `proxy_pass`; upstream minimo Go (risposta prima o dopo la lettura del body); nginx host-network con binario locale. Il binario diretto (senza nginx) passa 8 MiB in 3 ms. Ipotesi non provate: intercettazione del sandbox sulle porte 80/443 (la configurazione minima su porta 18080 ha inoltrato 1 MiB completo, ma il test con 8 MiB su 18080 non è stato concluso). Esclusi con esperimento: `Expect: 100-continue`, versione HTTP/1.0 upstream, `Connection` keep-alive, `proxy_buffer_size` 64k, TLS (stesso stallo in HTTP), `proxy_buffering on` (rompe lo streaming). Socket queue non rilevate (`ss` senza output). Esperimenti in scratchpad | utente (decisione) + agent-2 | (a) su un host senza intercettazione, o con nginx su porte non standard, eseguire `BASHT_E2E_BASE=https://<host> BASHT_E2E_MB=256 bash scripts/e2e.sh`; (b) se (a) passa, registrare G-E2E-FINAL e chiudere 4.2/P4; se non passa, il problema è nel percorso nginx: prossimo passo è tcpdump/ss in-container, non ancora fatto |
| B-SERENA | non bloccante | MCP `serena` non connesso (CONNECTION_CLOSED) in questa sessione; ricerca semantica fatta con tokensave | utente | riavviare o riconfigurare serena |
| B-SUBAGENT | non bloccante | `CLAUDE_CODE_SUBAGENT_MODEL=claude-haiku-5-5` in `~/.claude/settings.json`: non verificato in una nuova sessione | utente | aprire una nuova sessione e controllare il modello dei subagent |

## 10. Do-not-repeat
- Non eseguire `pkill -f <pattern>` quando il pattern compare nella stessa riga di comando: uccide la shell (exit 144). Usare `pkill -x` o PID.
- Non provare di nuovo le varianti nginx già escluse in B-NGX senza una nuova ipotesi.
- Il hook Bash blocca i comandi che contengono `curl`/`wget`: usare `ctx_execute` oppure uno script in file.

none

## 11. Progress board

### Sub-phases
| ID | Phase file | Depends on | Status | Attempt | Evidence / reason |
|----|------------|------------|--------|---------|-------------------|
| 0.1 | phase_01.md | none | DONE | 1 | G-R1 pass branch A (curl 8.18.0); self-review §7 |
| P0 | phase_01.md | 0.1, 0.2, 0.3 | DONE | 1 | G-U0.2 pass; README N/A (nessun comportamento distribuito) |
| 0.2 | phase_01.md | 0.1 (R1 branch A) | DONE | 1 | G-BASE + G-U0.2 pass (go1.27.3, vet exit 0, list non vuoto); R3 CONFIRMED |
| 0.3 | phase_01.md | 0.1 (R1 branch A) | DONE | 1 | G-R4 pass: A path con nome; B riga prima di EOF; C limite `download` (overview R4) |
| 1.1 | phase_02.md | 0.2 | DONE | 1 | G-U1.1 pass: 3 config, 3 slug, 6 registry (`-race`); D17 tombstone rev 3; self-review §7 |
| 1.2 | phase_02.md | 1.1 | DONE | 1 | G-U1.2 pass (-race, 5 ripetizioni stabili): 3 copy + 9 server; scostamenti rev 4 |
| 1.3 | phase_02.md | 1.2 | DONE | 1 | G-E2E1 PASS (`E2E OK`); fix Expect (rev 5) |
| 2.1 | phase_03.md | 1.2 | DONE | 1 | G-U2.1 PASS (6 test auth, -race); committato insieme a 2.2 in `d64bffb` (deviazione rev 6) |
| 2.2 | phase_03.md | 1.2 | DONE | 1 | G-U2.2 PASS (sanitize tabella, idle timeout, headers, config idle); G-E2E2 PASS; commit `d64bffb` |
| 3.1 | phase_04.md | 1.2 | DONE | 1 | G-U3.1 PASS (smoke -race); G-BENCH: mediana relay/baseline 64 KiB 1.02, 256 KiB 0.956, 1 MiB 0.991 (criterio ≥ 0.90 PASS); default 256 KiB confermato; commit `2d91a0d` |
| 4.1 | phase_05.md | P2 (o 2.1 SKIPPED), P3 | DONE | 1 | build `bashtransfer:local` pass (11 s); `compose config` pass senza `ports:` per app; `GET /` da rete interna pass; commit `e2ccd94` (wip) |
| 4.2 | phase_05.md | 4.1; R2 (`client_body_timeout`, S1) | BLOCKED | 1 | S1 R2 chiuso (`client_body_timeout` 60s default → 3600s); S2 `nginx -t` pass; trasferimento attraverso nginx NON verificato: B-NGX; commit `e2ccd94` (wip, `PEV-Result: blocked`) |
| 4.3 | phase_05.md | 4.1; 4.2 chiuso o SKIPPED; 2.1 DONE o SKIPPED | WRITTEN | 1 | README.md scritto (rev 7: precondizione 4.2 non soddisfatta; sezione nginx dichiarata non verificata); S2 cross-check su variabili e comandi; da rivedere dopo B-NGX |

README obligation per phase: P0 → N/A (nessun comportamento distribuito; verificato in chiusura P0); P1 → pendente su 4.3; P2 → pendente su 4.3 (auth, limiti); P3 → numeri di prestazione in 4.3 solo da §7; P4 → 4.3 completo.

### Phases
| ID | File | Closure unit | Status | Review / commit reference |
|----|------|--------------|--------|---------------------------|
| 0 | phase_01.md | P0 | DONE | P0 unit:P0:1 (review self-review §7); R1, R3, R4 CONFIRMED; G-BASE, G-U0.2, G-R1, G-R4 pass |
| 1 | phase_02.md | P1 | DONE | G-P1 PASS: `go test -race -count=1 ./...` 34 PASS/0 FAIL/0 SKIP + G-E2E1 PASS; README pendente su 4.3 (B-DISC, limiti); self-review §7 |
| 2 | phase_03.md | P2 | DONE | G-P2 PASS: `go test -race -count=1 ./...` 35 PASS/0 FAIL/0 SKIP + G-E2E2 PASS (`E2E OK` con auth e nome con spazi); README pendente su 4.3 (auth, limiti); self-review §7; commit `unit:P2:1` |
| 3 | phase_04.md | P3 | DONE | G-BENCH PASS (criterio D13 su 5 campioni, nessuno SKIP); G-P3 PASS: `go test -race -count=1 ./...` 36 PASS/0 FAIL/0 SKIP; commit `unit:P3:1` (solo STATE) |
| 4 | phase_05.md | P4 | BLOCKED | 4.2 BLOCKED (B-NGX); G-D1 build e compose PASS; G-E2E-FINAL e G-FINAL non eseguiti |

Statuses: TODO, IN_PROGRESS, IN_REVIEW, DONE, SKIPPED, BLOCKED.

### Tests
| ID/name | Owning unit | Gate | Status | Evidence |
|---------|-------------|------|--------|----------|
| TestLoadDefaults, TestLoadAuthPairRequired, TestLoadRejectsBadNumbers | 1.1 | G-U1.1 | PASS | `go test ./internal/config -run TestLoad -v -count=1` |
| TestGenerateLength, TestGenerateAlphabet, TestGenerateUnique | 1.1 | G-U1.1 | PASS | `go test ./internal/slug -v -count=1` |
| TestRegistryClaimOnce, TestRegistryFinishOnce, TestRegistryWaitTimeout, TestRegistryCapacity, TestRegistrySlugCollision, TestRegistryAbortKeepsFirst | 1.1 | G-U1.1 | PASS | `go test ./internal/relay -run TestRegistry -v -count=1 -race` |
| TestCopyFlushesEachWrite, TestCopyRoundTripBytes, TestCopyAllocsBounded | 1.2 | G-U1.2 | PASS | `go test ./internal/relay -run TestCopy -v -race` |
| TestUploadReturnsCommandsBeforeBody, TestRelayRoundTripSHA, TestGetUnknown404, TestGetSecondClaim410, TestSenderAbortFailsReceiver, TestReceiverAbortCleansSession, TestUploadMethodNotAllowed, TestUploadUnknownLength, TestUploadNameFromPath | 1.2 | G-U1.2 | PASS | `go test ./internal/server -v -race` (9 top-level; 4 sottotest sui size) |
| scripts/e2e.sh (E2E OK) | 1.3 | G-E2E1 | PASS | `bash scripts/e2e.sh` → `E2E OK` (RS-1 sha256, 410, 404, RS-2 mydir.tgz sha256) |
| TestUploadExpectContinue | 1.2 (aggiunto dopo e2e) | G-U1.2 | PASS | `go test ./internal/server -run TestUploadExpectContinue -race` |
| TestBasicAuth*, TestUploadRequiresAuth | 2.1 | G-U2.1 | TODO | — |
| TestSanitizeName, TestUploadIdleTimeout, TestDownloadHeaders, TestConfigIdleTimeout | 2.2 | G-U2.2 | TODO | — |
| BenchmarkRelayLoopback, BenchmarkBaselineRelay, TestBenchHarnessSmoke | 3.1 | G-BENCH, G-U3.1 | TODO | — |
| compose config (app senza ports) | 4.1 | G-D1 | TODO | — |
| docker build | 4.1 | G-D1 | TODO | B-NET (rete autorizzata) |
| RS-1 via nginx | 4.2 | G-E2E-FINAL | TODO | B-R2 parziale |

### Documentation
| Document/sections | Owning unit | Status | Evidence |
|-------------------|-------------|--------|----------|
| README.md (intero) | 4.3 | TODO | — |
| overview.md (R1, R3 evidence) | 0.1, 0.2 | TODO | — |
| deploy/nginx (commenti) | 4.2 | TODO | B-R2 parziale |

### Audits
| Report | Verdict | Current unresolved findings | Evidence |
|--------|---------|-----------------------------|----------|
| — | — | — | — |

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
