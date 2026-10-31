# bashtransfer

Relay di file: il mittente invia un file con `curl -T`, il server risponde subito con il comando di download, e il ricevente lo esegue. Il file passa in streaming dal mittente al ricevente e non viene mai salvato su disco.

Altra documentazione:

- [docs/architettura.md](docs/architettura.md): flusso di una sessione, stati, concorrenza, codici HTTP, scelte di progetto;
- [docs/operazioni.md](docs/operazioni.md): deploy sulla VM, tuning TCP, log e monitoraggio, aggiornamenti, risoluzione dei problemi.

## Uso

### Scenario 1: file

Mittente:

```bash
curl -T myfile.zip https://bashtransfer.mydomain.it
```

Risposta immediata: una tabella ASCII con le righe di download, una per strumento:

```
+-----------------------------------------------------------------------------------------------+
| bashtransfer: comandi per il ricevente                                                        |
+--------+--------------------------------------------------------------------------------------+
| curl   | curl -f -OJ "https://bashtransfer.mydomain.it/X9ZnTRvc5H"                            |
| wget   | wget --content-disposition "https://bashtransfer.mydomain.it/X9ZnTRvc5H"             |
| aria2c | aria2c -x1 -s1 --allow-overwrite=false "https://bashtransfer.mydomain.it/X9ZnTRvc5H" |
| direct | https://bashtransfer.mydomain.it/X9ZnTRvc5H                                          |
+--------+--------------------------------------------------------------------------------------+
| Un solo ricevente per link. Lascia aperto il terminale fino alla fine.                        |
+-----------------------------------------------------------------------------------------------+
```

La colonna dei comandi è larga quanto il comando più lungo. Il triplo clic su una riga seleziona anche i bordi `|`, che la shell non accetta: per copiare un comando selezionalo dall'inizio alla virgoletta finale, oppure usa uno dei modi in [Copiare il comando senza selezionarlo](#copiare-il-comando-senza-selezionarlo).

