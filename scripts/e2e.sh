#!/usr/bin/env bash
# Test end-to-end locale: RS-1 e RS-2 contro il binario appena compilato su 127.0.0.1.
# Tutti i file temporanei stanno in una directory creata qui e rimossa all'uscita.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
SERVER_PID=""
AUTH_PID=""
UP_PIDS=()
# Credenziali per ogni PUT verso BASE (solo in modalità esterna con BASHT_E2E_AUTH).
UP_AUTH=()

cleanup() {
	for pid in "${UP_PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
	if [[ -n "$SERVER_PID" ]]; then kill "$SERVER_PID" 2>/dev/null || true; fi
	if [[ -n "$AUTH_PID" ]]; then kill "$AUTH_PID" 2>/dev/null || true; fi
	rm -rf "$TMP"
}
trap cleanup EXIT

fail() {
	echo "E2E FAIL: $*" >&2
	if [[ -f "$TMP/rx.err" ]]; then echo "--- stderr dell'ultimo comando stampato ---" >&2; cat "$TMP/rx.err" >&2; fi
	if [[ -f "$TMP/server.log" ]]; then echo "--- server.log ---" >&2; cat "$TMP/server.log" >&2; fi
	exit 1
}

# BASHT_E2E_BASE: testa uno stack già avviato (es. nginx su https://127.0.0.1); il certificato
# di test è accettato. BASHT_E2E_MB: dimensione del payload in MiB (default 8).
MB="${BASHT_E2E_MB:-8}"
head -c $((MB * 1048576)) /dev/urandom > "$TMP/payload.bin"
PAYLOAD_SUM="$(sha256sum "$TMP/payload.bin" | awk '{print $1}')"

EXTERNAL=""
if [[ -n "${BASHT_E2E_BASE:-}" ]]; then
	EXTERNAL=1
	BASE="$BASHT_E2E_BASE"
	mkdir -p "$TMP/curlhome"
	printf 'insecure\n' > "$TMP/curlhome/.curlrc"
	export CURL_HOME="$TMP/curlhome"
	for _ in $(seq 1 100); do
		if curl -sf -o /dev/null "$BASE/"; then break; fi
		sleep 0.1
	done
	curl -sf -o /dev/null "$BASE/" || fail "lo stack non risponde su $BASE"
	# BASHT_E2E_AUTH=utente:password: lo stack ha Basic auth; ogni PUT la usa e si verificano i 401.
	AUTH_BASE="$BASE"
	AUTH_CRED="${BASHT_E2E_AUTH:-}"
	if [[ -n "$AUTH_CRED" ]]; then UP_AUTH=(-u "$AUTH_CRED"); fi
else
	PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')"
	BASE="http://127.0.0.1:$PORT"

	(cd "$ROOT" && go build -o "$TMP/bashtransfer" ./cmd/bashtransfer)

	BASHT_LISTEN="127.0.0.1:$PORT" "$TMP/bashtransfer" > "$TMP/server.log" 2>&1 &
	SERVER_PID=$!

	for _ in $(seq 1 100); do
		if curl -sf -o /dev/null "$BASE/"; then break; fi
		sleep 0.1
	done
	curl -sf -o /dev/null "$BASE/" || fail "il server non risponde su $BASE"

	# Secondo server con Basic auth (D9): stessa binaria, altra porta.
	AUTH_PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')"
	AUTH_BASE="http://127.0.0.1:$AUTH_PORT"
	AUTH_CRED="alice:s3cret"
	BASHT_LISTEN="127.0.0.1:$AUTH_PORT" BASHT_AUTH_USER=alice BASHT_AUTH_PASSWORD=s3cret \
		"$TMP/bashtransfer" > "$TMP/auth.log" 2>&1 &
	AUTH_PID=$!
	for _ in $(seq 1 100); do
		if curl -sf -o /dev/null "$AUTH_BASE/"; then break; fi
		sleep 0.1
	done
	curl -sf -o /dev/null "$AUTH_BASE/" || fail "il server con auth non risponde su $AUTH_BASE"
fi

# Attende la riga curl della tabella dei comandi ("| curl   | curl -f -OJ ... |") e ne estrae lo slug.
wait_slug() {
	local out="$1"
	for _ in $(seq 1 100); do
		if grep -q '^| curl ' "$out" 2>/dev/null; then break; fi
		sleep 0.1
	done
	sed -n 's#^| curl *| curl -f -OJ ".*/\([0-9A-Za-z]\{10\}\)" *|$#\1#p' "$out" | head -n 1
}

