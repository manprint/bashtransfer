# Operazioni

Deploy e gestione di bashtransfer su una VM con Docker. Uso e configurazione sono nel [README](../README.md); il funzionamento interno in [architettura.md](architettura.md).

## Requisiti della VM

- Linux con kernel ≥ 4.15 (sysctl TCP per namespace di rete), Docker Engine con il plugin `compose`.
- Porte 80 e 443 raggiungibili da Internet; nessun'altra porta serve (l'app non pubblica porte).
- Modulo `tcp_bbr` caricato sull'host: compose imposta `bbr` nel namespace di rete di nginx e un container non può caricare moduli.
- Certificato TLS per il nome pubblico (`fullchain.pem`, `privkey.pem`), gestito fuori dal repository (per esempio certbot sull'host).
- Memoria: ogni sessione attiva usa al più un buffer di copia (256 KiB di default) nell'app più i buffer TCP del kernel, che crescono fino a 32 MiB per socket solo quanto serve; il totale è limitato da `net.ipv4.tcp_mem` dell'host.

## Primo deploy

1. Caricare BBR, anche ai riavvii:

   ```bash
   sudo modprobe tcp_bbr
   echo tcp_bbr | sudo tee /etc/modules-load.d/bbr.conf
   ```

2. Servono sulla VM solo `compose.yaml` e `deploy/nginx/nginx.conf` (clonare il repository è il modo più semplice). Creare `.env` accanto a `compose.yaml` (non versionato, permessi `600`):

   ```
   BASHT_IMAGE=ghcr.io/owner/bashtransfer:1.0.0
   BASHT_CERTS_DIR=/etc/ssl/bashtransfer
   BASHT_PUBLIC_URL=https://bashtransfer.mydomain.it
   BASHT_AUTH_USER=utente
   BASHT_AUTH_PASSWORD=una-password-lunga
   ```

   `BASHT_CERTS_DIR` deve contenere `fullchain.pem` e `privkey.pem` come file regolari. Con certbot i file in `live/` sono link simbolici relativi verso `archive/`, che dentro il container non si risolvono: copiarli in `/etc/ssl/bashtransfer` con un deploy hook (sezione "Rinnovo del certificato"). nginx gira senza `DAC_OVERRIDE`: i file devono essere di root o leggibili da tutti; `privkey.pem` di root con permessi `600` va bene (verificato).

   Se il pacchetto su ghcr.io è privato: `docker login ghcr.io` con un token con permesso `read:packages`.

3. Avviare e controllare:

   ```bash
   docker compose pull
   docker compose up -d
   docker compose ps                     # app: healthy, nginx: running
   docker compose logs app | head        # server_start, server_listening
   curl -sS https://bashtransfer.mydomain.it/
   ```

4. Verificare i parametri TCP effettivi di nginx:

   ```bash
   docker compose exec nginx sysctl net.ipv4.tcp_congestion_control net.ipv4.tcp_rmem net.ipv4.tcp_wmem
   ```

   Attesi `bbr`, `4096 131072 33554432`, `4096 16384 33554432`. Se il modulo `tcp_bbr` manca, il container nginx non dovrebbe partire (errore sul sysctl): caricare il modulo, oppure `BASHT_TCP_CC=cubic` nel `.env`.

5. Prova end-to-end dalla workstation (non dalla VM):

   ```bash
   BASHT_E2E_BASE=https://bashtransfer.mydomain.it BASHT_E2E_MB=256 \
     BASHT_E2E_AUTH=utente:una-password-lunga bash scripts/e2e.sh
   ```

   Lo script accetta qualsiasi certificato. Senza auth configurata omettere `BASHT_E2E_AUTH`. Lascia nei log un `transfer` con `outcome` `aborted` per ogni caso di caduta simulata: è atteso.

## Aggiornamenti

Impostare in `.env` il nuovo tag (`BASHT_IMAGE=ghcr.io/owner/bashtransfer:1.1.0`; usare tag di versione, non `latest`, per sapere cosa gira e poter tornare indietro), poi:

```bash
git pull                # solo se cambiano compose.yaml o nginx.conf
docker compose pull
docker compose up -d
```

Ritorno alla versione precedente: rimettere il tag precedente in `.env` e ripetere `docker compose up -d`.

La ricreazione di `app` interrompe i trasferimenti in corso dopo 10 secondi di grazia; i mittenti in attesa vengono chiusi subito. Gli slug emessi prima del riavvio rispondono `404` (le sessioni sono solo in memoria). nginx risolve `app` a ogni connessione e non va riavviato.

## Rinnovo del certificato

nginx legge i certificati all'avvio. Dopo il rinnovo:

```bash
docker compose exec nginx nginx -s reload
```

Il reload non interrompe i trasferimenti: i worker vecchi terminano quando le loro connessioni finiscono. Con certbot, un deploy hook che copia i file e ricarica nginx, per esempio `/etc/letsencrypt/renewal-hooks/deploy/bashtransfer.sh` (eseguibile):

```bash
#!/bin/sh
set -e
install -m 644 -o root -g root /etc/letsencrypt/live/bashtransfer.mydomain.it/fullchain.pem /etc/ssl/bashtransfer/fullchain.pem
install -m 600 -o root -g root /etc/letsencrypt/live/bashtransfer.mydomain.it/privkey.pem /etc/ssl/bashtransfer/privkey.pem
cd /opt/bashtransfer && docker compose exec -T nginx nginx -s reload
```

(`/opt/bashtransfer` è la directory con `compose.yaml` e `.env`.)

## Log

Formato JSON in compose (`BASHT_LOG_FORMAT`), rotazione 3 × 10 MB per container. Eventi e campi sono descritti nel README, sezione "Log". Esempi con `jq`:

```bash
# tutti gli esiti, in diretta
docker compose logs -f --no-log-prefix app | jq -c 'select(.msg=="transfer")'

# trasferimenti falliti con il motivo
docker compose logs --no-log-prefix app | jq -c 'select(.msg=="transfer" and .outcome!="ok") | {time, outcome, err, bytes}'

# conteggio per esito
docker compose logs --no-log-prefix app | jq -r 'select(.msg=="transfer") | .outcome' | sort | uniq -c

# velocità dei trasferimenti riusciti sopra 100 MB (MB/s)
docker compose logs --no-log-prefix app | jq -r 'select(.msg=="transfer" and .outcome=="ok" and .bytes>1e8) | .rate_mbs'

# tentativi di upload con credenziali errate e sessioni esaurite
docker compose logs --no-log-prefix app | jq -c 'select(.msg=="request_rejected" and (.reason=="unauthorized" or .reason=="capacity"))'
```

`BASHT_LOG_LEVEL=debug` aggiunge `upload_accepted`, `download_claimed`, `receiver_attached` (tutti con lo stesso `id` del `transfer` finale) e i `404`. L'app vede solo l'IP di nginx: gli IP dei client non sono nei log dell'app. Il blocco `stream` di nginx non ha access log.

## Monitoraggio

- Stato del container: `docker inspect -f '{{.State.Health.Status}}' $(docker compose ps -q app)`. L'healthcheck esegue `GET /` ogni 30 s.
- Segnali da osservare nei log: `request_rejected` con `reason=capacity` (aumentare `BASHT_MAX_SESSIONS` o cercare chi occupa sessioni), molti `unauthorized` (tentativi sulla password), `transfer` con `outcome=aborted` e `err="sender idle"` (mittenti lenti o bloccati), qualsiasi evento a livello `ERROR`.

## Risoluzione dei problemi

| Sintomo | Causa probabile | Cosa fare |
|---|---|---|
| nginx non parte, errore su `tcp_congestion_control` | modulo `tcp_bbr` non caricato | `sudo modprobe tcp_bbr`, oppure `BASHT_TCP_CC=cubic` |
| `docker compose up` chiede `BASHT_IMAGE`, `BASHT_PUBLIC_URL` o `BASHT_CERTS_DIR` | variabile obbligatoria mancante | impostarla nel `.env` |
| `docker compose pull` fallisce con `denied` o `unauthorized` | pacchetto ghcr.io privato | renderlo pubblico o `docker login ghcr.io` con un token `read:packages` |
| nginx non parte: `cannot load certificate` o `Permission denied` | link simbolici di certbot o file non di root e non leggibili | copiare i file come file regolari di root (deploy hook sopra) |
| upload lento con RTT alto, download veloce | buffer di invio del mittente (default Linux 4 MiB: ~66 MB/s a 40 ms) | sul mittente: `sysctl -w net.ipv4.tcp_wmem="4096 16384 33554432"` e `bbr` (README, "Prestazioni di rete") |
| download lento con RTT alto | buffer di ricezione del ricevente o perdita sul suo percorso | sul ricevente: `net.ipv4.tcp_rmem` con massimo 32 MiB |
| velocità ferma sotto ~200–300 Mbit/s su un solo lato | client in Wi-Fi | misurare con il client via cavo |
| `401` all'upload | credenziali assenti o errate | `curl -u utente:password -T ...` |
| `503` all'upload | `BASHT_MAX_SESSIONS` raggiunto | vedere i log; aumentare il limite |
| il client riceve un reset dopo 64 connessioni | `limit_conn perip 64` in nginx | alzare o rimuovere in `deploy/nginx/nginx.conf` |
| `410` al download | slug già usato, concluso o scaduto | il mittente deve inviare di nuovo |
| `404` al download | slug sbagliato, oppure server riavviato | controllare l'URL; reinviare |
| `400` al download | ripresa (`curl -C -`, `wget -c`) o download manager con più connessioni | scaricare dall'inizio con il comando stampato |
| trasferimento interrotto dopo una pausa del mittente | `BASHT_IDLE_TIMEOUT` (2 min) | alzarlo se il comando del mittente può restare fermo a lungo (per esempio `tar` su molti file piccoli lenti) |
| il mittente aspetta e lo slug scade | nessun ricevente entro `BASHT_WAIT_TIMEOUT` (1 h) | alzarlo; deve restare sotto `proxy_timeout` di nginx (24 h) |

## Script di verifica e misura

| Script | Cosa fa | Dove |
|---|---|---|
| `go test -race ./...` | test unitari e di integrazione | sviluppo |
| `scripts/e2e.sh` | e2e con curl, wget, aria2c (se installato), cadute, paralleli, auth; locale o contro uno stack (`BASHT_E2E_BASE`) | sviluppo, VM |
| `scripts/bench.sh` | throughput del relay su loopback contro una baseline a due hop | sviluppo |
| `scripts/netbench.sh` | effetto di algoritmo di congestione e buffer TCP con netem in Docker (richiede `sudo modprobe sch_netem` e un'immagine locale con `tc`) | sviluppo |

`netbench.sh` cambia i sysctl solo dentro i namespace di rete dei suoi container: l'host non viene modificato.
