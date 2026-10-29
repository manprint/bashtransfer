# Architettura

bashtransfer è un relay HTTP/1.1: il body di un `PUT` (mittente) viene copiato nella risposta di un `GET` (ricevente) mentre arriva, senza disco e senza buffer oltre quello di copia. Questo documento descrive come funziona il codice; uso, configurazione e log sono nel [README](../README.md), il deploy in [operazioni.md](operazioni.md).

## Componenti

```
client ──TLS──▶ nginx (stream, :443) ──TCP──▶ app (:8080, Go net/http)
                                               ├─ server.handler   upload / download / usage
                                               ├─ relay.Registry   sessioni, slug, tombstone
                                               └─ relay.Copier     copia con buffer riusabili
```

| Pacchetto | File | Responsabilità |
|---|---|---|
| `cmd/bashtransfer` | `main.go` | configurazione, logger, `http.Server`, segnali e arresto ordinato, `-healthcheck` |
| `internal/config` | `config.go` | lettura e validazione delle variabili `BASHT_*` |
| `internal/slug` | `slug.go` | slug di 10 caratteri `[0-9A-Za-z]` da `crypto/rand`, senza bias modulo (~59 bit) |
| `internal/relay` | `registry.go` | stato delle sessioni: unico punto che lo muta, sotto un mutex |
| `internal/relay` | `copy.go` | `Copier` (pool di buffer, flush per scrittura), `IdleReader` (inattività e interruzione del mittente) |
| `internal/server` | `handler.go` | routing, upload, download, gestione di `Range`, log |
| `internal/server` | `receiver.go` | adattatore della risposta del ricevente: header, scadenza di scrittura |
| `internal/server` | `peer*.go` | rilevamento della caduta di un mittente in attesa (Linux, `ppoll` con `POLLRDHUP`) |
| `internal/server` | `name.go`, `auth.go` | sanitizzazione del nome, Basic auth a tempo costante |

nginx usa il modulo `stream` (proxy TCP con TLS), non il proxy HTTP: l'app risponde al `PUT` con i comandi prima di leggere il body, e il proxy HTTP di nginx in quel caso interrompe l'upload.

## Ciclo di vita di una sessione

```
            PUT                        GET (Claim)                    copia finita
  ────────────▶  waiting  ─────────────────────────▶  relaying  ─────────────────▶  finished
                   │  │                                  │                          (tombstone 410)
   WAIT_TIMEOUT o  │  │ il mittente chiude               │ errore (mittente, ricevente,
   arresto server  │  │ (POLLRDHUP, ogni 1 s)            │ inattività, lunghezza)
                   ▼  ▼                                  ▼
               expired / sender_gone                  aborted
               (tombstone 410)                        (tombstone 410)
```

1. **Upload** (`handler.upload`): autorizzazione, `EnableFullDuplex` (risposta prima della lettura del body), `Registry.Create` (slug unico, timer di attesa), `100 Continue` se richiesto, tre righe di comando, flush. Poi l'handler attende in `select`:
   - `Attached()`: un ricevente ha reclamato lo slug, inizia la copia;
   - `Expired()`: timer di attesa scaduto o arresto del server;
   - `r.Context().Done()`: connessione chiusa da net/http;
   - ticker del controllo del mittente (solo Linux): `peerClosed` vede FIN o RST anche con byte del body non letti nel buffer del kernel.
2. **Download** (`handler.download`): slug valido, nessun `Range` da un byte > 0, `Registry.Claim` (riesce una volta sola). L'handler del ricevente non scrive nulla: consegna il proprio `ResponseWriter` al mittente attraverso il canale `attached` e attende `Finished()`. Se il client del ricevente se ne va, registra `ErrReceiverGone` con `Abort` e continua ad attendere `Finished()`: il `ResponseWriter` resta valido finché il mittente lo usa.
3. **Relay**: la goroutine del mittente scrive header e body nella risposta del ricevente (`responseReceiver`), con flush dopo ogni scrittura. Con lunghezza nota la risposta ha `Content-Length`; altrimenti è chunked.
4. **Fine**: il mittente chiama sempre `Registry.Finish` (defer), che rimuove la sessione, lascia una tombstone (410 per 24 ore, al massimo 100000) e chiude `Finished()`. Il ricevente, sbloccato, termina la risposta; se la sessione ha un errore e la risposta è iniziata, interrompe la connessione senza chunk finale (`panic(http.ErrAbortHandler)`), così il client vede un errore invece di un file troncato con esito positivo.

