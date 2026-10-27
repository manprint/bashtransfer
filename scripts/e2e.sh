#!/usr/bin/env bash
# Test end-to-end locale: RS-1 e RS-2 contro il binario appena compilato su 127.0.0.1.
# Tutti i file temporanei stanno in una directory creata qui e rimossa all'uscita.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
SERVER_PID=""
AUTH_PID=""
UP_PIDS=()

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
curl -N -sS -T "$TMP/payload.bin" "$BASE/e2e.bin" > "$TMP/upload.out" 2> "$TMP/upload.err" &
UP_PID=$!
UP_PIDS+=("$UP_PID")
SLUG="$(wait_slug "$TMP/upload.out")"
[[ -n "$SLUG" ]] || fail "nessuno slug entro 10 s (upload.out: $(cat "$TMP/upload.out"))"
grep -q '^wget -O "e2e.bin" ' "$TMP/upload.out" || fail "riga wget mancante o nome errato"

curl -sS -o "$TMP/received.bin" "$BASE/$SLUG" || fail "download RS-1 fallito"
RECV_SUM="$(sha256sum "$TMP/received.bin" | awk '{print $1}')"
[[ "$RECV_SUM" == "$PAYLOAD_SUM" ]] || fail "sha256 RS-1 diverso"
wait "$UP_PID" || fail "upload RS-1 terminato con errore"

CODE="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/$SLUG")"
[[ "$CODE" == "410" ]] || fail "secondo download RS-1: $CODE, atteso 410"

CODE="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/ABCDEFGHIJ")"
[[ "$CODE" == "404" ]] || fail "slug sconosciuto: $CODE, atteso 404"

# RS-2: upload da stdin, lunghezza ignota, nome dal path.
cat "$TMP/payload.bin" | curl -N -sS -T - "$BASE/mydir.tgz" > "$TMP/upload2.out" 2> "$TMP/upload2.err" &
UP2_PID=$!
UP_PIDS+=("$UP2_PID")
SLUG2="$(wait_slug "$TMP/upload2.out")"
[[ -n "$SLUG2" ]] || fail "RS-2: nessuno slug entro 10 s"
grep -q '^curl -o "mydir.tgz" ' "$TMP/upload2.out" || fail "RS-2: nome mydir.tgz mancante nei comandi"

curl -sS -o "$TMP/mydir.tgz" "$BASE/$SLUG2" || fail "download RS-2 fallito"
RECV2_SUM="$(sha256sum "$TMP/mydir.tgz" | awk '{print $1}')"
[[ "$RECV2_SUM" == "$PAYLOAD_SUM" ]] || fail "sha256 RS-2 diverso"
wait "$UP2_PID" || fail "upload RS-2 terminato con errore"

if [[ -z "$EXTERNAL" ]]; then
# Auth (D9): solo PUT è protetto; senza credenziali o con password sbagliata: 401.
printf 'auth' > "$TMP/small.bin"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -T "$TMP/small.bin" "$AUTH_BASE/auth.bin")"
[[ "$CODE" == "401" ]] || fail "PUT senza credenziali: $CODE, atteso 401"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -u alice:sbagliata -T "$TMP/small.bin" "$AUTH_BASE/auth.bin")"
[[ "$CODE" == "401" ]] || fail "PUT con password errata: $CODE, atteso 401"

# Upload autorizzato con nome con spazi (D5): i comandi usano il nome sanificato.
curl -N -sS -u alice:s3cret -T "$TMP/small.bin" "$AUTH_BASE/strano%20nome.zip" > "$TMP/auth.out" 2> "$TMP/auth.err" &
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
