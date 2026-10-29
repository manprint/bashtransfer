# bashtransfer

Relay di file: il mittente invia un file con `curl -T`, il server risponde subito con il comando di download, e il ricevente lo esegue. Il file passa in streaming dal mittente al ricevente e non viene mai salvato su disco.

## Uso

### Scenario 1: file

Mittente:

```bash
curl -T myfile.zip https://bashtransfer.mydomain.it
```

Risposta immediata (tre righe, una per strumento):

```
curl -o "myfile.zip" "https://bashtransfer.mydomain.it/AbC123xY9z"
wget -O "myfile.zip" "https://bashtransfer.mydomain.it/AbC123xY9z"
aria2c -x1 -s1 -o "myfile.zip" "https://bashtransfer.mydomain.it/AbC123xY9z"
```

Ricevente: esegue una delle tre righe. Il file viene scaricato mentre il mittente lo invia.

Il nome del file è preso dal path dell'upload. Con `curl -T myfile.zip https://host` curl invia il nome locale come path e il comando è `-o "myfile.zip"` (verificato). Il nome `download` si usa solo quando il mittente invia da stdin (`-T -`) senza nome nel path.

### Scenario 2: stream da comando

Mittente, senza file su disco:

```bash
sudo tar -cpzf - mydir | curl -N -T - https://bashtransfer.mydomain.it/mydir.tgz
```

Il ricevente riceve `mydir.tgz`. `curl -N` disattiva il buffering dell'output: serve per vedere la riga di comando subito durante l'upload di un pipe. Con un file (scenario 1) non serve.

Senza nome nel path (`... | curl -T - https://bashtransfer.mydomain.it`) il ricevente riceve un file chiamato `download`.

### Limiti del nome

Il nome viene ripulito: si tiene solo l'ultimo segmento del path, si tolgono caratteri di controllo (anche C1), virgolette, backslash, separatori di riga e controlli bidirezionali Unicode, si tolgono i punti iniziali (`.bashrc` diventa `bashrc`: il ricevente esegue il comando nella sua directory e non deve sovrascrivere file nascosti), si limita a 128 caratteri. Un `/` codificato (`%2F`) conta come separatore: resta l'ultima parte. `$`, backtick e `!` diventano `_`, perché il nome finisce tra virgolette doppie in una riga che l'utente incolla in una shell. Un nome vuoto, o `-` (che per `curl -o` e `wget -O` è lo stdout), diventa `download`.

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
| `BASHT_IDLE_TIMEOUT` | `2m` | Interruzione del relay se il mittente non invia byte per questo tempo, o se il ricevente non accetta una singola scrittura entro questo tempo (minimo `1s`). |
| `BASHT_MAX_SESSIONS` | `1000` | Numero massimo di slug attivi contemporaneamente. |
| `BASHT_BUF_SIZE` | `262144` | Dimensione del buffer di copia, tra 4096 e 4194304 byte. Il default 256 KiB è il buffer con il throughput assoluto più alto del relay in tutte e quattro le sessioni. |

## Autenticazione

Se `BASHT_AUTH_USER` e `BASHT_AUTH_PASSWORD` sono impostate, ogni `PUT` richiede Basic auth; senza credenziali o con credenziali errate la risposta è `401` con `WWW-Authenticate`. Il download (`GET`) non richiede credenziali: chi ha lo slug può scaricare.

```bash
curl -u utente:password -T myfile.zip https://bashtransfer.mydomain.it
```

Se le variabili non sono impostate, l'upload è anonimo.

## Deploy con Docker Compose

Il file `compose.yaml` avvia due servizi: `app` (il relay, sulla rete interna, senza porte pubblicate) e `nginx` (TLS e porte 80 e 443).

1. Preparare una directory con `fullchain.pem` e `privkey.pem`. Il repository non gestisce certificati.
2. Impostare le variabili desiderate, per esempio in un file `.env` non versionato accanto a `compose.yaml`:

   ```
   BASHT_CERTS_DIR=/etc/ssl/bashtransfer
   BASHT_PUBLIC_URL=https://bashtransfer.mydomain.it
   BASHT_AUTH_USER=utente
   BASHT_AUTH_PASSWORD=una-password-lunga
   ```

3. Avviare:

   ```bash
   docker compose up -d --build
   ```

La configurazione nginx è in `deploy/nginx/nginx.conf`: worker automatici, `worker_connections 8192`, risoluzione DNS a ogni connessione (il container `app` può essere ricreato senza riavviare nginx), buffer di copia da 128k, keepalive TCP verso il client (60 s, poi 4 sonde ogni 15 s: un client sparito libera la sessione in circa 2 minuti) e un tetto di 64 connessioni simultanee per IP del client (`limit_conn`; rimuovere le due righe per disattivarlo). TLS è terminato dal blocco `stream` (proxy TCP): il relay passa byte per byte, senza il proxy HTTP di nginx, che interrompe il body di un upload quando il server ha già risposto (verificato in questo ambiente). Il blocco `http` serve solo il redirect da 80 a 443.