## Concorrenza

- **Una goroutine scrive al ricevente**: quella del mittente. L'handler del ricevente tocca il `ResponseWriter` solo dopo `Finished()`, la cui chiusura dà l'ordinamento happens-before (anche per il campo `started`).
- **Stato della sessione** (`claimed`, `err`, mappa delle sessioni, tombstone) protetto da `Registry.mu`. `Slug`, `ID`, `Name`, `Length`, `Created` sono immutabili.
- **Canali**: `attached` (buffer 1, un solo invio sotto lock in `Claim`), `expired` (chiuso una volta sotto lock), `finished` (chiuso una volta con `sync.Once`), `aborted` (chiuso una volta sotto lock alla prima `Abort`). Nessun invio bloccante sotto lock.
- **Interruzione del mittente**: durante la copia una goroutine attende `Aborted()`; se il ricevente cade, chiama `IdleReader.Stop`, che imposta una scadenza di lettura passata sulla connessione del mittente e sblocca subito la `Read` in corso. Per questo la risposta al mittente ha `Connection: close`.
- **Scadenze**: lettura del mittente rinnovata a ogni `Read` (`BASHT_IDLE_TIMEOUT`), scrittura al ricevente rinnovata a ogni `Write` (stesso valore) e rimossa a fine risposta, perché la connessione del ricevente può essere riusata (keep-alive).
- **Arresto**: `Shutdown` chiude il listener e chiama `Registry.ExpireWaiting` (i mittenti in attesa terminano con `expired`); i trasferimenti in corso hanno 10 secondi, poi `Close`. `stop_grace_period` di compose (15 s) resta sopra.

## Codici HTTP

| Richiesta | Stato | Quando |
|---|---|---|
| `GET /` | 200 | testo d'uso (anche healthcheck) |
| `PUT /[nome]` | 200 | comandi; il body della risposta resta aperto fino alla fine del relay |
| `PUT` | 401 | auth configurata e credenziali assenti o errate |
| `PUT` | 503 | `BASHT_MAX_SESSIONS` raggiunto |
| `GET /`, `PUT` | 400 | `Host` vuoto o con caratteri che la shell interpreta (solo senza `BASHT_PUBLIC_URL`) |
| `GET /<slug>` | 200 | download (header `Content-Disposition`, `Accept-Ranges: none`, `Cache-Control: no-store`) |
| `GET /<slug>` | 400 | `Range` che non parte dal primo byte; lo slug non viene consumato |
| `GET /<slug>` | 404 | slug sconosciuto o sintassi non valida |
| `GET /<slug>` | 410 | slug già reclamato, concluso o scaduto (per 24 ore) |
| `GET /<slug>` | 503 | trasferimento interrotto prima di iniziare la risposta |
| altri metodi | 405 | con `Allow: GET, PUT` |
| path con più segmenti | 404 | |

## Scelte di progetto

- **Go, net/http, nessuna dipendenza esterna.** Il relay copia con `io.CopyBuffer` e un buffer da 256 KiB da `sync.Pool`; misurato alla pari con una baseline a due hop (README, "Prestazioni"). Il limite reale è TCP: vedi README, "Prestazioni di rete".
- **Flush a ogni scrittura**: lo stream deve arrivare al ricevente mentre il mittente invia (scenario `tar | curl`), senza attendere che un buffer si riempia. Ogni scrittura è grande quanto la lettura dal mittente (fino al buffer di copia), quindi il costo per byte resta basso.
- **Un solo ricevente, nessun Range.** Il body del mittente si legge una volta sola. Il download parallelo a segmenti (aria2 `-x N`) richiederebbe disco o RAM non limitata e non aumenterebbe la velocità, limitata dall'upload del mittente. Una ripresa riceve `400` (non `416`, che `curl -C -` interpreta come file già completo) e non consuma lo slug.
- **Slug come segreto**: 10 caratteri, ~59 bit; i log usano un ID separato.
- **Nome del file**: preso dal path, ripulito per finire tra virgolette doppie in una riga di shell (README, "Limiti del nome").
- **Rilevamento del mittente in attesa**: net/http non vede la chiusura del client finché il body non è letto; `ppoll` con `POLLRDHUP` a timeout zero la vede subito, anche con dati non letti. Fuori da Linux resta solo `BASHT_WAIT_TIMEOUT`.
- **Basic auth solo sul `PUT`**: confronto a tempo costante sui digest SHA-256 di utente e password.