# Comando stampato per uno strumento (curl, wget, aria2c) nella tabella in $2.
printed() {
	sed -n "s#^| $1 *| \(.*[^ ]\) *|\$#\1#p" "$2" | head -n 1
}

# I tre comandi stampati per l'URL $2/$3, senza nome di file (scripts/invariant.py), e il link
# per il browser (riga direct).
check_commands() {
	local out="$1" u="$2/$3"
	[[ "$(printed direct "$out")" == "$u" ]] || fail "riga direct errata: $(printed direct "$out")"
	[[ "$(printed curl "$out")" == "curl -f -OJ \"$u\"" ]] || fail "riga curl errata: $(printed curl "$out")"
	[[ "$(printed wget "$out")" == "wget --content-disposition \"$u\"" ]] || fail "riga wget errata: $(printed wget "$out")"
	[[ "$(printed aria2c "$out")" == "aria2c -x1 -s1 --allow-overwrite=false \"$u\"" ]] ||
		fail "riga aria2c errata: $(printed aria2c "$out")"
}

# HOME del ricevente: nessuna configurazione dell'utente (.curlrc, .wgetrc, aria2.conf) cambia
# l'esito dei comandi stampati; solo certificato di test accettato e un solo tentativo.
RXHOME="$TMP/rxhome"
mkdir -p "$RXHOME/.aria2"
printf 'insecure\n' > "$RXHOME/.curlrc"
printf 'check_certificate = off\ntries = 1\n' > "$RXHOME/.wgetrc"
printf 'check-certificate=false\nmax-tries=1\n' > "$RXHOME/.aria2/aria2.conf"

# Esegue in $3 il comando stampato per lo strumento $1 nella tabella in $2, così come lo incolla
# il ricevente. Lo stderr resta in $TMP/rx.err, mostrato da fail.
run_printed() {
	local line
	line="$(printed "$1" "$2")"
	[[ -n "$line" ]] || fail "riga $1 mancante in $2"
	(cd "$3" && HOME="$RXHOME" XDG_CONFIG_HOME="$RXHOME/.config" bash -c "$line" > /dev/null 2> "$TMP/rx.err")
}

# RS-1: upload da file, nome dal path.
curl -N -sS "${UP_AUTH[@]}" -T "$TMP/payload.bin" "$BASE/e2e.bin" > "$TMP/upload.out" 2> "$TMP/upload.err" &
UP_PID=$!
UP_PIDS+=("$UP_PID")
SLUG="$(wait_slug "$TMP/upload.out")"
[[ -n "$SLUG" ]] || fail "nessuno slug entro 10 s (upload.out: $(cat "$TMP/upload.out"))"
check_commands "$TMP/upload.out" "$BASE" "$SLUG"

# Ripresa (curl -C -): 400 senza reclamare lo slug; il download completo successivo riesce.
printf 'parziale' > "$TMP/received.bin"
CODE="$(curl -s -C - -o "$TMP/received.bin" -w '%{http_code}' "$BASE/$SLUG" || true)"
[[ "$CODE" == "400" ]] || fail "ripresa con curl -C -: $CODE, atteso 400"
rm -f "$TMP/received.bin"
# In modalità esterna i comandi devono usare la base del test (BASHT_PUBLIC_URL == BASHT_E2E_BASE).
if [[ -n "$EXTERNAL" ]]; then
	grep -qF "\"$BASE/" "$TMP/upload.out" || fail "comandi con base diversa da $BASE: $(cat "$TMP/upload.out")"
fi

# Browser e anteprime (chat, posta) vedono la pagina di download, che non consuma il link: il
# download successivo riesce.
CODE="$(curl -s -H 'Accept: text/html' -o "$TMP/landing.html" -w '%{http_code}' "$BASE/$SLUG")"
[[ "$CODE" == "200" ]] || fail "pagina di download: $CODE, atteso 200"
grep -qF 'e2e.bin' "$TMP/landing.html" || fail "pagina di download senza il nome del file"
grep -q '<form[^>]*method="post"' "$TMP/landing.html" || fail "pagina di download senza il pulsante Download"
CODE="$(curl -s -A 'Slackbot-LinkExpanding 1.0 (+https://api.slack.com/robots)' -o /dev/null -w '%{http_code}' "$BASE/$SLUG")"
[[ "$CODE" == "200" ]] || fail "anteprima di Slack: $CODE, atteso 200 (pagina)"

