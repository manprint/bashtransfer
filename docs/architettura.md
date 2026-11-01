# Architettura

bashtransfer è un relay HTTP/1.1: il body di un `PUT` (mittente) viene copiato nella risposta di un `GET` (ricevente) mentre arriva, senza disco e senza buffer oltre quello di copia. Questo documento descrive come funziona il codice; uso, configurazione e log sono nel [README](../README.md), il deploy in [operazioni.md](operazioni.md).

## Componenti

```
client ──TLS──▶ nginx (stream, :443) ──TCP──▶ app (:8080, Go net/http)
                                               ├─ server.handler   upload / download / usage / reserve / cancel
                                               ├─ server.pages     pagine HTML (home, download, messaggi)
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
| `internal/server` | `handler.go` | routing, upload, download, gestione di `Range`, prenotazione e annullamento degli invii dal browser, log |
| `internal/server` | `pages.go`, `pages/*.html` | pagine per i browser (home con l'invio, pagina di download, messaggi di errore), incorporate con `embed`, CSP senza risorse esterne |
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

1. **Upload** (`handler.upload`): autorizzazione, `EnableFullDuplex` (risposta prima della lettura del body), `Registry.Create` (slug unico, timer di attesa), `100 Continue` se richiesto, la tabella dei comandi (curl, wget, aria2c, direct), flush. Poi l'handler attende in `select`:
   - `Attached()`: un ricevente ha reclamato lo slug, inizia la copia;
   - `Expired()`: timer di attesa scaduto o arresto del server;
   - `r.Context().Done()`: connessione chiusa da net/http;
   - ticker del controllo del mittente (solo Linux): `peerClosed` vede FIN o RST anche con byte del body non letti nel buffer del kernel.
2. **Download** (`handler.download`): slug valido, nessun `Range` da un byte > 0, `Registry.Claim` (riesce una volta sola). L'handler del ricevente non scrive nulla: consegna il proprio `ResponseWriter` al mittente attraverso il canale `attached` e attende `Finished()`. Se il client del ricevente se ne va, registra `ErrReceiverGone` con `Abort` e continua ad attendere `Finished()`: il `ResponseWriter` resta valido finché il mittente lo usa.
3. **Relay**: la goroutine del mittente scrive header e body nella risposta del ricevente (`responseReceiver`), con flush dopo ogni scrittura. Con lunghezza nota la risposta ha `Content-Length`; altrimenti è chunked.
4. **Fine**: il mittente chiama sempre `Registry.Finish` (defer), che rimuove la sessione, lascia una tombstone (410 per 24 ore, al massimo 100000) e chiude `Finished()`. Il ricevente, sbloccato, termina la risposta; se la sessione ha un errore e la risposta è iniziata, interrompe la connessione senza chunk finale (`panic(http.ErrAbortHandler)`), così il client vede un errore invece di un file troncato con esito positivo. Il mittente riceve in fondo alla propria risposta la riga `OK` (tutti i byte scritti verso il ricevente) o `ERRORE` (byte consegnati e motivo, oppure scadenza senza ricevente). In caso di errore, su HTTP/1 la risposta al mittente si chiude in scrittura, attende `abortLinger` (500 ms, perché curl legga la riga) e poi viene interrotta, così curl esce con codice 18; su HTTP/2 lo stream termina regolarmente (un reset farebbe aggiungere ad Apache una sua pagina 502).

### Invio dal browser

La pagina non può usare la risposta del `PUT` come curl: su HTTP/1 il browser la legge solo dopo aver inviato tutto il body, e la risposta di un trasferimento interrotto si perde nel reset della connessione. L'invio usa quindi due richieste:

1. **Prenotazione** (`handler.reserve`, `POST /` con JSON `{name, size}`): `Registry.Reserve` crea una sessione con un token (128 bit) e un timer di aggancio di 30 secondi, senza mittente. La risposta è uno stream NDJSON che resta aperto: prima riga link, token e comandi; poi `waiting`, `attached`, `progress` (byte consegnati, `Session.Delivered`, ogni 500 ms) e l'esito (`Session.Result`). Lo stream scrive almeno una riga al secondo: è anche il modo in cui il server si accorge che la pagina è chiusa (dietro Apache, solo dopo alcune scritture fallite).
2. **Body** (`handler.upload` con `X-Bashtransfer-Token`): `Registry.Bind` lega il `PUT` alla prenotazione (token a tempo costante, una volta sola) e da lì il flusso è quello di curl. La lunghezza deve essere quella prenotata: il ricevente la vede già in `Content-Length`.

La sessione termina per esito del relay, `DELETE /<slug>` con il token (Cancel o chiusura vera della pagina), scadenza dell'attesa o mancato `PUT` entro 30 secondi. Con `BASHT_SENDER_GRACE` (default 120 secondi), un mittente assente prima del relay conserva la sessione per un riaggancio: si conservano solo metadati, mai il file.

La UI negozia `foreground` nella prenotazione e invia piccole conferme di presenza ogni cinque secondi (`POST /<slug>` autenticato dal token). Passare in secondo piano, o non confermare la presenza per 15 secondi, stacca il body in attesa e avvia la tolleranza. Lo stream di avanzamento è indipendente: perderlo da solo non interrompe un upload sano. I client senza presenza mantengono il comportamento precedente dello stream.

Al ritorno in primo piano `Registry.Resume` con `rebind` stacca il vecchio body prima del relay; `BeginRelay` rende atomici l'inizio della copia e la sostituzione del mittente. Se il relay è già iniziato, lo stream ripreso dichiara `started` e non viene reinviato il file. Non è prevista ripresa dei byte dopo una caduta del body.

Il ricevente legge `GET /<slug>?status=1` senza reclamare la sessione. Il browser abilita Download solo con il mittente disponibile; `ClaimReady` verifica nuovamente la disponibilità sotto lock, lasciando intatto il link in caso di assenza. I riceventi da terminale continuano a poter reclamare una sessione e attendere il mittente.

Il link aperto in un browser (`Accept: text/html`) o da un programma di anteprima (User-Agent di chat, posta, Office) dà la pagina di download (`handler.landing`, `Registry.Peek`: non reclama lo slug). Il pulsante della pagina fa un `POST /<slug>`, gestito come il `GET` di curl.

## Concorrenza

- **Una goroutine scrive al ricevente**: quella del mittente. L'handler del ricevente tocca il `ResponseWriter` solo dopo `Finished()`, la cui chiusura dà l'ordinamento happens-before (anche per il campo `started`).
- **Stato della sessione** (`claimed`, `err`, mappa delle sessioni, tombstone) protetto da `Registry.mu`. `Slug`, `ID`, `Name`, `Length`, `Created` sono immutabili.
- **Canali**: `attached` (buffer 1, un solo invio sotto lock in `Claim`), `expired` (chiuso una volta sotto lock), `finished` (chiuso una volta con `sync.Once`), `aborted` (chiuso una volta sotto lock alla prima `Abort`). Nessun invio bloccante sotto lock.
- **Interruzione del relay**: durante la copia una goroutine attende `Aborted()`; alla disconnessione o a Cancel, imposta scadenze passate sia sulla lettura dal mittente (`IdleReader.Stop`) sia sulla scrittura al ricevente. Anche un ricevente connesso che non legge viene interrotto subito, senza attendere il timeout di inattività. Per questo la risposta al mittente ha `Connection: close`.
- **Scadenze**: lettura del mittente rinnovata a ogni `Read` (`BASHT_IDLE_TIMEOUT`), scrittura al ricevente rinnovata a ogni `Write` (stesso valore) e rimossa a fine risposta, perché la connessione del ricevente può essere riusata (keep-alive). I timer di aggancio e tolleranza sono associati a una generazione: un callback già in coda non può terminare un aggancio successivo.
- **Errori di flush**: anche un errore nell'ultimo flush verso il ricevente viene propagato come `receiver_gone`, anziché dichiarare `OK` solo perché `Write` aveva accettato i byte nel buffer.
- **Arresto**: `Shutdown` chiude il listener e chiama `Registry.ExpireWaiting` (i mittenti in attesa terminano con `expired`); i trasferimenti in corso hanno 10 secondi, poi `Close`. `stop_grace_period` di compose (15 s) resta sopra.

## Codici HTTP

| Richiesta | Stato | Quando |
|---|---|---|
| `GET /` | 200 | testo d'uso (anche healthcheck); ai browser la pagina di cortesia |
| `POST /` | 200 | prenotazione di un invio dal browser: stream NDJSON fino all'esito |
| `POST /` | 400, 401, 415, 503 | JSON non valido o senza `size`; auth; `Content-Type` non JSON; capacità |
| `PUT /[nome]` | 200 | comandi; il body della risposta resta aperto fino alla fine del relay |
| `PUT` | 401 | auth configurata e credenziali assenti o errate |
| `PUT` | 503 | `BASHT_MAX_SESSIONS` raggiunto |
| `GET /`, `PUT` | 400 | `Host` vuoto o con caratteri che la shell interpreta (solo senza `BASHT_PUBLIC_URL`) |
| `GET /<slug>` | 200 | download (header `Content-Disposition`, `Accept-Ranges: none`, `Cache-Control: no-store`); ai browser e alle anteprime la pagina di download, che non consuma lo slug |
| `POST /<slug>` | come `GET` | download dal pulsante della pagina |
| `PUT /<slug>` + token | 200, 400, 404 | body dell'invio dal browser; lunghezza diversa dalla prenotazione; token errato o prenotazione scaduta |
| `DELETE /<slug>` + token | 204, 404 | annulla l'invio dal browser; token errato o sessione conclusa |
| `GET /<slug>` | 400 | `Range` che non parte dal primo byte; lo slug non viene consumato |
| `GET /<slug>` | 404 | slug sconosciuto o sintassi non valida (ai browser con una pagina) |
| `GET /<slug>` | 410 | slug già reclamato, concluso o scaduto (per 24 ore; ai browser con una pagina) |
| `GET /<slug>` | 503 | trasferimento interrotto prima di iniziare la risposta |
| altri metodi | 405 | con `Allow: GET, POST, PUT` (anche `HEAD`, e `DELETE` senza token) |
| path con più segmenti | 404 | |

## Scelte di progetto

- **Go, net/http, nessuna dipendenza esterna.** Il relay copia con `io.CopyBuffer` e un buffer da 256 KiB da `sync.Pool`; misurato alla pari con una baseline a due hop (README, "Prestazioni"). Il limite reale è TCP: vedi README, "Prestazioni di rete".
- **Flush a ogni scrittura**: lo stream deve arrivare al ricevente mentre il mittente invia (scenario `tar | curl`), senza attendere che un buffer si riempia. Ogni scrittura è grande quanto la lettura dal mittente (fino al buffer di copia), quindi il costo per byte resta basso.
- **Un solo ricevente, nessun Range.** Il body del mittente si legge una volta sola. Il download parallelo a segmenti (aria2 `-x N`) richiederebbe disco o RAM non limitata e non aumenterebbe la velocità, limitata dall'upload del mittente. Una ripresa riceve `400` (non `416`, che `curl -C -` interpreta come file già completo) e non consuma lo slug.
- **Slug come segreto**: 10 caratteri, ~59 bit; i log usano un ID separato.
- **Nome del file**: preso dal path e ripulito (README, "Limiti del nome"). Arriva al ricevente solo in `Content-Disposition`: le righe stampate non contengono il nome, così nessuna indica un file da scrivere.
- **La sorgente non viene mai alterata**: le tre righe stampate non sovrascrivono un file esistente (`curl -OJ` crea con `O_EXCL`, `wget --content-disposition` e `aria2c --allow-overwrite=false` rinominano). Ogni risposta che non è un download ha `Content-Disposition: inline; filename="bashtransfer.txt"`, perché un `curl -OJ` su un errore o sulla tabella non ripieghi sul nome nell'URL, che per il mittente è la sorgente. `scripts/invariant.py` verifica l'invariante su tutte le varianti dei comandi (README, "La sorgente non viene mai alterata").
- **Rilevamento del mittente in attesa**: net/http non vede la chiusura del client finché il body non è letto; `ppoll` con `POLLRDHUP` a timeout zero la vede subito, anche con dati non letti. Fuori da Linux resta solo `BASHT_WAIT_TIMEOUT`.
- **Basic auth solo sull'invio** (`PUT` di curl e `POST /` del browser): confronto a tempo costante sui digest SHA-256 di utente e password. Il `PUT` e il `DELETE` del browser sono autorizzati dal token della prenotazione.
- **Invio dal browser senza CORS**: `POST /` accetta solo `application/json`, `PUT` e `DELETE` richiedono `X-Bashtransfer-Token`; un altro sito non può inviarli senza un preflight, che il server non autorizza. Il token viaggia in un header, non nella query registrata dai proxy.
- **Pagina di download prima del file**: i link incollati in chat e posta vengono aperti dalle anteprime; con un `GET` che scarica, l'anteprima consumerebbe il link prima del ricevente.
