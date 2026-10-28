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

# Attende la prima riga di comandi di un upload e ne estrae lo slug.
wait_slug() {
	local out="$1"
	for _ in $(seq 1 100); do
		if grep -q '^curl -o' "$out" 2>/dev/null; then break; fi
		sleep 0.1
	done
	sed -n 's#^curl -o "[^"]*" ".*/\([0-9A-Za-z]\{10\}\)"$#\1#p' "$out" | head -n 1
}

# RS-1: upload da file, nome dal path.
curl -N -sS "${UP_AUTH[@]}" -T "$TMP/payload.bin" "$BASE/e2e.bin" > "$TMP/upload.out" 2> "$TMP/upload.err" &
UP_PID=$!
UP_PIDS+=("$UP_PID")
SLUG="$(wait_slug "$TMP/upload.out")"
[[ -n "$SLUG" ]] || fail "nessuno slug entro 10 s (upload.out: $(cat "$TMP/upload.out"))"
grep -q '^wget -O "e2e.bin" ' "$TMP/upload.out" || fail "riga wget mancante o nome errato"
# In modalità esterna i comandi devono usare la base del test (BASHT_PUBLIC_URL == BASHT_E2E_BASE).
if [[ -n "$EXTERNAL" ]]; then
	grep -qF "\"$BASE/" "$TMP/upload.out" || fail "comandi con base diversa da $BASE: $(cat "$TMP/upload.out")"
fi

curl -sS -o "$TMP/received.bin" "$BASE/$SLUG" || fail "download RS-1 fallito"
RECV_SUM="$(sha256sum "$TMP/received.bin" | awk '{print $1}')"
[[ "$RECV_SUM" == "$PAYLOAD_SUM" ]] || fail "sha256 RS-1 diverso"
wait "$UP_PID" || fail "upload RS-1 terminato con errore"

CODE="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/$SLUG")"
[[ "$CODE" == "410" ]] || fail "secondo download RS-1: $CODE, atteso 410"

CODE="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/ABCDEFGHIJ")"
[[ "$CODE" == "404" ]] || fail "slug sconosciuto: $CODE, atteso 404"

# RS-2: upload da stdin, lunghezza ignota, nome dal path.
cat "$TMP/payload.bin" | curl -N -sS "${UP_AUTH[@]}" -T - "$BASE/mydir.tgz" > "$TMP/upload2.out" 2> "$TMP/upload2.err" &
UP2_PID=$!
UP_PIDS+=("$UP2_PID")
SLUG2="$(wait_slug "$TMP/upload2.out")"
[[ -n "$SLUG2" ]] || fail "RS-2: nessuno slug entro 10 s"
grep -q '^curl -o "mydir.tgz" ' "$TMP/upload2.out" || fail "RS-2: nome mydir.tgz mancante nei comandi"

curl -sS -o "$TMP/mydir.tgz" "$BASE/$SLUG2" || fail "download RS-2 fallito"
RECV2_SUM="$(sha256sum "$TMP/mydir.tgz" | awk '{print $1}')"
[[ "$RECV2_SUM" == "$PAYLOAD_SUM" ]] || fail "sha256 RS-2 diverso"
wait "$UP2_PID" || fail "upload RS-2 terminato con errore"

# RS-3: download con wget, come nella seconda riga stampata.
if command -v wget > /dev/null; then
	curl -N -sS "${UP_AUTH[@]}" -T "$TMP/payload.bin" "$BASE/w.bin" > "$TMP/upload3.out" 2> "$TMP/upload3.err" &
	UP3_PID=$!
	UP_PIDS+=("$UP3_PID")
	SLUG3="$(wait_slug "$TMP/upload3.out")"
	[[ -n "$SLUG3" ]] || fail "RS-3: nessuno slug entro 10 s"
	wget -q --no-check-certificate --tries=1 -O "$TMP/w.bin" "$BASE/$SLUG3" || fail "download wget fallito"
	cmp -s "$TMP/w.bin" "$TMP/payload.bin" || fail "download wget: contenuto diverso"
	wait "$UP3_PID" || fail "upload RS-3 terminato con errore"
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
while b"wget -O" not in buf and time.time() < deadline:
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
grep -q '^curl -o "strano nome.zip" ' "$TMP/auth.out" || fail "auth: nome con spazi mancante nel comando curl"
grep -q '^wget -O "strano nome.zip" ' "$TMP/auth.out" || fail "auth: nome con spazi mancante nel comando wget"

# Il download resta pubblico: nessuna credenziale richiesta.
curl -sS -o "$TMP/auth.dl" "$AUTH_BASE/$AUTH_SLUG" || fail "download pubblico con auth fallito"
cmp -s "$TMP/auth.dl" "$TMP/small.bin" || fail "download con auth: contenuto diverso"
wait "$AUTH_UP_PID" || fail "upload autorizzato terminato con errore"
fi

echo "E2E OK"