# Download con la prima riga stampata: il nome viene da Content-Disposition.
mkdir -p "$TMP/rs1"
run_printed curl "$TMP/upload.out" "$TMP/rs1" || fail "download RS-1 fallito"
RECV_SUM="$(sha256sum "$TMP/rs1/e2e.bin" | awk '{print $1}')"
[[ "$RECV_SUM" == "$PAYLOAD_SUM" ]] || fail "sha256 RS-1 diverso"
wait "$UP_PID" || fail "upload RS-1 terminato con errore"
# Esito finale per il mittente.
grep -qF "OK: download completato, $((MB * 1048576)) byte" "$TMP/upload.out" ||
	fail "RS-1: esito OK mancante per il mittente: $(tail -n 2 "$TMP/upload.out")"

CODE="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/$SLUG")"
[[ "$CODE" == "410" ]] || fail "secondo download RS-1: $CODE, atteso 410"

# Link già usato, con i comandi stampati, in una cartella con un file omonimo (per esempio quello
# appena inviato, se il ricevente lavora nella cartella del mittente): il file resta intatto, non
# compaiono altri file e il comando esce con errore.
mkdir -p "$TMP/k"
printf 'ORIGINALE' > "$TMP/k/e2e.bin"
for tool in curl wget aria2c; do
	command -v "$tool" > /dev/null || continue
	if run_printed "$tool" "$TMP/upload.out" "$TMP/k"; then fail "$tool su link usato: uscita 0"; fi
done
[[ "$(cat "$TMP/k/e2e.bin")" == "ORIGINALE" ]] || fail "un comando su link usato ha sovrascritto il file"
# curl 8.5 (non la 8.18) crea il file di Content-Disposition (bashtransfer.txt) prima che -f fermi il
# download: resta vuoto.
if [[ -f "$TMP/k/bashtransfer.txt" && ! -s "$TMP/k/bashtransfer.txt" ]]; then rm "$TMP/k/bashtransfer.txt"; fi
[[ "$(ls -A "$TMP/k")" == "e2e.bin" ]] || fail "file inattesi dopo i comandi su link usato: $(ls -A "$TMP/k")"

