# bashtransfer

Relay di file: il mittente invia un file con `curl -T`, il server risponde subito con il comando di download, e il ricevente lo esegue. Il file passa in streaming dal mittente al ricevente e non viene mai salvato su disco.

## Uso

### Scenario 1: file

Mittente:

```bash
curl -T myfile.zip https://bashtransfer.mydomain.it
```

Risposta immediata (due righe, una per strumento):

```
curl -o "myfile.zip" "https://bashtransfer.mydomain.it/AbC123xY9z"
wget -O "myfile.zip" "https://bashtransfer.mydomain.it/AbC123xY9z"
```

Ricevente: esegue una delle due righe. Il file viene scaricato mentre il mittente lo invia.

Il nome del file è preso dal path dell'upload: `curl -T myfile.zip https://host/myfile.zip` produce `-o "myfile.zip"`. Con `https://host` senza nome il file si chiama `download`.

### Scenario 2: stream da comando

Mittente, senza file su disco:

```bash
sudo tar -cpzf - mydir | curl -N -T - https://bashtransfer.mydomain.it/mydir.tgz
```

Il ricevente riceve `mydir.tgz`. `curl -N` disattiva il buffering dell'output: serve per vedere la riga di comando subito durante l'upload di un pipe. Con un file (scenario 1) non serve.

Senza nome nel path (`... | curl -T - https://bashtransfer.mydomain.it`) il ricevente riceve un file chiamato `download`.

### Limiti del nome

Il nome viene ripulito: si tiene solo l'ultimo segmento del path, si tolgono caratteri di controllo, virgolette e backslash, si limita a 128 caratteri. Un nome vuoto diventa `download`.

## Configurazione

Tutte le variabili sono opzionali. Un valore vuoto equivale a non impostarlo.

| Variabile | Default | Significato |
|-----------|---------|-------------|
| `BASHT_LISTEN` | `:8080` | Indirizzo di ascolto HTTP del server. |
| `BASHT_PUBLIC_URL` | vuoto | Base degli URL nei comandi. Se vuoto, si usa `X-Forwarded-Proto` (oppure `https` con TLS diretto) e `Host`. |
| `BASHT_AUTH_USER` | vuoto | Utente per l'upload. Va impostato insieme a `BASHT_AUTH_PASSWORD`. |
| `BASHT_AUTH_PASSWORD` | vuoto | Password per l'upload. |
| `BASHT_WAIT_TIMEOUT` | `1h` | Quanto il mittente può attendere un ricevente prima che lo slug scada. |
| `BASHT_IDLE_TIMEOUT` | `2m` | Interruzione del relay se il mittente non invia byte per questo tempo (minimo `1s`). |
| `BASHT_MAX_SESSIONS` | `1000` | Numero massimo di slug attivi contemporaneamente. |
| `BASHT_BUF_SIZE` | `262144` | Dimensione del buffer di copia, tra 4096 e 4194304 byte. Il default 256 KiB è quello misurato più veloce. |

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

La configurazione nginx è in `deploy/nginx/bashtransfer.conf`: redirect da 80 a 443, body illimitato, nessun buffer sul relay, timeout di lettura e scrittura di 3600 secondi, `client_body_timeout` di 3600 secondi (il default di 60 secondi tronca mittenti fermi più a lungo), header `Host`, `X-Forwarded-Proto` e `X-Forwarded-For`.

Senza nginx il server parla solo HTTP semplice: non esegue TLS.

## Limiti e sicurezza

- Lo slug è il segreto: chi lo conosce può scaricare finché il trasferimento è attivo. Usare HTTPS.
- Un solo ricevente per slug. Un secondo `GET` sullo stesso slug riceve `410`.
- Uno slug concluso, scaduto o già reclamato risponde `410` per 24 ore; dopo, `404`.
- Un mittente che attende senza che nessuno scarichi viene chiuso solo alla scadenza di `BASHT_WAIT_TIMEOUT`: il server Go non rileva la caduta del mittente finché non ha letto il body.
- Il relay non salva nulla su disco: se il mittente o il ricevente cadono a metà, il trasferimento fallisce e il ricevente vede un errore (il download viene interrotto senza chunk finale).
- Un mittente fermo più a lungo di `BASHT_IDLE_TIMEOUT` interrompe il trasferimento.
- Con `BASHT_AUTH_USER` non impostato chiunque può inviare un file.
- Il relay non verifica l'integrità: il ricevente deve confrontare eventualmente un hash.

## Prestazioni

Misurate in loopback con `scripts/bench.sh` (8 CPU, Intel Xeon, go1.27.3, trasferimenti da 256 MiB, mediana di 5 campioni). Rapporto relay su baseline a due hop equivalenti: 1.02 con buffer 64 KiB, 0.956 con 256 KiB, 0.991 con 1 MiB. Su una rete reale i valori dipendono dalla rete e non sono stati misurati.

## Stato della verifica

- Test unitari e di integrazione (`go test -race ./...`): verificati.
- Script end-to-end locale (`scripts/e2e.sh`, con e senza auth, con nome con spazi): verificato.
- Immagine Docker e configurazione compose (`docker compose config`, avvio di `app` e risposta a `GET /` dalla rete interna): verificati.
- Percorso completo attraverso nginx: **non verificato**. In questo ambiente il trasferimento si blocca dopo circa 0.1–0.8 MB quando passa da nginx, anche con una configurazione minimale. La causa non è stata isolata.

## Sviluppo

```bash
go test -race ./...
bash scripts/e2e.sh
```