Perché nginx non aggiunge header HTTP, `BASHT_PUBLIC_URL` è obbligatorio in compose: i comandi stampati usano quell'URL, non `Host` o `X-Forwarded-Proto`.

Senza nginx il server parla solo HTTP semplice: non esegue TLS.

L'immagine ha un `HEALTHCHECK` (`/bashtransfer -healthcheck`: `GET /` su `BASHT_LISTEN`, solo loopback); `nginx` parte quando `app` è sano. Il container `app` gira con filesystem in sola lettura, senza capability e senza nuovi privilegi; i log hanno rotazione (3 file da 10 MB).

## Log

Una riga per trasferimento su stderr: `outcome` (`ok`, `aborted`, `expired`, `sender_gone`), byte, durata ed eventuale errore. Slug e nome del file non compaiono mai nei log.

## Limiti e sicurezza

- Lo slug è il segreto: chi lo conosce può scaricare finché il trasferimento è attivo. Usare HTTPS.
- Un solo ricevente per slug. Un secondo `GET` sullo stesso slug riceve `410`.
- Uno slug concluso, scaduto o già reclamato risponde `410` per 24 ore; dopo, `404`.
- Il mittente deve restare connesso finché il ricevente non ha finito: se cade mentre attende (anche dopo aver inviato tutto il file), lo slug si libera entro circa un secondo e risponde `410`. Se i buffer TCP sono già pieni di dati non letti, la chiusura resta in coda dietro i dati: lo slug si libera quando il kernel del mittente abbandona la connessione (misurato in questo ambiente: circa 6 minuti); dietro nginx vale anche il keepalive verso il client. Su Linux il server controlla la connessione ogni secondo (`POLLRDHUP`), perché net/http non rileva la caduta finché il body non è letto; su altri sistemi la sessione resta fino a `BASHT_WAIT_TIMEOUT`.
- Il relay non salva nulla su disco: se il mittente o il ricevente cadono a metà, il trasferimento fallisce e il ricevente vede un errore (il download viene interrotto senza chunk finale). Se cade il ricevente, il mittente viene interrotto subito, anche se in quel momento non sta inviando byte.
- La risposta al mittente ha `Connection: close`: la connessione di un upload non viene riusata.
- Un mittente fermo più a lungo di `BASHT_IDLE_TIMEOUT` interrompe il trasferimento.
- Con `BASHT_AUTH_USER` non impostato chiunque può inviare un file. L'app vede solo l'IP di nginx (stream senza PROXY protocol): l'unico limite per IP è `limit_conn` in nginx; non ci sono quote di banda né limiti di frequenza.
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
- Script end-to-end locale (`scripts/e2e.sh`: file e stdin, download con `wget`, mittente e ricevente che cadono a metà, mittente che cade in attesa del ricevente, trasferimenti paralleli, con e senza auth, nome con spazi): verificato.
- Immagine Docker e configurazione compose (`docker compose config`, avvio di `app` e risposta a `GET /` dalla rete interna): verificati.
- Percorso completo attraverso nginx (stack compose, porta 443, certificato di test): `scripts/e2e.sh` esterno con 256 MiB, auth (`BASHT_E2E_AUTH`), 20 trasferimenti paralleli, `wget`, caduta di mittente e ricevente (anche del mittente in attesa), sha256 identici: verificato. Redirect da 80 e `GET /` via HTTPS: verificati. Il certificato di test non è stato verificato con una CA pubblica.
- Auth attraverso nginx (senza credenziali e con credenziali errate: `401`; con credenziali corrette: trasferimento identico; `GET` dello slug senza credenziali: `410` dopo l'uso): verificata dall'e2e esterno con `BASHT_E2E_AUTH`.
- Healthcheck dell'immagine e avvio ordinato di nginx dopo `app`: verificati.
- Riga `aria2c`, rifiuto della ripresa (`curl -C -`: `400`, slug ancora scaricabile), interruzione immediata del mittente quando il ricevente cade, chiusura dei mittenti in attesa all'arresto: verificati da test Go e `scripts/e2e.sh` locale. Il download con `aria2c` è nell'e2e ma non è stato eseguito (`aria2c` non installato in questo ambiente).
- Effetto di algoritmo di congestione e buffer TCP: misurato con `scripts/netbench.sh` e attraverso lo stack compose con netem (vedi "Prestazioni di rete"). Stack compose con i sysctl di nginx: `scripts/e2e.sh` esterno con 256 MiB, auth e 20 trasferimenti paralleli verificato.

## Sviluppo

```bash
go test -race ./...
bash scripts/e2e.sh
# contro uno stack già avviato (BASHT_PUBLIC_URL uguale a BASHT_E2E_BASE):
BASHT_E2E_BASE=https://127.0.0.1 BASHT_E2E_MB=256 bash scripts/e2e.sh
# stack con Basic auth: ogni PUT usa le credenziali e si verificano i 401
BASHT_E2E_BASE=https://127.0.0.1 BASHT_E2E_AUTH=utente:password BASHT_E2E_PARALLEL=20 bash scripts/e2e.sh
```
