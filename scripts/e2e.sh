#!/usr/bin/env bash
# Test end-to-end locale: RS-1 e RS-2 contro il binario appena compilato su 127.0.0.1.
# Tutti i file temporanei stanno in una directory creata qui e rimossa all'uscita.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
SERVER_PID=""
UP_PIDS=()

cleanup() {
	for pid in "${UP_PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
	if [[ -n "$SERVER_PID" ]]; then kill "$SERVER_PID" 2>/dev/null || true; fi
	rm -rf "$TMP"
}
trap cleanup EXIT

fail() {
	echo "E2E FAIL: $*" >&2
	if [[ -f "$TMP/server.log" ]]; then echo "--- server.log ---" >&2; cat "$TMP/server.log" >&2; fi
	exit 1
}

PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')"
BASE="http://127.0.0.1:$PORT"

head -c 8388608 /dev/urandom > "$TMP/payload.bin"
PAYLOAD_SUM="$(sha256sum "$TMP/payload.bin" | awk '{print $1}')"

(cd "$ROOT" && go build -o "$TMP/bashtransfer" ./cmd/bashtransfer)

BASHT_LISTEN="127.0.0.1:$PORT" "$TMP/bashtransfer" > "$TMP/server.log" 2>&1 &
SERVER_PID=$!

for _ in $(seq 1 100); do
	if curl -sf -o /dev/null "$BASE/"; then break; fi
	sleep 0.1
done
curl -sf -o /dev/null "$BASE/" || fail "il server non risponde su $BASE"

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

echo "E2E OK"
