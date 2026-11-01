#!/usr/bin/env bash
# Frontend tests with Playwright (scripts/ui/ui.mjs) against the freshly built binary.
#   bash scripts/ui/run.sh          direct servers on 127.0.0.1 (HTTP/1.1)
#   bash scripts/ui/run.sh apache   through Apache 2.4 in Docker, h2c proxy as in README
# Each mode starts four servers: long wait, BASHT_WAIT_TIMEOUT=3s, Basic auth (u:p) and
# BASHT_SENDER_GRACE=8 (grace for a disconnected browser sender).
# First setup, from scripts/ui: npm ci && npx playwright-core install chromium
# (without Chromium system libraries: install --with-deps chromium).
# BASHT_UI_OUT: parent directory for preserved files/screenshots; otherwise the directory is
# temporary and removed if all tests pass. Other variables pass through to ui.mjs (ONLY, CHROME).
set -euo pipefail

MODE="${1:-direct}"
case "$MODE" in direct | apache) ;; *) echo "usage: $0 [direct|apache]" >&2; exit 2 ;; esac

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
# Image pinned by digest (2.4.69); container names match httpd.conf.
HTTPD_IMAGE="httpd:2.4.69@sha256:d3accb516506c05ae0068bb54ba6798a10b249b64db91268fca692326cb02b4b"
CONTAINERS=(bt-ui-apache bt-ui-app bt-ui-app-short bt-ui-app-auth bt-ui-app-grace)
NET=bt-ui-net

# Local servers: no proxy for curl, Node or Chromium.
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
	echo "UI FAIL: no response from $1" >&2
	return 1
}

free_port() {
	python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'
}

# Static binary: runs in a container in Apache mode.
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
	# Use Apache's image as the app base too: mount the static binary read-only.
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
		for c in "${CONTAINERS[@]}"; do echo "--- $c ---" >&2; docker logs --tail 200 "$c" >&2 2>&1; done
	fi
	if [[ -z "${BASHT_UI_OUT:-}" ]]; then
		cp -r "$OUT" "${TMPDIR:-/tmp}/bashtransfer-ui-$MODE-$$"
		echo "files and screenshots: ${TMPDIR:-/tmp}/bashtransfer-ui-$MODE-$$" >&2
	fi
fi
if [[ -n "${BASHT_UI_OUT:-}" ]]; then echo "files and screenshots: $OUT"; fi
exit $status