# Link valido, ricevente nella cartella del mittente con il comando stampato: il file omonimo è
# proprio quello in invio. curl -OJ rifiuta di sovrascriverlo; wget e aria2c salvano la copia con
# un altro nome (same.bin.1, same.1.bin).
for tool in curl wget aria2c; do
	command -v "$tool" > /dev/null || continue
	rm -rf "$TMP/same"
	mkdir -p "$TMP/same"
	cp "$TMP/payload.bin" "$TMP/same/same.bin"
	(cd "$TMP/same" && curl -N -sS "${UP_AUTH[@]}" -T same.bin "$BASE/same.bin" > "$TMP/same.out" 2> "$TMP/same.err") &
	SAME_PID=$!
	UP_PIDS+=("$SAME_PID")
	SAME_SLUG="$(wait_slug "$TMP/same.out")"
	[[ -n "$SAME_SLUG" ]] || fail "stessa cartella ($tool): nessuno slug entro 10 s"
	if run_printed "$tool" "$TMP/same.out" "$TMP/same"; then rc=0; else rc=$?; fi
	wait "$SAME_PID" || true
	cmp -s "$TMP/same/same.bin" "$TMP/payload.bin" || fail "$tool nella cartella del mittente ha modificato il file in invio"
	if [[ "$tool" == curl ]]; then
		[[ "$rc" != 0 ]] || fail "curl -OJ sul file in invio: uscita 0"
		[[ "$(ls -A "$TMP/same")" == "same.bin" ]] || fail "curl -OJ sul file in invio: file inattesi: $(ls -A "$TMP/same")"
	else
		[[ "$rc" == 0 ]] || fail "$tool nella cartella del mittente: uscita $rc"
		[[ "$(find "$TMP/same" -type f | wc -l)" == 2 ]] || fail "$tool nella cartella del mittente: file: $(ls -A "$TMP/same")"
		for f in "$TMP/same"/*; do
			cmp -s "$f" "$TMP/payload.bin" || fail "$tool nella cartella del mittente: $(basename "$f") diverso"
		done
	fi
done

CODE="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/ABCDEFGHIJ")"
[[ "$CODE" == "404" ]] || fail "slug sconosciuto: $CODE, atteso 404"

# RS-2: upload da stdin, lunghezza ignota, nome dal path.
cat "$TMP/payload.bin" | curl -N -sS "${UP_AUTH[@]}" -T - "$BASE/mydir.tgz" > "$TMP/upload2.out" 2> "$TMP/upload2.err" &
UP2_PID=$!
UP_PIDS+=("$UP2_PID")
SLUG2="$(wait_slug "$TMP/upload2.out")"
[[ -n "$SLUG2" ]] || fail "RS-2: nessuno slug entro 10 s"
check_commands "$TMP/upload2.out" "$BASE" "$SLUG2"

# Download con la prima riga stampata (curl -f -OJ): il nome viene dall'header Content-Disposition.
mkdir -p "$TMP/rs2"
run_printed curl "$TMP/upload2.out" "$TMP/rs2" || fail "download RS-2 fallito"
RECV2_SUM="$(sha256sum "$TMP/rs2/mydir.tgz" | awk '{print $1}')"
[[ "$RECV2_SUM" == "$PAYLOAD_SUM" ]] || fail "sha256 RS-2 diverso"
wait "$UP2_PID" || fail "upload RS-2 terminato con errore"
# Esito finale anche per la pipe, di lunghezza ignota.
grep -qF "OK: download completato, $((MB * 1048576)) byte" "$TMP/upload2.out" ||
	fail "RS-2: esito OK mancante per il mittente: $(tail -n 2 "$TMP/upload2.out")"

# Nome non ASCII: curl -OJ legge filename (byte UTF-8), non filename*.
curl -N -sS "${UP_AUTH[@]}" -T "$TMP/payload.bin" "$BASE/citt%C3%A0%20%C3%A8.bin" > "$TMP/utf8.out" 2> "$TMP/utf8.err" &
UTF8_PID=$!
UP_PIDS+=("$UTF8_PID")
UTF8_SLUG="$(wait_slug "$TMP/utf8.out")"
[[ -n "$UTF8_SLUG" ]] || fail "nome UTF-8: nessuno slug entro 10 s"
mkdir -p "$TMP/utf8"
run_printed curl "$TMP/utf8.out" "$TMP/utf8" || fail "download con nome UTF-8 fallito"
cmp -s "$TMP/utf8/città è.bin" "$TMP/payload.bin" || fail "curl -OJ: nome UTF-8 non rispettato: $(ls -A "$TMP/utf8")"
wait "$UTF8_PID" || fail "upload con nome UTF-8 terminato con errore"

# RS-3: download con wget, come nella seconda riga stampata.
if command -v wget > /dev/null; then
	curl -N -sS "${UP_AUTH[@]}" -T "$TMP/payload.bin" "$BASE/w.bin" > "$TMP/upload3.out" 2> "$TMP/upload3.err" &
	UP3_PID=$!
	UP_PIDS+=("$UP3_PID")
	SLUG3="$(wait_slug "$TMP/upload3.out")"
	[[ -n "$SLUG3" ]] || fail "RS-3: nessuno slug entro 10 s"
	mkdir -p "$TMP/rs3"
	run_printed wget "$TMP/upload3.out" "$TMP/rs3" || fail "download wget fallito"
	cmp -s "$TMP/rs3/w.bin" "$TMP/payload.bin" || fail "download wget: contenuto o nome diverso: $(ls -A "$TMP/rs3")"
	wait "$UP3_PID" || fail "upload RS-3 terminato con errore"
fi

# RS-3b: download con aria2c, come nella terza riga stampata (solo se installato).
if command -v aria2c > /dev/null; then
	cat "$TMP/payload.bin" | curl -N -sS "${UP_AUTH[@]}" -T - "$BASE/a.bin" > "$TMP/upload3b.out" 2> "$TMP/upload3b.err" &
	UP3B_PID=$!
	UP_PIDS+=("$UP3B_PID")
	SLUG3B="$(wait_slug "$TMP/upload3b.out")"
	[[ -n "$SLUG3B" ]] || fail "RS-3b: nessuno slug entro 10 s"
	mkdir -p "$TMP/rs3b"
	run_printed aria2c "$TMP/upload3b.out" "$TMP/rs3b" || fail "download aria2c fallito"
	cmp -s "$TMP/rs3b/a.bin" "$TMP/payload.bin" || fail "download aria2c: contenuto o nome diverso: $(ls -A "$TMP/rs3b")"
	wait "$UP3B_PID" || fail "upload RS-3b terminato con errore"
fi

# RS-4: il mittente cade a metà. Il ricevente deve vedere un errore, non un file troncato con esito 0.
mkfifo "$TMP/abort.fifo"
curl -N -sS "${UP_AUTH[@]}" -T - "$BASE/abort.bin" < "$TMP/abort.fifo" > "$TMP/upload4.out" 2> "$TMP/upload4.err" &
UP4_PID=$!
UP_PIDS+=("$UP4_PID")
exec 3> "$TMP/abort.fifo"
# curl non invia la richiesta finché stdin non ha dati: il primo blocco parte subito, il pipe resta aperto.
head -c 1048576 /dev/urandom >&3 &
UP_PIDS+=($!)
SLUG4="$(wait_slug "$TMP/upload4.out")"
[[ -n "$SLUG4" ]] || fail "RS-4: nessuno slug entro 10 s"
curl -sS -o "$TMP/abort.dl" "$BASE/$SLUG4" 2> "$TMP/abort.dl.err" &
DL4_PID=$!
UP_PIDS+=("$DL4_PID")
sleep 1
kill "$UP4_PID" 2>/dev/null || true
exec 3>&-
if timeout 30 tail --pid="$DL4_PID" -f /dev/null; then
	if wait "$DL4_PID"; then fail "RS-4: download riuscito con mittente caduto"; fi
else
	fail "RS-4: il ricevente non termina entro 30 s dopo la caduta del mittente"
fi

# RS-5: il ricevente cade a metà. Il mittente deve terminare con errore.
head -c $((64 * 1048576)) /dev/zero > "$TMP/zero.bin"
curl -N -sS "${UP_AUTH[@]}" -T "$TMP/zero.bin" "$BASE/rabort.bin" > "$TMP/upload5.out" 2> "$TMP/upload5.err" &
UP5_PID=$!
UP_PIDS+=("$UP5_PID")
SLUG5="$(wait_slug "$TMP/upload5.out")"
[[ -n "$SLUG5" ]] || fail "RS-5: nessuno slug entro 10 s"
curl -s --max-time 1 --limit-rate 1M -o /dev/null "$BASE/$SLUG5" || true
if timeout 30 tail --pid="$UP5_PID" -f /dev/null; then
	if wait "$UP5_PID"; then fail "RS-5: upload riuscito con ricevente caduto"; fi
else
	fail "RS-5: il mittente non termina entro 30 s dopo la caduta del ricevente"
fi
grep -qF "ERRORE: download NON completato dopo " "$TMP/upload5.out" ||
	fail "RS-5: esito ERRORE mancante per il mittente: $(tail -n 2 "$TMP/upload5.out")"
rm -f "$TMP/zero.bin"

head -c 1048576 /dev/urandom > "$TMP/par.bin"

# RS-7: il mittente cade mentre attende il ricevente, con il body non ancora letto. Lo slug si
# libera in pochi secondi (410) invece di restare occupato fino a BASHT_WAIT_TIMEOUT. Mittente
# in Python: dichiara 1 MiB, ne invia 1000 byte, legge i comandi e chiude. Con curl e una pipe il
# momento dell'invio dipende dal buffer di stdin di curl.
SLUG7="$(python3 - "$BASE" "${UP_AUTH[1]:-}" <<'PY'
import base64, re, socket, ssl, sys, time
from urllib.parse import urlsplit
u = urlsplit(sys.argv[1])
port = u.port or (443 if u.scheme == "https" else 80)
s = socket.create_connection((u.hostname, port), timeout=10)
if u.scheme == "https":
    ctx = ssl.create_default_context()
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    s = ctx.wrap_socket(s, server_hostname=u.hostname)
auth = ""
if sys.argv[2]:
    auth = "Authorization: Basic " + base64.b64encode(sys.argv[2].encode()).decode() + "\r\n"
s.sendall(("PUT /wait.bin HTTP/1.1\r\nHost: " + u.netloc + "\r\n" + auth +
           "Content-Length: 1048576\r\n\r\n").encode() + b"x" * 1000)
buf = b""
deadline = time.time() + 10
while b"| aria2c " not in buf and time.time() < deadline:
    chunk = s.recv(4096)
    if not chunk:
        break
    buf += chunk
s.close()
m = re.search(rb'/([0-9A-Za-z]{10})"', buf)
print(m.group(1).decode() if m else "")
PY
)"
[[ -n "$SLUG7" ]] || fail "RS-7: nessuno slug"
# Una sola GET dopo l'attesa: senza rilevamento reclamerebbe lo slug, quindi una GET ripetuta
# non proverebbe nulla.
sleep 3
CODE="$(curl -s --max-time 5 -o /dev/null -w '%{http_code}' "$BASE/$SLUG7" || true)"
[[ "$CODE" == "410" ]] || fail "RS-7: slug del mittente caduto: $CODE, atteso 410 entro 3 s"

# RS-6: trasferimenti paralleli, ciascuno con il proprio slug e contenuto integro.
PAR="${BASHT_E2E_PARALLEL:-8}"
PAR_PIDS=()
for i in $(seq 1 "$PAR"); do
	(
		curl -N -sS "${UP_AUTH[@]}" -T "$TMP/par.bin" "$BASE/p$i.bin" > "$TMP/par$i.out" 2>&1 &
		up=$!
		s="$(wait_slug "$TMP/par$i.out")"
		[[ -n "$s" ]] && curl -sS -o "$TMP/par$i.dl" "$BASE/$s" && wait "$up" && cmp -s "$TMP/par.bin" "$TMP/par$i.dl"
	) &
	PAR_PIDS+=($!)
done
for i in "${!PAR_PIDS[@]}"; do
	wait "${PAR_PIDS[$i]}" || fail "RS-6: trasferimento parallelo $((i + 1)) fallito"
done

# RS-8: invio dal browser come lo fa la pagina, rifatto con curl. La prenotazione (POST / in JSON)
# risponde con uno stream NDJSON: link e token, poi attached, progress e l'esito. Il body va con PUT
# e il token; il ricevente usa il pulsante Download della pagina (POST /<slug>).
WEB_SIZE=$((MB * 1048576))
curl -sS -N "${UP_AUTH[@]}" -H 'Content-Type: application/json' -d "{\"name\":\"web upload.bin\",\"size\":$WEB_SIZE}" \
	"$BASE/" > "$TMP/web.ndjson" 2> "$TMP/web.err" &
WEB_STREAM_PID=$!
UP_PIDS+=("$WEB_STREAM_PID")
for _ in $(seq 1 100); do
	[[ -s "$TMP/web.ndjson" ]] && break
	sleep 0.1
done
read -r WEB_SLUG WEB_TOKEN < <(python3 -c 'import json,sys; d=json.loads(open(sys.argv[1]).readline()); print(d["slug"], d["token"])' "$TMP/web.ndjson") ||
	fail "RS-8: prenotazione senza link: $(cat "$TMP/web.ndjson" "$TMP/web.err")"
curl -sS -H "X-Bashtransfer-Token: $WEB_TOKEN" -T "$TMP/payload.bin" "$BASE/$WEB_SLUG" > "$TMP/web.put" 2>&1 &
WEB_PUT_PID=$!
UP_PIDS+=("$WEB_PUT_PID")
mkdir -p "$TMP/rs8"
(cd "$TMP/rs8" && curl -sS -f -OJ -X POST "$BASE/$WEB_SLUG") || fail "RS-8: download POST fallito"
cmp -s "$TMP/rs8/web upload.bin" "$TMP/payload.bin" || fail "RS-8: contenuto o nome diverso: $(ls -A "$TMP/rs8")"
wait "$WEB_STREAM_PID" || fail "RS-8: stream della prenotazione terminato con errore"
wait "$WEB_PUT_PID" || fail "RS-8: PUT terminato con errore"
grep -qx '{"event":"attached"}' "$TMP/web.ndjson" || fail "RS-8: evento attached mancante: $(cat "$TMP/web.ndjson")"
tail -n 1 "$TMP/web.ndjson" | grep -q "^{\"outcome\":\"ok\",\"bytes\":$WEB_SIZE," ||
	fail "RS-8: esito dello stream: $(tail -n 1 "$TMP/web.ndjson")"
tail -n 1 "$TMP/web.put" | grep -q '^{"outcome":"ok"' || fail "RS-8: esito del PUT: $(tail -n 2 "$TMP/web.put")"

# Annullamento dalla pagina: DELETE col token chiude subito il link, anche con lo stream aperto.
curl -sS -N "${UP_AUTH[@]}" -H 'Content-Type: application/json' -d '{"name":"x.bin","size":1}' \
	"$BASE/" > "$TMP/cancel.ndjson" 2>&1 &
CANCEL_PID=$!
UP_PIDS+=("$CANCEL_PID")
for _ in $(seq 1 100); do
	[[ -s "$TMP/cancel.ndjson" ]] && break
	sleep 0.1
done
read -r C_SLUG C_TOKEN < <(python3 -c 'import json,sys; d=json.loads(open(sys.argv[1]).readline()); print(d["slug"], d["token"])' "$TMP/cancel.ndjson") ||
	fail "annullamento: prenotazione senza link: $(cat "$TMP/cancel.ndjson")"
CODE="$(curl -s -X DELETE -H "X-Bashtransfer-Token: $C_TOKEN" -o /dev/null -w '%{http_code}' "$BASE/$C_SLUG")"
[[ "$CODE" == "204" ]] || fail "annullamento: DELETE $CODE, atteso 204"
CODE="$(curl -s -H 'Accept: text/html' -o /dev/null -w '%{http_code}' "$BASE/$C_SLUG")"
[[ "$CODE" == "410" ]] || fail "annullamento: link $CODE, atteso 410"
wait "$CANCEL_PID" || true
tail -n 1 "$TMP/cancel.ndjson" | grep -q '^{"outcome":"sender_gone"' || fail "annullamento: esito dello stream: $(tail -n 1 "$TMP/cancel.ndjson")"

if [[ -z "$EXTERNAL" || -n "$AUTH_CRED" ]]; then
# Auth (D9): solo PUT è protetto; senza credenziali o con password sbagliata: 401.
printf 'auth' > "$TMP/small.bin"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -T "$TMP/small.bin" "$AUTH_BASE/auth.bin")"
[[ "$CODE" == "401" ]] || fail "PUT senza credenziali: $CODE, atteso 401"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -u "${AUTH_CRED%%:*}:sbagliata" -T "$TMP/small.bin" "$AUTH_BASE/auth.bin")"
[[ "$CODE" == "401" ]] || fail "PUT con password errata: $CODE, atteso 401"

# Upload autorizzato con nome con spazi (D5): i comandi usano il nome sanificato.
curl -N -sS -u "$AUTH_CRED" -T "$TMP/small.bin" "$AUTH_BASE/strano%20nome.zip" > "$TMP/auth.out" 2> "$TMP/auth.err" &
AUTH_UP_PID=$!
UP_PIDS+=("$AUTH_UP_PID")
AUTH_SLUG="$(wait_slug "$TMP/auth.out")"
[[ -n "$AUTH_SLUG" ]] || fail "auth: nessuno slug entro 10 s (auth.out: $(cat "$TMP/auth.out"))"
check_commands "$TMP/auth.out" "$AUTH_BASE" "$AUTH_SLUG"

# Il download resta pubblico: nessuna credenziale richiesta.
mkdir -p "$TMP/authdl"
run_printed curl "$TMP/auth.out" "$TMP/authdl" || fail "download pubblico con auth fallito"
cmp -s "$TMP/authdl/strano nome.zip" "$TMP/small.bin" || fail "download con auth: contenuto o nome diverso: $(ls -A "$TMP/authdl")"
wait "$AUTH_UP_PID" || fail "upload autorizzato terminato con errore"
fi

# Invariante: nessun comando di ricezione o di invio, né una sua variante sbagliata, altera la
# sorgente (solo con il server locale: lo script avvia istanze con configurazioni proprie).
if [[ -z "$EXTERNAL" ]]; then
	python3 "$ROOT/scripts/invariant.py" "$TMP/bashtransfer" || fail "invariante della sorgente violata"
fi

echo "E2E OK"