Ricevente: esegue una delle tre righe di comando, oppure apre nel browser il link della riga `direct` (vedi [Download dal browser](#download-dal-browser)). Il file viene scaricato mentre il mittente lo invia e prende il nome originale (`myfile.zip`), che arriva dall'header `Content-Disposition`: le righe non contengono il nome, quindi nessuna di esse può indicare un file da scrivere.

Quando il ricevente ha finito, il mittente legge l'esito in fondo alla tabella (vedi [Esito per il mittente](#esito-per-il-mittente)).

L'header `Content-Disposition` contiene il nome sia in `filename*` (UTF-8 codificato, per i browser) sia in `filename` con i byte UTF-8 (per `curl -OJ`, che legge solo `filename`): i nomi con lettere accentate restano corretti.

#### La sorgente non viene mai alterata

Il link viene mandato a persone che non conoscono lo strumento e che possono sbagliare comando o dimenticare un flag. Cosa garantisce il sistema, senza contare su istruzioni:

- **ricevente**: la riga stampata, con qualunque flag omesso o tra quelli comuni aggiunto e con file di configurazione ostili, non modifica il file o la cartella in invio, su link valido, usato, scaduto o sconosciuto;
- **mittente**: ogni forma di `curl -T` senza `-O`/`-o` (file, pipe, stdin, cartelle con `tar`) non modifica la sorgente, qualunque sia l'esito; con `-OJ` la sorgente resta intatta sulle risposte `2xx`/`3xx` (vedi sotto per gli errori).

Come:

- un ricevente su un altro computer non può toccarla: il server non scrive mai sul disco del mittente e `curl -T` apre la sorgente in sola lettura;
- il rischio esiste solo quando il ricevente lavora nella stessa cartella del mittente (stessa macchina o cartella condivisa). Lì nessuna delle righe sovrascrive un file esistente:
  - `curl -f -OJ` crea il file con `O_EXCL`: se esiste esce con codice 23 senza toccarlo. Con `-f` un errore HTTP esce con codice 22 senza scrivere nulla;
  - `wget --content-disposition` salva `myfile.zip.1`; busybox wget non conosce l'opzione ed esce senza scrivere;
  - `aria2c --allow-overwrite=false` salva `myfile.1.zip`, anche con `allow-overwrite=true` nel suo `aria2.conf`;
  - il browser (riga `direct`) non sovrascrive un file esistente: salva con un altro nome (`myfile (1).zip`). È il comportamento predefinito dei browser, non verificato dai test;
- l'invio dal browser legge il file con le API del browser, che non danno accesso in scrittura al file scelto;
- ogni risposta che non è un download (errori `400`/`404`/`410`, la risposta al mittente) ha un nome proprio, `bashtransfer.txt`: un `curl -OJ` che riceve un errore o la tabella non ripiega sul nome nell'URL.

[`scripts/invariant.py`](scripts/invariant.py), eseguito dall'e2e e dalla CI, lo verifica a ogni modifica. Prova come sorgente un file con nome accentato, un file chiamato `bashtransfer.txt`, una cartella e un link simbolico, e confronta prima e dopo contenuto, dimensione, date e inode.

- Ricevente: la riga stampata e ogni sua variante con flag omessi o aggiunti (`curl` senza `-f`/`-O`/`-J`, `-C -`, `-L`; `wget` senza `--content-disposition`, `-c`, `-N`, `-nc`; busybox `wget`; `aria2c` senza `-x1`/`-s1`/`--allow-overwrite=false`, `-c`, `-x4 -s4`), con configurazione vuota e con `.curlrc`, `.wgetrc` e `aria2.conf` ostili, su link valido, sconosciuto, già usato, scaduto e con mittente caduto.
- Mittente: le forme comuni di `curl -T` (con e senza nome nell'URL, `/` finale, `-N`, `-f`, `-J`, `-OJ`, `-L`, da pipe e da stdin, `tar | curl` per le cartelle) con download riuscito, ricevente caduto, link scaduto, Ctrl+C, `401` e `503`.

Restano scoperti, e vengono eseguiti dal test senza pretenderne l'esito:

- il ricevente che indica il nome della sorgente come uscita (`curl -o myfile.zip`, `wget -O myfile.zip`, `> myfile.zip`) o chiede di sovrascrivere (`curl --clobber` o `clobber` nel `.curlrc`, `aria2c --allow-overwrite=true`, oppure `allow-overwrite=true` nel suo `aria2.conf` con la riga stampata privata di `--allow-overwrite=false`), o estrae un archivio sopra la cartella in invio;
- il mittente che aggiunge `-O` o `-o` a `curl -T`: curl scrive la risposta nel file con il nome dell'URL, cioè la sorgente (lo crea vuoto anche se la risposta non ha body). La risposta deve contenere i comandi per il ricevente, quindi il server non può impedirlo. Con `-OJ` la sorgente è protetta dalle risposte `2xx`/`3xx`; curl recenti (8.18) ignorano `Content-Disposition` sulle risposte `4xx`/`5xx` e troncano la sorgente anche con una risposta senza body, quindi un `401` (credenziali errate), un `503` (server pieno) o un `503` di Apache durante un riavvio del container la troncano. curl 8.5 usa l'header anche sugli errori e la lascia intatta. `-O` e `-OJ` servono solo per scaricare: nell'invio non vanno usati.

#### Copiare il comando senza selezionarlo

Su Linux il doppio clic sull'URL lo seleziona tutto e il clic centrale lo incolla subito (selezione primaria, senza Ctrl+C né menu). Per un ricevente con curl basta l'URL: `curl -f -OJ URL`.

Per avere il comando curl già negli appunti, una funzione da mettere nel `~/.bashrc` del mittente (`wl-copy` su Wayland, `xclip -selection clipboard` su X11):

```bash
bt() {
  curl -N -T "$1" "https://bashtransfer.mydomain.it/${2:-}" | tee /dev/tty \
    | sed -nu 's/^| curl *| \(.*[^ ]\) *|$/\1/p' | head -n 1 | wl-copy
}
```

`bt myfile.zip` stampa la tabella e copia la riga curl negli appunti appena arriva; `bt - mydir.tgz` legge da stdin. `curl -N` e `sed -u` servono perché la riga arrivi subito, non a fine trasferimento.

Il nome del file è preso dal path dell'upload. Con `curl -T myfile.zip https://host` curl invia il nome locale come path e il file del ricevente si chiama `myfile.zip` (verificato). Il nome `download` si usa solo quando il mittente invia da stdin (`-T -`) senza nome nel path.

### Scenario 2: stream da comando

Mittente, senza file su disco:

```bash
sudo tar -cpzf - mydir | curl -N -T - https://bashtransfer.mydomain.it/mydir.tgz
```

Il ricevente riceve `mydir.tgz`. `curl -N` disattiva il buffering dell'output: serve per vedere la riga di comando subito durante l'upload di un pipe. Con un file (scenario 1) non serve.

Senza nome nel path (`... | curl -T - https://bashtransfer.mydomain.it`) il ricevente riceve un file chiamato `download`.

### Esito per il mittente

Quando il trasferimento finisce, il server scrive al mittente, sotto la tabella, una riga con l'esito. Vale per i file e per gli stream da pipe (di cui non conosce la lunghezza):

```
OK: download completato, 104857600 byte (100.0 MiB) consegnati al ricevente in 3.2s.
ERRORE: download NON completato dopo 52428800 di 104857600 byte: <motivo>.
ERRORE: download NON avvenuto: nessun ricevente si è collegato in 1h0m0s, il link è stato chiuso.
```

- `ERRORE` è affidabile: il ricevente non ha il file intero (connessione caduta, comando interrotto, file non salvato), e il mittente deve reinviare.
- `OK` significa che tutti i byte sono stati consegnati alla connessione del ricevente. Per file molto piccoli (pochi KB) curl del ricevente può avere già letto tutto quando rifiuta di salvare (file omonimo presente, disco pieno): in quel caso l'errore compare solo sul terminale del ricevente.
- Con connessione diretta curl del mittente esce con codice 18 in caso di errore; dietro Apache (HTTP/2 verso il backend) esce con 0. Conta la riga `OK`/`ERRORE`.

### Pagina di benvenuto

Un `GET /` da browser (header `Accept` con `text/html`) mostra la pagina di cortesia, in inglese:

- **Sender, Send a file**: l'invio dal browser (vedi [Invio dal browser](#invio-dal-browser));
- **Sender, Send from a terminal**: `curl URL -T FILE` e la variante da pipe per le cartelle, con il pulsante Copy, e cosa aspettarsi (tabella, terminale aperto, riga `OK`/`ERRORE`);
- **Receiver, Receive a file**: il link nel browser (riga `direct`) oppure i comandi curl, wget e aria2c;
- **Install the tools**: i comandi per installare curl, wget e aria2c (Debian/Ubuntu, Fedora/RHEL, Arch, openSUSE, Alpine, macOS, FreeBSD, Windows);
- **Privacy and security**: nulla viene salvato; link casuali e monouso; con `BASHT_PUBLIC_URL` in `https://`, cifratura HTTPS in transito ma non end-to-end, con il consiglio di cifrare i file riservati prima di inviarli (vedi [Limiti e sicurezza](#limiti-e-sicurezza)).

Le pagine sono incorporate nel binario (`internal/server/pages/`), non caricano risorse esterne (CSP restrittiva, `frame-ancestors 'none'`), seguono il tema chiaro o scuro del sistema e usano come base dei comandi `BASHT_PUBLIC_URL`. Gli altri client (curl, wget, aria2c) ricevono lo stesso `GET /` in testo semplice, con le istruzioni.

### Download dal browser

Il link della riga `direct`, aperto in un browser, mostra una pagina con nome e dimensione del file, il pulsante **Download** e i comandi per il terminale. Il download parte solo dal pulsante (un `POST` sulla stessa URL):

- aprire o ricaricare la pagina non consuma il link; neanche le anteprime di chat, social, posta e Office (User-Agent con `bot`, `preview`, `slack`, `whatsapp`, `telegram`, `discord`, `outlook` e simili, anche senza `Accept: text/html`);
- un link già usato, scaduto o sconosciuto mostra una pagina con il motivo (`410` o `404`); un link il cui download è già in corso lo dice ("Download already started");
- dopo il clic il pulsante diventa "Download started" e non può partire un secondo download;
- per un invio da pipe la dimensione è "unknown (streamed by the sender)".

curl, wget e aria2c, che non chiedono `text/html`, ricevono il file direttamente con il `GET`. Un programma sconosciuto che apre i link con `Accept: */*` (per esempio uno scanner di sicurezza della posta che non si dichiara) consuma il link come un download.

### Invio dal browser

Nella pagina di cortesia, sotto il titolo, un riquadro accetta un file trascinato o scelto con il pulsante **Upload** (uno solo: più file trascinati insieme vengono rifiutati). Il browser:

1. chiede il link (`POST /` con `{"name": ..., "size": ...}` in JSON) e mostra subito la tabella per il ricevente, con le righe curl, wget, aria2c e direct e i pulsanti Copy: la stessa del terminale;
2. invia il file (`PUT /<slug>`) solo quando il ricevente si collega, come `curl -T`;
3. mostra la barra di avanzamento con percentuale, velocità, byte consegnati e tempo stimato, e alla fine l'esito: "Done: the receiver got the whole file", oppure il motivo (link scaduto, ricevente disconnesso, invio interrotto).

La pagina va tenuta aperta fino alla fine: chiudendola, il browser chiede conferma. **Cancel** e la chiusura della pagina chiudono subito il link: un ricevente che lo apre dopo vede "Sender stopped" (410), uno che sta scaricando viene interrotto. Se invece cade la rete del mittente, il link si chiude quando il server se ne accorge (vedi sotto e [Limiti e sicurezza](#limiti-e-sicurezza)).

**Stesso telefono: tolleranza del mittente (`BASHT_SENDER_GRACE`).** Su Android, quando il mittente passa a un'altra app per aprire il link (per esempio da Chrome a Brave), il sistema sospende la rete di Chrome dopo pochi secondi: la connessione cade prima del ricevente e il link si chiude. Con `BASHT_SENDER_GRACE=120` (secondi) un mittente dal browser caduto prima che un ricevente inizi il download non chiude più il link: la prenotazione resta viva per 120 secondi. Tornando alla pagina, questa riapre lo stream e ripete il `PUT` con lo stesso token, e il trasferimento riparte. Il ricevente che nel frattempo preme **Download** in un browser riceve subito l'header del file (il browser lo tratta come download in corso, anche con l'app in secondo piano) e i dati partono quando il mittente è tornato; un ricevente da terminale resta in attesa. Dietro Apache (`mod_proxy_http2`) l'header anticipato non è arrivato al browser prima dei primi dati nei test (Playwright, `scripts/ui`): lì il browser mostra la richiesta in attesa finché il mittente non torna, e il download parte allora. Se il mittente non torna entro il tempo impostato, il link si chiude come per un mittente caduto ("Sender stopped"). Variabile vuota o `0`: nessuna tolleranza, comportamento di sempre. **Non funziona** se il sistema termina il processo del browser (il file scelto non esiste più) o se il mittente non torna in primo piano. Cancel e la chiusura della pagina chiudono subito il link anche con la tolleranza. Una caduta durante il download non è recuperabile: il trasferimento è interrotto come sempre.

Con `BASHT_AUTH_USER` impostato il browser chiede utente e password con la sua finestra di login; se l'utente annulla, la pagina mostra "This server requires a login to send files.".

Protocollo (per chi scrive un client):

- `POST /`, `Content-Type: application/json`, body `{"name":"file.zip","size":1234}` (al massimo 4096 byte; `size` obbligatorio e non negativo): richiede Basic auth come il `PUT` del terminale. Risponde `200` con uno stream NDJSON (`application/x-ndjson`), una riga JSON per evento, aperto per tutto il trasferimento:
  - la prenotazione: `{"slug","url","token","commands":[{"label","line"}...],"grace":S}` (`grace`, in secondi, c'è solo con `BASHT_SENDER_GRACE`);
  - `{"event":"waiting"}` ogni secondo finché nessun ricevente si collega;
  - `{"event":"attached"}` quando il ricevente si collega;
  - `{"event":"progress","bytes":N}` con i byte consegnati al ricevente, ogni 500 ms se cambiano e comunque ogni secondo;
  - l'esito, ultima riga: `{"outcome":"ok"|"expired"|"receiver_gone"|"idle"|"length_mismatch"|"sender_gone","bytes":N,"seconds":S}`.
- `PUT /<slug>` con l'header `X-Bashtransfer-Token: <token>` e il body di esattamente `size` byte: va iniziato entro 30 secondi dalla prenotazione. Il token sostituisce la Basic auth. Risponde con lo stesso esito in JSON (su HTTP/1 il browser lo legge solo se ha inviato tutto il body: l'esito affidabile è quello dello stream). Token errato: `404`; lunghezza diversa: `400`.
- Con `BASHT_SENDER_GRACE`: `POST /` con `{"resume":"<slug>"}` e l'header `X-Bashtransfer-Token` riapre lo stream di una prenotazione viva, con la prenotazione come prima riga (`404` se lo slug è chiuso, il token è errato o la tolleranza è disattivata). La caduta dell'ultimo stream, o del `PUT`, prima dell'inizio del relay mette il mittente in tolleranza invece di chiudere il link; un nuovo `PUT` con lo stesso token riaggancia la sessione, anche se il server non ha ancora visto cadere il vecchio (che viene sostituito), purché il relay non sia iniziato.
- `DELETE /<slug>` con lo stesso header: chiude il link e interrompe il trasferimento (`204`; `404` se il token è errato o la sessione è già conclusa). È quello che fa la pagina con Cancel o alla chiusura (`fetch` con `keepalive`).
- La chiusura dello stream NDJSON chiude il link (senza `BASHT_SENDER_GRACE`; con la tolleranza mette il mittente in attesa di un riaggancio, vedi sopra). Se il `DELETE` non parte ma il browser chiude la connessione (processo terminato, scheda chiusa senza `pagehide`), il server se ne accorge alla prima scrittura fallita: subito con connessione diretta, in circa 3,5 secondi dietro Apache (misurato con Apache 2.4.69: Apache segnala la chiusura del client al backend solo dopo alcune scritture fallite, per questo lo stream scrive almeno una riga al secondo). Se la rete del mittente cade senza chiudere la connessione (nessun FIN o RST), le righe dello stream restano nei buffer TCP: il link si chiude alla scadenza della ritrasmissione TCP, a `BASHT_WAIT_TIMEOUT` o, durante il trasferimento, a `BASHT_IDLE_TIMEOUT`, come per un mittente curl.

Il token è casuale (128 bit), confrontato in tempo costante, e viaggia in un header e non nella query, che i proxy registrano nei log. `POST /` richiede JSON e `PUT`/`DELETE` del browser un header personalizzato: un altro sito non può inviarli senza un preflight CORS, che il server non autorizza.

### Limiti del nome

Il nome viene ripulito: si tiene solo l'ultimo segmento del path, si tolgono caratteri di controllo (anche C1), virgolette, backslash, separatori di riga e controlli bidirezionali Unicode, si tolgono i punti iniziali (`.bashrc` diventa `bashrc`: il ricevente esegue il comando nella sua directory e non deve sovrascrivere file nascosti), si limita a 128 caratteri. Un `/` codificato (`%2F`) conta come separatore: resta l'ultima parte. `$`, backtick e `!` diventano `_`: il ricevente può riusare il nome in una riga di shell. Un nome vuoto, o `-` (lo stdout per molti strumenti), diventa `download`.

### Download multipart e ripresa

Il relay è un flusso unico: il body del mittente passa una sola volta, in ordine, a un solo ricevente. Non può servire intervalli di byte, quindi:

- il download risponde con `Accept-Ranges: none`; `aria2c` usa una sola connessione (la riga stampata ha `-x1 -s1`). Più connessioni non aumenterebbero comunque la velocità: il limite è la connessione di upload del mittente;
- un `GET` con `Range` che parte da un byte diverso dal primo (ripresa con `curl -C -` o `wget -c`, connessioni aggiuntive di un download manager) riceve `400` e non consuma lo slug: il download completo resta possibile. Su uno slug sconosciuto, concluso o già reclamato la risposta è quella di un `GET` normale (`404` o `410`). `Range: bytes=0-` è servito con `200` e il body intero;
- un download interrotto non si riprende: il mittente deve inviare di nuovo.

## Configurazione

Tutte le variabili sono opzionali. Un valore vuoto equivale a non impostarlo.

| Variabile | Default | Significato |
|-----------|---------|-------------|
| `BASHT_LISTEN` | `:8080` | Indirizzo di ascolto HTTP del server. |
| `BASHT_PUBLIC_URL` | vuoto | Base degli URL nei comandi: `http(s)://host[/prefisso]`, senza query, credenziali né caratteri che la shell interpreta (validata all'avvio). Se vuoto, si usano `X-Forwarded-Proto` e `Host`; un `Host` con caratteri che la shell interpreta (`$`, `!`, spazi, virgolette, backtick, backslash) riceve `400`. Obbligatorio in compose. |
| `BASHT_AUTH_USER` | vuoto | Utente per l'upload. Va impostato insieme a `BASHT_AUTH_PASSWORD`. |
| `BASHT_AUTH_PASSWORD` | vuoto | Password per l'upload. |
| `BASHT_WAIT_TIMEOUT` | `1h` | Quanto il mittente può attendere un ricevente prima che lo slug scada. |
| `BASHT_SENDER_GRACE` | vuoto (`0`) | Secondi interi (0–86400). Se > 0, un mittente dal browser caduto prima del ricevente (tipicamente l'app in secondo piano su un telefono) mantiene il link vivo per questo tempo e può riagganciarsi (vedi "Invio dal browser"). Vuoto o `0`: il link si chiude subito, come sempre. |
| `BASHT_IDLE_TIMEOUT` | `2m` | Interruzione del relay se il mittente non invia byte per questo tempo, o se il ricevente non accetta una singola scrittura entro questo tempo (minimo `1s`). |
| `BASHT_MAX_SESSIONS` | `1000` | Numero massimo di slug attivi contemporaneamente. |
| `BASHT_BUF_SIZE` | `262144` | Dimensione del buffer di copia, tra 4096 e 4194304 byte. Il default 256 KiB è il buffer con il throughput assoluto più alto del relay in tutte e quattro le sessioni. |
| `BASHT_LOG_FORMAT` | `text` (`json` in compose) | Formato dei log su stderr: `text` (logfmt, `chiave=valore`) o `json` (un oggetto per riga). |
| `BASHT_LOG_LEVEL` | `info` | Livello minimo: `debug`, `info`, `warn`, `error`. `debug` aggiunge gli eventi intermedi di ogni sessione e i rifiuti `404`. |

Un valore non valido ferma l'avvio con un messaggio che nomina la variabile.

## Autenticazione

Se `BASHT_AUTH_USER` e `BASHT_AUTH_PASSWORD` sono impostate, ogni `PUT` richiede Basic auth; senza credenziali o con credenziali errate la risposta è `401` con `WWW-Authenticate`. Per l'invio dal browser le credenziali servono alla prenotazione (`POST /`, il browser le chiede con la sua finestra di login); il `PUT` e il `DELETE` della pagina usano il token della prenotazione. Il download (`GET`, o `POST` dal pulsante della pagina di download) non richiede credenziali: chi ha lo slug può scaricare.

```bash
curl -u utente:password -T myfile.zip https://bashtransfer.mydomain.it
```

Se le variabili non sono impostate, l'upload è anonimo.

## Immagine

Il workflow `.github/workflows/image.yml` esegue `gofmt`, `go vet`, `go test -race`, `scripts/e2e.sh` (con `aria2c`) e i test frontend `scripts/ui/run.sh` (Playwright, server diretti e Apache) a ogni pull request e push, e pubblica l'immagine su GitHub Container Registry (`ghcr.io/<owner>/<repo>`, con il token del workflow, senza secret):

| Evento | Tag pubblicati |
|---|---|
| push su `main` | `main`, `sha-<commit>` |
| tag `vX.Y.Z` | `X.Y.Z`, `X.Y`, `latest` |
| pull request | nessuno (solo build) |

L'immagine è multi-architettura (`linux/amd64`, `linux/arm64`, compilazione incrociata senza emulazione), con SBOM e provenance. Il primo push crea un pacchetto privato: per scaricarlo senza login renderlo pubblico dalle impostazioni del pacchetto su GitHub, oppure eseguire `docker login ghcr.io` sulla VM con un token con permesso `read:packages`.

Rilascio:

```bash
git tag v1.0.0 && git push origin v1.0.0
```

## Deploy con Docker Compose

`compose.yaml` è lo stack di produzione: `app` (il relay, immagine pubblicata) e `nginx` (TLS, porte 80 e 443). La procedura completa per la VM è in [docs/operazioni.md](docs/operazioni.md).

1. Preparare una directory con `fullchain.pem` e `privkey.pem`. Il repository non gestisce certificati.
2. Impostare le variabili, per esempio in un file `.env` non versionato accanto a `compose.yaml`:

   ```
   BASHT_IMAGE=ghcr.io/owner/bashtransfer:1.0.0
   BASHT_CERTS_DIR=/etc/ssl/bashtransfer
   BASHT_PUBLIC_URL=https://bashtransfer.mydomain.it
   BASHT_AUTH_USER=utente
   BASHT_AUTH_PASSWORD=una-password-lunga
   ```

3. Avviare:

   ```bash
   docker compose pull
   docker compose up -d
   ```

Per costruire l'immagine dal sorgente invece di scaricarla:

```bash
BASHT_IMAGE=bashtransfer:local docker compose -f compose.yaml -f compose.build.yaml up -d --build
```

Variabili di compose (oltre alle `BASHT_*` del server): `BASHT_IMAGE` (obbligatoria), `BASHT_CERTS_DIR` (obbligatoria), `BASHT_TCP_CC` (algoritmo di congestione di nginx, default `bbr`).

Reti: `frontend` porta le porte pubblicate di nginx; `backend` è `internal`, collega nginx e `app`, e non ha uscita verso l'esterno: `app` non è raggiungibile se non da nginx e non può aprire connessioni verso Internet.

Isolamento dei container:

- `app`: filesystem in sola lettura, nessuna capability, `no-new-privileges`, utente `nonroot` (immagine distroless senza shell).
- `nginx`: filesystem in sola lettura con tmpfs per pid e file temporanei, solo le capability `CHOWN`, `SETUID`, `SETGID`, `NET_BIND_SERVICE` (il master parte come root, i worker come `nginx`), `no-new-privileges`. Senza `DAC_OVERRIDE` il master legge i certificati solo se sono di root o leggibili da tutti: `privkey.pem` di root con permessi `600` (come con certbot) va bene.
- Log con rotazione (3 file da 10 MB per container).

La configurazione nginx è in `deploy/nginx/nginx.conf` (nginx 1.30 stable, fissato per digest): worker automatici, `worker_connections 8192`, risoluzione DNS a ogni connessione (il container `app` può essere ricreato senza riavviare nginx), buffer di copia da 128k, keepalive TCP verso il client (60 s, poi 4 sonde ogni 15 s: un client sparito libera la sessione in circa 2 minuti) e un tetto di 64 connessioni simultanee per IP del client (`limit_conn`; rimuovere le due righe per disattivarlo). TLS è terminato dal blocco `stream` (proxy TCP): il relay passa byte per byte, senza il proxy HTTP di nginx, che interrompe il body di un upload quando il server ha già risposto (verificato in questo ambiente). Il blocco `http` serve solo il redirect da 80 a 443.

Perché nginx non aggiunge header HTTP, `BASHT_PUBLIC_URL` è obbligatorio in compose: i comandi stampati usano quell'URL, non `Host` o `X-Forwarded-Proto`.

Senza nginx il server parla solo HTTP semplice: non esegue TLS.

L'immagine ha un `HEALTHCHECK` (`/bashtransfer -healthcheck`: `GET /` su `BASHT_LISTEN`, solo loopback); `nginx` parte quando `app` è sano. All'arresto nginx riceve `SIGQUIT` e attende la fine dei trasferimenti in corso fino a 30 secondi (`stop_grace_period`); `app` ha 10 secondi di grazia interni (15 in compose).

### Dietro Apache (mod_proxy_http2)

Alternativa a nginx quando la 443 è già di Apache. Il server accetta HTTP/1.1 e HTTP/2 in chiaro (h2c) sulla stessa porta; Apache deve parlargli in h2c.

Il proxy HTTP/1.1 di Apache (`ProxyPass http://`) non funziona: invia tutto il body dell'upload prima di leggere la risposta, il server risponde con i comandi e legge il body solo quando arriva il ricevente, quindi `curl -T` resta fermo senza stampare nulla. Con `h2c://` richiesta e risposta scorrono insieme.

Cosa fare:

1. Avviare l'app con un'immagine che contiene il supporto h2c (successiva a `sha-66a3f84`) e con `BASHT_PUBLIC_URL` uguale all'URL pubblico: Apache non passa `Host` o `X-Forwarded-Proto` ai comandi stampati. L'app deve essere raggiungibile da Apache sulla porta `BASHT_LISTEN` (stessa rete Docker, oppure porta pubblicata su `127.0.0.1`).
2. Abilitare i moduli. `proxy_http2` usa la libreria nghttp2 (inclusa nei pacchetti Debian/Ubuntu di `apache2`). `mod_http2` (HTTP/2 verso i client) non serve: se `a2enmod` lo abilita come dipendenza, `Protocols http/1.1` nel vhost (vedi sotto) lo esclude per questo sito:

   ```bash
   a2enmod proxy proxy_http proxy_http2 headers ssl
   ```

3. Nel vhost TLS sostituire le due righe `ProxyPass` e `ProxyPassReverse` che puntavano a `http://`:

   ```apache
   <VirtualHost *:443>
       ServerName bashtransfer.mydomain.it

       Protocols http/1.1

       ProxyRequests     Off
       ProxyPreserveHost On
       ProxyPass         / h2c://bash-transfer:8080/ flushpackets=on timeout=600 connectiontimeout=5 retry=0
       ProxyPassReverse  / h2c://bash-transfer:8080/
       ProxyIOBufferSize 65536
       LimitRequestBody  0
       SetEnv no-gzip    1
       SetEnv no-brotli  1

       SSLCertificateFile    /etc/letsencrypt/live/bashtransfer.mydomain.it/fullchain.pem
       SSLCertificateKeyFile /etc/letsencrypt/live/bashtransfer.mydomain.it/privkey.pem
       Include /etc/letsencrypt/options-ssl-apache.conf
   </VirtualHost>
   ```

   `Protocols http/1.1` tiene il collegamento client-Apache su HTTP/1.1: con HTTP/2 verso il client (se `mod_http2` è caricato e `Protocols` non lo esclude) l'upload è limitato dalla finestra di flusso di Apache e, su una rete con latenza, scende di molto (misurato in locale con 40 ms di RTT simulati: circa 11 MB/s con h2, oltre 200 MB/s con HTTP/1.1; il download non cambia). Il solo `proxy_http2` verso l'app resta h2c. `bash-transfer` è il nome del container sulla rete condivisa con Apache; con l'app su `127.0.0.1` usare `h2c://127.0.0.1:8080/`.
4. Controllare e ricaricare:

   ```bash
   apache2ctl configtest && systemctl reload apache2
   ```

5. Se Apache risponde `500` e nell'`error.log` compare `AH01144: No protocol handler was valid for the URL ... (scheme 'h2c')`, `mod_proxy_http2` non è caricato. Controllare con `apache2ctl -M | grep http2`: deve comparire `proxy_http2_module`. Con Apache in un container `a2enmod` va eseguito nell'immagine (`RUN a2enmod proxy proxy_http proxy_http2 headers ssl` nel Dockerfile): un `a2enmod` lanciato nel container in esecuzione si perde quando viene ricreato.
6. Verificare: `curl -T FILE https://bashtransfer.mydomain.it` deve stampare subito la tabella con le righe `curl`, `wget`, `aria2c`, `direct`; eseguendo una di esse da un altro terminale, o aprendo il link `direct` nel browser, il file passa.

Come si comporta:

- Su HTTP/2 il server invia sempre `100 Continue`: Apache toglie `Expect` e, senza il 100, curl (che lo usa sopra circa 1 MB) scarta il body appena vede la risposta con i comandi.
- Su HTTP/2 il server non invia `Connection: close`: sarebbe tradotto in un `GOAWAY` che blocca il proxy mentre il body è in attesa.
- Mentre il mittente attende il ricevente, il server gli scrive un byte `\r` ogni 2 secondi (invisibile nel terminale: i comandi terminano con `\n`). Apache non si accorge da solo di un mittente caduto finché non rilegge il body; la scrittura di quel byte fallisce, Apache chiude lo stream e lo slug si libera entro circa 4 secondi. Un ricevente che prova prima riceve `410` o un abort, mai un file corrotto.
- Lo stesso traffico azzera il `timeout` del proxy: l'attesa del ricevente è limitata solo da `BASHT_WAIT_TIMEOUT` (verificato con `timeout=5` e un'attesa di 15 secondi). Il `timeout` conta invece durante il trasferimento: un'interruzione di rete più lunga lo chiude.
- L'app vede solo l'IP di Apache: eventuali limiti per IP vanno impostati in Apache.
- Le risposte generate da Apache (il redirect `301` del vhost :80, il `503` con l'app ferma durante un riavvio) non passano dall'app e non hanno il nome `bashtransfer.txt` (vedi [La sorgente non viene mai alterata](#la-sorgente-non-viene-mai-alterata)). Facoltativo, nei vhost :443 e :80, con il modulo `headers`:

  ```apache
  Header always set Content-Disposition "inline; filename=\"bashtransfer.txt\"" "expr=-z resp('Content-Disposition')"
  ```

  La condizione lascia intatto il nome dei download. Protegge un mittente che aggiunge per errore `-OJ` all'invio dal `301` e, con curl meno recenti, dal `503`: curl 8.18 ignora l'header sulle risposte `4xx`/`5xx` (verificato con Apache 2.4.59, curl 8.18 e 8.5).
- Invio dal browser: la pagina chiude il link con un `DELETE` esplicito (Cancel, chiusura della pagina), che passa subito. Se non parte e il browser chiude la connessione, Apache segnala al backend la chiusura dello stream NDJSON solo dopo alcune scritture fallite verso il browser: il link si chiude in circa 3,5 secondi.
- Il log di accesso `combined` di Apache registra la riga della richiesta, cioè lo slug e, per gli invii da terminale, il nome del file (`PUT /myfile.zip`): chi legge il log può scaricare un link ancora attivo. Per non registrarli, nel vhost sostituire la riga `CustomLog ${APACHE_LOG_DIR}/access.log combined` con:

  ```apache
  LogFormat "%h %t \"%m\" %>s %b %D" bashtransfer
  CustomLog ${APACHE_LOG_DIR}/access.log bashtransfer
  ```

  `%D` è la durata della richiesta in microsecondi. Il token dell'invio dal browser viaggia in un header e non compare nei log di Apache.
- Verificato con Apache 2.4.69 (immagine `httpd:2.4`) in HTTP in chiaro verso il client, comprese le pagine web e l'invio dal browser (test Playwright con Chromium); con la versione e il TLS del proprio server conviene ripetere la prova del punto 6 con un file di qualche centinaio di MB.

## Log

Una riga per evento su stderr, nel formato di `BASHT_LOG_FORMAT`. Ogni riga ha `time`, `level` e `msg` (il nome dell'evento, stabile: usarlo per filtrare). **Slug, nomi di file, path e credenziali non compaiono mai**: una sessione è identificata da `id`, 16 caratteri esadecimali casuali e indipendenti dallo slug, quindi chi legge i log non può scaricare.

| Evento (`msg`) | Livello | Quando | Campi |
|---|---|---|---|
| `server_start` | INFO | avvio | `listen`, `public_url`, `auth` (bool), `wait_timeout`, `idle_timeout`, `sender_grace`, `max_sessions`, `buf_size`, `log_level`, `go`, `gomaxprocs` |
| `server_listening` | INFO | porta aperta | `addr` |
| `upload_reserved` | DEBUG | invio dal browser prenotato (`POST /`) | `id`, `length` |
| `upload_accepted` | DEBUG | sessione creata, comandi inviati; per il browser, inizio del `PUT` | `id`, `length` (-1 se ignota) |
| `upload_cancelled` | DEBUG | invio dal browser annullato (`DELETE`) | `id` |
| `upload_resumed` | INFO | il mittente dal browser ha riaperto lo stream dopo una caduta (`BASHT_SENDER_GRACE`) | `id` |
| `sender_detached` | INFO | il mittente dal browser è caduto prima del ricevente e il link resta vivo per la tolleranza | `id`, `wait_s`, `grace_s` |
| `sender_grace_expired` | INFO | la tolleranza è scaduta senza che il mittente tornasse: il link si chiude | `id`, `wait_s` |
| `download_claimed` | DEBUG | un ricevente ha reclamato lo slug | `id` |
| `receiver_attached` | DEBUG | il relay inizia | `id`, `wait_s` |
| `transfer` | INFO, WARN se `aborted` | fine di ogni sessione, una volta | `id`, `outcome`, `bytes`, `length` (se nota), `wait_s`, `transfer_s` e `rate_mbs` (se un ricevente si è agganciato), `err` (se presente) |
| `request_rejected` | vedi sotto | richiesta rifiutata | `method`, `status`, `reason` |
| `shutdown_start` | INFO | primo SIGTERM/SIGINT | `sessions` (vive), `grace` |
| `shutdown_forced` | WARN | grazia scaduta | `err`, `sessions` |
| `shutdown_done` | INFO | uscita | `forced`, `duration_s` |
| `session_create_failed`, `claim_failed`, `full_duplex_unavailable` | ERROR | errori interni (non attesi) | `err` |
| (messaggio di net/http) | WARN | errori del server HTTP (accept, richieste malformate) | `component=net/http` |

`outcome` di `transfer`:

- `ok`: tutti i byte consegnati;
- `aborted`: relay iniziato e interrotto; `err` dice perché (`sender idle`, `receiver gone`, `length mismatch`, `relay interrupted`, errore di rete del mittente o del ricevente);
- `expired`: nessun ricevente entro `BASHT_WAIT_TIMEOUT`, o arresto del server;
- `sender_gone`: il mittente è caduto prima che un ricevente si agganciasse.

`wait_s` è l'attesa del ricevente, `transfer_s` la durata del relay, `rate_mbs` la velocità media in MB/s (10^6 byte). Le durate sono secondi con tre decimali, numeri anche in JSON.

`reason` di `request_rejected`, con il livello: `unauthorized` (401, WARN), `capacity` (503, WARN: `BASHT_MAX_SESSIONS` raggiunto), `range_not_supported` (400, INFO), `invalid_host` (400, INFO), `gone` (410, INFO: link già reclamato da un ricevente, pagina "Link already used or expired"), `gone_sender` (410, INFO: il mittente è caduto o ha annullato prima di ogni ricevente, pagina "Sender stopped"), `gone_expired` (410, INFO: nessun ricevente entro `BASHT_WAIT_TIMEOUT`, pagina "Link expired"), `not_found` (404, DEBUG: slug inesistenti, scansioni; INFO per il `PUT` del browser con token errato o prenotazione scaduta), `bad_request` (400: DEBUG per un `POST /` malformato, INFO per un `PUT` del browser di lunghezza diversa da quella prenotata), `unsupported_media_type` (415, DEBUG: `POST /` non JSON), `method_not_allowed` (405, DEBUG), `internal` (500 o 503, ERROR).

Esempio (`json`):

```json
{"time":"2026-10-29T18:02:11.4+01:00","level":"INFO","msg":"transfer","id":"9f3c2a71d04be6a8","outcome":"ok","bytes":268435456,"length":268435456,"wait_s":4.217,"transfer_s":2.31,"rate_mbs":116.21}
```

Esempi di analisi in [docs/operazioni.md](docs/operazioni.md#log).

## Limiti e sicurezza

- Lo slug è il segreto: chi lo conosce può scaricare finché il trasferimento è attivo. Usare HTTPS.
- Cosa conserva il server: nulla su disco. Il contenuto attraversa la memoria del processo solo durante il relay (il buffer di copia, `BASHT_BUF_SIZE`, più i buffer TCP del kernel e del proxy); nome, dimensione e slug restano in memoria finché la sessione è attiva, lo slug concluso (senza nome) per 24 ore come tombstone. I log dell'app non contengono slug, nomi né contenuto; quelli del proxy sì, se non configurati (vedi [Dietro Apache](#dietro-apache-mod_proxy_http2)).
- Cifratura: HTTPS protegge il file tra i client e il proxy (nginx o Apache). Non è cifratura end-to-end: il proxy decifra, e il tratto proxy-app (rete Docker interna; h2c in chiaro dietro Apache) e la memoria dell'app vedono il file in chiaro. Chi controlla il server mentre un trasferimento è in corso può leggerlo; i trasferimenti già conclusi non sono recuperabili dal server. Per i file riservati: cifrarli prima dell'invio (per esempio `gpg -c`, `age -p`, o un archivio 7z con password) e passare la password per un altro canale. La pagina di cortesia lo dice.
- Un solo ricevente per slug. Un secondo `GET` sullo stesso slug riceve `410`. La pagina di download per i browser non consuma lo slug: lo consuma il pulsante Download (`POST`).
- Uno slug concluso, scaduto o già reclamato risponde `410` per 24 ore; dopo, `404`.
- Il mittente deve restare connesso finché il ricevente non ha finito: se cade mentre attende (anche dopo aver inviato tutto il file), lo slug si libera entro circa un secondo e risponde `410`. Se i buffer TCP sono già pieni di dati non letti, la chiusura resta in coda dietro i dati: lo slug si libera quando il kernel del mittente abbandona la connessione (misurato in questo ambiente: circa 6 minuti); dietro nginx vale anche il keepalive verso il client. Su Linux il server controlla la connessione ogni secondo (`POLLRDHUP`), perché net/http non rileva la caduta finché il body non è letto; su altri sistemi la sessione resta fino a `BASHT_WAIT_TIMEOUT`.
- Il relay non salva nulla su disco: se il mittente o il ricevente cadono a metà, il trasferimento fallisce e il ricevente vede un errore (il download viene interrotto senza chunk finale). Se cade il ricevente, il mittente viene interrotto subito, anche se in quel momento non sta inviando byte.
- La risposta al mittente ha `Connection: close`: la connessione di un upload non viene riusata.
- Un mittente fermo più a lungo di `BASHT_IDLE_TIMEOUT` interrompe il trasferimento.
- Con `BASHT_AUTH_USER` non impostato chiunque può inviare un file, da terminale o dalla pagina web. L'app vede solo l'IP di nginx (stream senza PROXY protocol): l'unico limite per IP è `limit_conn` in nginx; non ci sono quote di banda né limiti di frequenza.
- Le tombstone sono al massimo 100000: sotto un carico molto alto uno slug concluso torna `404` prima delle 24 ore.
- Un ricevente che resta connesso senza leggere viene interrotto da `BASHT_IDLE_TIMEOUT`.
- Un arresto del server (SIGTERM) chiude subito i mittenti ancora senza ricevente (lo slug risponde `410` fino al riavvio, poi `404`: le sessioni non sopravvivono al processo) e lascia 10 secondi ai trasferimenti in corso, poi li interrompe. Un secondo SIGINT o SIGTERM termina subito il processo.
- Il relay non verifica l'integrità: il ricevente deve confrontare eventualmente un hash.

## Prestazioni

Misurate in loopback con `scripts/bench.sh` (8 CPU, Intel Xeon, go1.27.3, trasferimenti da 256 MiB, mediana di 5 campioni per sessione). Quattro sessioni con lo stesso codice. Rapporto relay su baseline a due hop equivalenti, mediana delle quattro sessioni: 1.10 con buffer 64 KiB, 0.99 con 256 KiB, 0.98 con 1 MiB. La variabilità tra sessioni è grande: i singoli rapporti vanno da 0.89 a 1.21. La soglia D13 (0.90) è rispettata dalla mediana a 256 KiB, ma una sessione singola è scesa a 0.896. Su una rete reale i valori dipendono dalla rete e non sono stati misurati.

## Prestazioni di rete

Con un link da 1 Gbit/s il limite non è il relay ma TCP: la finestra deve coprire il prodotto banda × RTT (1 Gbit/s × 40 ms = 5 MB) e l'algoritmo di congestione decide quanto si perde con la perdita di pacchetti. Ogni trasferimento ha due gambe TCP indipendenti, e la più lenta fissa la velocità:

- **download** (relay → ricevente): conta il lato che invia, cioè nginx (algoritmo e `tcp_wmem`), e `tcp_rmem` del ricevente;
- **upload** (mittente → relay): conta il mittente (algoritmo e `tcp_wmem`), che il server non controlla.

`compose.yaml` imposta nel namespace di rete di nginx (che termina le connessioni dei client) `bbr`, `tcp_rmem` e `tcp_wmem` con massimo 32 MiB e `tcp_slow_start_after_idle=0`. Sull'host serve il modulo `tcp_bbr`, che un container non può caricare:

```bash
sudo modprobe tcp_bbr
echo tcp_bbr | sudo tee /etc/modules-load.d/bbr.conf
```

Se il modulo manca, la scrittura del sysctl fallisce e il container nginx non dovrebbe partire (non verificato: qui il modulo è già caricato). `BASHT_TCP_CC=cubic` torna a cubic. Nessun container è privilegiato: i sysctl `net.*` sono per namespace. I buffer crescono solo quanto serve (autotuning), ma un ricevente lento può riempire il buffer di invio fino al massimo: il totale resta limitato da `net.ipv4.tcp_mem` dell'host.

Misura con `scripts/netbench.sh` (Docker, netem a 1 Gbit/s per direzione, HTTP diretto all'app, 10 s per punto, MB/s; 117 MB/s è la velocità di linea). Profili: `stock` = cubic, buffer massimi 6 MiB in ricezione e 4 MiB in invio (default del kernel); `tuned` = la configurazione di compose; `host` = la workstation di sviluppo (bbr, `tcp_rmem` 32 MiB, `tcp_wmem` 4 MiB).

| Gamba | Relay | Client | 20 ms | 20 ms, 0.1% perdita | 40 ms | 40 ms, 0.1% perdita |
|---|---|---|---|---|---|---|
| download | stock | stock | 115 | 19 | 60 | 25 |
| download | tuned | stock | 117 | 115 | 88 | 83 |
| download | tuned | host | 117 | 117 | 115 | 115 |
| upload | qualsiasi | stock | 118 | 21–29 | 67 | 29–38 |
| upload | qualsiasi | host | 118 | 111 | 67 | 61–62 |

Lato server, `bbr` con buffer stock si ferma a 68 MB/s a 40 ms (limite di `tcp_wmem`); cubic con buffer da 32 MiB tiene a 40 ms senza perdita (115) ma crolla con lo 0.1% (38). Solo la combinazione tiene la linea in tutti i casi.

Lato client, per usare tutto il gigabit anche in upload con RTT sopra ~30 ms, il mittente Linux deve alzare il buffer di invio (e conviene `bbr`):

```bash
sudo sysctl -w net.ipv4.tcp_wmem="4096 16384 33554432" net.ipv4.tcp_rmem="4096 131072 33554432"
sudo sysctl -w net.ipv4.tcp_congestion_control=bbr   # richiede il modulo tcp_bbr
```

Attraverso lo stack compose (nginx stream con TLS, Basic auth), stessa emulazione, client con il profilo `host`:

| Gamba | nginx | 20 ms | 20 ms, 0.1% perdita | 40 ms | 40 ms, 0.1% perdita |
|---|---|---|---|---|---|
| download | stock | 116 | 30 | 67 | 26 |
| download | compose (`tuned`) | 117 | 117 | 114 | 114 |
| upload | stock | 119 | 110 | 66 | 62 |
| upload | compose (`tuned`) | 118 | 109 | 66 | 62 |

Il TLS di nginx non è il limite: senza netem, con mittente e ricevente sulla rete interna, il relay attraverso nginx porta circa 950 MB/s (16 CPU condivise da tutti i container). Non misurato su reti reali. Un client in Wi-Fi è spesso limitato dalla radio prima che da TCP.

Comando usato: `BASHT_NB_IMAGE=<immagine locale con sh, curl, tc, ethtool, pkill> bash scripts/netbench.sh` (serve `sudo modprobe sch_netem` sull'host).

## Stato della verifica

- Test unitari e di integrazione (`go test -race ./...`), compresi concorrenza (40 trasferimenti paralleli), assenza di goroutine residue, ricevente fermo, mittente scaduto e keep-alive: verificati.
- Script end-to-end locale (`scripts/e2e.sh`: file e stdin, download con `wget`, mittente e ricevente che cadono a metà, mittente che cade in attesa del ricevente, trasferimenti paralleli, con e senza auth, nome con spazi, riga `direct`, pagina di download che non consuma il link, invio con il protocollo del browser e annullamento con `DELETE`): verificato, con curl 8.18 e 8.5.
- Pagine web e invio dal browser con Playwright (Chromium 1248 headless), con connessione diretta e attraverso Apache 2.4.69 in h2c: layout della home a 1280 e 390 px in tema chiaro e scuro (allineamento di campi e pulsanti, comandi su una riga, nessuno scroll orizzontale), pulsanti Copy, upload con il pulsante e con il trascinamento, rifiuto di più file, tabella dei link, ricevente curl e ricevente browser dalla pagina di download (sha256 identici, nome con lettere accentate), avanzamento e velocità, esiti (completato, link scaduto, ricevente disconnesso), Cancel e chiusura della pagina (link `410` entro 30 ms), chiusura dello stream senza `DELETE` (link `410` subito con connessione diretta, in circa 3,5 s via Apache), pagina chiusa durante il download (ricevente interrotto), pagina `404` e `410`, invio da pipe (dimensione ignota), Basic auth (login annullato e con credenziali). Script: `scripts/ui/run.sh` (vedi [Sviluppo](#sviluppo)), eseguito anche dalla CI nei due modi. Altri browser non sono stati provati.
- Immagine Docker e configurazione compose (`docker compose config`, avvio di `app` e risposta a `GET /` dalla rete interna): verificati.
- Percorso completo attraverso nginx (stack compose, porta 443, certificato di test): `scripts/e2e.sh` esterno con 256 MiB, auth (`BASHT_E2E_AUTH`), 20 trasferimenti paralleli, `wget`, caduta di mittente e ricevente (anche del mittente in attesa), sha256 identici: verificato. Redirect da 80 e `GET /` via HTTPS: verificati. Il certificato di test non è stato verificato con una CA pubblica.
- Auth attraverso nginx (senza credenziali e con credenziali errate: `401`; con credenziali corrette: trasferimento identico; `GET` dello slug senza credenziali: `410` dopo l'uso): verificata dall'e2e esterno con `BASHT_E2E_AUTH`.
- Healthcheck dell'immagine e avvio ordinato di nginx dopo `app`: verificati.
- Stack di produzione con isolamento (nginx 1.30 in sola lettura con capability ridotte, rete `backend` interna senza uscita verso l'esterno): e2e esterno con auth e 20 trasferimenti paralleli, redirect da 80, arresto di nginx con un trasferimento in corso (atteso fino alla fine, file integro): verificati.
- Immagine multi-architettura: build `linux/amd64` e `linux/arm64` verificata in locale (binario arm64 statico). Il workflow GitHub è validato con `actionlint` ma non eseguito (nessun remote).
- `aria2c` 1.37 contro il relay: download da file e da pipe con sha256 identico; anche con `-x4` usa una sola connessione e completa.
- Riga `aria2c`, rifiuto della ripresa (`curl -C -`: `400`, slug ancora scaricabile), interruzione immediata del mittente quando il ricevente cade, chiusura dei mittenti in attesa all'arresto: verificati da test Go e `scripts/e2e.sh` locale, compreso il download con `aria2c` 1.37.
- La sorgente non viene mai alterata (`scripts/invariant.py`, eseguito dall'e2e locale e dalla CI): 2488 casi con curl 8.18, GNU wget 1.25, busybox wget 1.37 e aria2c 1.37; 2060 casi del ricevente e 178 del mittente senza nessuna sorgente alterata, il resto sono i limiti elencati in [La sorgente non viene mai alterata](#la-sorgente-non-viene-mai-alterata). Tre build volutamente difettose vengono rilevate: righe stampate `curl -f -o "nome"` (36 casi) e `wget -nc -O "nome"` (3 casi, busybox ignora `-nc`), risposte senza `Content-Disposition` (16 casi del mittente con `-OJ`).
- Effetto di algoritmo di congestione e buffer TCP: misurato con `scripts/netbench.sh` e attraverso lo stack compose con netem (vedi "Prestazioni di rete"). Stack compose con i sysctl di nginx: `scripts/e2e.sh` esterno con 256 MiB, auth e 20 trasferimenti paralleli verificato.

## Sviluppo

```bash
go test -race ./...
bash scripts/e2e.sh
# contro uno stack già avviato (BASHT_PUBLIC_URL uguale a BASHT_E2E_BASE):
BASHT_E2E_BASE=https://127.0.0.1 BASHT_E2E_MB=256 bash scripts/e2e.sh
# stack con Basic auth: ogni PUT usa le credenziali e si verificano i 401
BASHT_E2E_BASE=https://127.0.0.1 BASHT_E2E_AUTH=utente:password BASHT_E2E_PARALLEL=20 bash scripts/e2e.sh
# solo l'invariante della sorgente (l'e2e locale la esegue alla fine); fallisce se manca uno strumento
go build -o /tmp/bt ./cmd/bashtransfer
BASHT_INV_REQUIRE=curl,wget,aria2c,busybox python3 scripts/invariant.py /tmp/bt
# test frontend con Playwright (Chromium headless); la prima volta, da scripts/ui:
#   npm ci && npx playwright-core install chromium   (--with-deps se mancano le librerie di sistema)
bash scripts/ui/run.sh          # server diretti
bash scripts/ui/run.sh apache   # attraverso Apache 2.4 in Docker (proxy h2c)
```

`scripts/invariant.py` usa curl, wget, aria2c e busybox del sistema (quelli assenti vengono saltati, salvo quelli in `BASHT_INV_REQUIRE`), lancia quattro istanze del server su porte libere (normale, attesa breve, con auth, con capacità 1) e scrive l'esito di ogni caso in JSON, una riga per caso, nel file indicato da `BASHT_INV_DUMP`. `BASHT_INV_JOBS` (default 8) è il numero di casi in parallelo.

`scripts/ui/run.sh` compila il binario, avvia tre server (normale, attesa di 3 secondi, Basic auth `u:p`), direttamente su porte libere di `127.0.0.1` oppure in container dietro un Apache 2.4.69 configurato come nel vhost di [Dietro Apache](#dietro-apache-mod_proxy_http2) (`scripts/ui/httpd.conf`, immagine `httpd` fissata per digest), ed esegue `scripts/ui/ui.mjs`: 18 test su layout (1280 e 390 px, tema chiaro e scuro), pulsanti Copy, invio dal browser con il pulsante e con il trascinamento, ricevente curl e browser, esiti, Cancel, chiusura della pagina, link usati e scaduti, Basic auth. Chromium non può collegarsi ad altri host che `127.0.0.1` e `localhost`. `BASHT_UI_OUT` conserva file e screenshot; `ONLY` (regex) sceglie i test.
