#!/usr/bin/env bash
# Test frontend con Playwright (scripts/ui/ui.mjs) contro il binario appena compilato.
#   bash scripts/ui/run.sh          server diretti su 127.0.0.1 (HTTP/1.1)
#   bash scripts/ui/run.sh apache   attraverso Apache 2.4 in Docker, proxy h2c come nel README
# Ogni modo avvia quattro server: attesa lunga, BASHT_WAIT_TIMEOUT=3s, Basic auth (u:p) e
# BASHT_SENDER_GRACE=8 (tolleranza per il mittente dal browser caduto).
# Prima volta, da scripts/ui: npm ci && npx playwright-core install chromium
# (su un sistema senza le librerie di Chromium: install --with-deps chromium).
# BASHT_UI_OUT: cartella in cui creare quella dei file e degli screenshot, che resta; senza, è
# temporanea e rimossa se tutti i test passano. Le altre variabili sono passate a ui.mjs (ONLY, CHROME).
set -euo pipefail

MODE="${1:-direct}"
case "$MODE" in direct | apache) ;; *) echo "uso: $0 [direct|apache]" >&2; exit 2 ;; esac

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$DIR/../.." && pwd)"
TMP="$(mktemp -d)"
if [[ -n "${BASHT_UI_OUT:-}" ]]; then
	mkdir -p "$BASHT_UI_OUT"
	OUT="$(mktemp -d "$BASHT_UI_OUT/$MODE.XXXXXX")"
else
	OUT="$TMP/out"
fi
PIDS=()
# Immagine fissata per digest (2.4.69); i nomi dei container sono quelli di httpd.conf.
HTTPD_IMAGE="httpd:2.4.69@sha256:d3accb516506c05ae0068bb54ba6798a10b249b64db91268fca692326cb02b4b"
CONTAINERS=(bt-ui-apache bt-ui-app bt-ui-app-short bt-ui-app-auth bt-ui-app-grace)
NET=bt-ui-net

# I server sono locali: nessun proxy per curl, node e Chromium.
unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY all_proxy ALL_PROXY

cleanup() {
	for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
	if [[ "$MODE" == apache ]]; then
		docker rm -f "${CONTAINERS[@]}" >/dev/null 2>&1 || true
		docker network rm "$NET" >/dev/null 2>&1 || true
	fi
	rm -rf "$TMP"
}
trap cleanup EXIT

wait_up() {
	for _ in $(seq 1 100); do
		if curl -sf -o /dev/null "$1/"; then return 0; fi
		sleep 0.1
	done
	echo "UI FAIL: nessuna risposta da $1" >&2
	return 1
}

free_port() {
	python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'
}

# Statica: nel modo apache gira in un container.
(cd "$ROOT" && CGO_ENABLED=0 go build -o "$TMP/bashtransfer" ./cmd/bashtransfer)

if [[ "$MODE" == direct ]]; then
	bases=()
	for i in 0 1 2 3; do
		port="$(free_port)"
		case $i in
		0) envs=() ;;
		1) envs=(BASHT_WAIT_TIMEOUT=3s) ;;
		2) envs=(BASHT_AUTH_USER=u BASHT_AUTH_PASSWORD=p) ;;
		3) envs=(BASHT_SENDER_GRACE=8) ;;
		esac
		env "${envs[@]}" BASHT_LISTEN="127.0.0.1:$port" BASHT_LOG_LEVEL=debug \
			"$TMP/bashtransfer" > "$TMP/server-$i.log" 2>&1 &
		PIDS+=($!)
		bases+=("http://127.0.0.1:$port")
	done
else
	docker rm -f "${CONTAINERS[@]}" >/dev/null 2>&1 || true
	docker network rm "$NET" >/dev/null 2>&1 || true
	docker network create "$NET" >/dev/null
	# L'immagine di Apache fa da base anche per l'app: il binario statico è montato in sola lettura.
	app() {
		docker run -d --name "$1" --network "$NET" --entrypoint /bashtransfer \
			-v "$TMP/bashtransfer:/bashtransfer:ro" -e BASHT_LISTEN=:8080 -e BASHT_LOG_LEVEL=debug \
			"${@:2}" "$HTTPD_IMAGE" > /dev/null
	}
	app bt-ui-app
	app bt-ui-app-short -e BASHT_WAIT_TIMEOUT=3s
	app bt-ui-app-auth -e BASHT_AUTH_USER=u -e BASHT_AUTH_PASSWORD=p
	app bt-ui-app-grace -e BASHT_SENDER_GRACE=8
	docker run -d --name bt-ui-apache --network "$NET" \
		-v "$DIR/httpd.conf:/usr/local/apache2/conf/httpd.conf:ro" \
		-p 127.0.0.1::80 -p 127.0.0.1::81 -p 127.0.0.1::82 -p 127.0.0.1::83 "$HTTPD_IMAGE" > /dev/null
	bases=()
	for p in 80 81 82 83; do
		bases+=("http://$(docker port bt-ui-apache "$p/tcp" | head -n 1)")
	done
fi

for b in "${bases[@]}"; do wait_up "$b"; done

status=0
(cd "$DIR" && BASE="${bases[0]}" BASE_SHORT="${bases[1]}" BASE_AUTH="${bases[2]}" BASE_GRACE="${bases[3]}" OUT="$OUT" TAG="$MODE" \
	timeout 600 node ui.mjs) || status=$?

if [[ $status -ne 0 ]]; then
	if [[ "$MODE" == direct ]]; then
		for i in 0 1 2 3; do echo "--- server-$i.log ---" >&2; tail -n 40 "$TMP/server-$i.log" >&2; done
	else
		for c in "${CONTAINERS[@]}"; do echo "--- $c ---" >&2; docker logs --tail 40 "$c" >&2 2>&1; done
	fi
	if [[ -z "${BASHT_UI_OUT:-}" ]]; then
		cp -r "$OUT" "${TMPDIR:-/tmp}/bashtransfer-ui-$MODE-$$"
		echo "file e screenshot: ${TMPDIR:-/tmp}/bashtransfer-ui-$MODE-$$" >&2
	fi
fi
if [[ -n "${BASHT_UI_OUT:-}" ]]; then echo "file e screenshot: $OUT"; fi
exit $status
