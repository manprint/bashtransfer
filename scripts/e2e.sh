#!/usr/bin/env bash
# Local end-to-end tests: RS-1 and RS-2 against the freshly built binary on 127.0.0.1.
# All temporary files live in a directory created here and removed on exit.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
SERVER_PID=""
AUTH_PID=""
UP_PIDS=()
# Credentials for every PUT to BASE (external mode with BASHT_E2E_AUTH only).
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
	if [[ -f "$TMP/rx.err" ]]; then echo "--- stderr of the last printed command ---" >&2; cat "$TMP/rx.err" >&2; fi
	if [[ -f "$TMP/server.log" ]]; then echo "--- server.log ---" >&2; cat "$TMP/server.log" >&2; fi
	exit 1
}

# BASHT_E2E_BASE: test an existing stack (e.g. nginx at https://127.0.0.1); accept the test
# certificate. BASHT_E2E_MB: payload size in MiB (default 8).
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
	curl -sf -o /dev/null "$BASE/" || fail "stack not responding at $BASE"
	# BASHT_E2E_AUTH=user:password: use Basic auth for every PUT and check the 401 responses.
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
	curl -sf -o /dev/null "$BASE/" || fail "server not responding at $BASE"

	# Second server with Basic auth (D9): same binary, different port.
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
	curl -sf -o /dev/null "$AUTH_BASE/" || fail "authenticated server not responding at $AUTH_BASE"
fi

# Wait for the curl table row ("| curl   | curl -f -OJ ... |") and extract its slug.
wait_slug() {
	local out="$1"
	for _ in $(seq 1 100); do
		if grep -q '^| curl ' "$out" 2>/dev/null; then break; fi
		sleep 0.1
	done
	sed -n 's#^| curl *| curl -f -OJ ".*/\([0-9A-Za-z]\{10\}\)" *|$#\1#p' "$out" | head -n 1
}

# Command printed for a tool (curl, wget, aria2c) in the table in $2.
printed() {
	sed -n "s#^| $1 *| \(.*[^ ]\) *|\$#\1#p" "$2" | head -n 1
}

# The three commands printed for URL $2/$3, without filenames (scripts/invariant.py),
# and the browser link (direct row).
check_commands() {
	local out="$1" u="$2/$3"
	[[ "$(printed direct "$out")" == "$u" ]] || fail "incorrect direct row: $(printed direct "$out")"
	[[ "$(printed curl "$out")" == "curl -f -OJ \"$u\"" ]] || fail "incorrect curl row: $(printed curl "$out")"
	[[ "$(printed wget "$out")" == "wget --content-disposition \"$u\"" ]] || fail "incorrect wget row: $(printed wget "$out")"
	[[ "$(printed aria2c "$out")" == "aria2c -x1 -s1 --allow-overwrite=false \"$u\"" ]] ||
		fail "incorrect aria2c row: $(printed aria2c "$out")"
}

# Receiver HOME: user configuration (.curlrc, .wgetrc, aria2.conf) cannot change the
# printed commands' results; accept only the test certificate and use a single attempt.
RXHOME="$TMP/rxhome"
mkdir -p "$RXHOME/.aria2"
printf 'insecure\n' > "$RXHOME/.curlrc"
printf 'check_certificate = off\ntries = 1\n' > "$RXHOME/.wgetrc"
printf 'check-certificate=false\nmax-tries=1\n' > "$RXHOME/.aria2/aria2.conf"

# Run the command printed for tool $1 in table $2 inside $3, exactly as pasted by the
# receiver. Keep stderr in $TMP/rx.err for fail to display.
run_printed() {
	local line
	line="$(printed "$1" "$2")"
	[[ -n "$line" ]] || fail "missing $1 row in $2"
	(cd "$3" && HOME="$RXHOME" XDG_CONFIG_HOME="$RXHOME/.config" bash -c "$line" > /dev/null 2> "$TMP/rx.err")
}

# RS-1: file upload, filename from the path.
curl -N -sS "${UP_AUTH[@]}" -T "$TMP/payload.bin" "$BASE/e2e.bin" > "$TMP/upload.out" 2> "$TMP/upload.err" &
UP_PID=$!
UP_PIDS+=("$UP_PID")
SLUG="$(wait_slug "$TMP/upload.out")"
[[ -n "$SLUG" ]] || fail "no slug within 10 s (upload.out: $(cat "$TMP/upload.out"))"
check_commands "$TMP/upload.out" "$BASE" "$SLUG"

# Resume (curl -C -): 400 without claiming the slug; a subsequent full download succeeds.
printf 'parziale' > "$TMP/received.bin"
CODE="$(curl -s -C - -o "$TMP/received.bin" -w '%{http_code}' "$BASE/$SLUG" || true)"
[[ "$CODE" == "400" ]] || fail "resume with curl -C -: $CODE, expected 400"
rm -f "$TMP/received.bin"
# In external mode, commands must use the test base URL (BASHT_PUBLIC_URL == BASHT_E2E_BASE).
if [[ -n "$EXTERNAL" ]]; then
	grep -qF "\"$BASE/" "$TMP/upload.out" || fail "commands use a base other than $BASE: $(cat "$TMP/upload.out")"
fi

# Browsers and previews (chat, email) see a non-consuming download page:
# the subsequent download succeeds.
CODE="$(curl -s -H 'Accept: text/html' -o "$TMP/landing.html" -w '%{http_code}' "$BASE/$SLUG")"
[[ "$CODE" == "200" ]] || fail "download page: $CODE, expected 200"
grep -qF 'e2e.bin' "$TMP/landing.html" || fail "download page missing the filename"
grep -q '<form[^>]*method="post"' "$TMP/landing.html" || fail "download page missing the Download button"
CODE="$(curl -s -A 'Slackbot-LinkExpanding 1.0 (+https://api.slack.com/robots)' -o /dev/null -w '%{http_code}' "$BASE/$SLUG")"
[[ "$CODE" == "200" ]] || fail "Slack preview: $CODE, expected 200 (page)"

# Download using the first printed row: the filename comes from Content-Disposition.
mkdir -p "$TMP/rs1"
run_printed curl "$TMP/upload.out" "$TMP/rs1" || fail "RS-1 download failed"
RECV_SUM="$(sha256sum "$TMP/rs1/e2e.bin" | awk '{print $1}')"
[[ "$RECV_SUM" == "$PAYLOAD_SUM" ]] || fail "RS-1 sha256 mismatch"
wait "$UP_PID" || fail "RS-1 upload exited with an error"
# Final outcome for the sender.
grep -qF "OK: download completed, $((MB * 1048576)) bytes" "$TMP/upload.out" ||
	fail "RS-1: sender missing OK outcome: $(tail -n 2 "$TMP/upload.out")"

CODE="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/$SLUG")"
[[ "$CODE" == "410" ]] || fail "second RS-1 download: $CODE, expected 410"

# Used link and printed commands in a directory with a same-named file (e.g. the file just
# sent when the receiver works in the sender's directory): the file stays intact,
# no other files appear, and the command exits with an error.
mkdir -p "$TMP/k"
printf 'ORIGINALE' > "$TMP/k/e2e.bin"
for tool in curl wget aria2c; do
	command -v "$tool" > /dev/null || continue
	if run_printed "$tool" "$TMP/upload.out" "$TMP/k"; then fail "$tool on used link: exit 0"; fi
done
[[ "$(cat "$TMP/k/e2e.bin")" == "ORIGINALE" ]] || fail "a command on a used link overwrote the file"
# curl 8.5 (unlike 8.18) creates the Content-Disposition file (bashtransfer.txt) before
# -f stops the download: it remains empty.
if [[ -f "$TMP/k/bashtransfer.txt" && ! -s "$TMP/k/bashtransfer.txt" ]]; then rm "$TMP/k/bashtransfer.txt"; fi
[[ "$(ls -A "$TMP/k")" == "e2e.bin" ]] || fail "unexpected files after commands on a used link: $(ls -A "$TMP/k")"

# Valid link, receiver in the sender's directory using the printed command: the same-named
# file is the source itself. curl -OJ refuses to overwrite it; wget and aria2c save a copy
# under another name (same.bin.1, same.1.bin).
for tool in curl wget aria2c; do
	command -v "$tool" > /dev/null || continue
	rm -rf "$TMP/same"
	mkdir -p "$TMP/same"
	cp "$TMP/payload.bin" "$TMP/same/same.bin"
	(cd "$TMP/same" && curl -N -sS "${UP_AUTH[@]}" -T same.bin "$BASE/same.bin" > "$TMP/same.out" 2> "$TMP/same.err") &
	SAME_PID=$!
	UP_PIDS+=("$SAME_PID")
	SAME_SLUG="$(wait_slug "$TMP/same.out")"
	[[ -n "$SAME_SLUG" ]] || fail "same directory ($tool): no slug within 10 s"
	if run_printed "$tool" "$TMP/same.out" "$TMP/same"; then rc=0; else rc=$?; fi
	wait "$SAME_PID" || true
	cmp -s "$TMP/same/same.bin" "$TMP/payload.bin" || fail "$tool in the sender's directory changed the source file"
	if [[ "$tool" == curl ]]; then
		[[ "$rc" != 0 ]] || fail "curl -OJ on the source file: exit 0"
		[[ "$(ls -A "$TMP/same")" == "same.bin" ]] || fail "curl -OJ on the source file: unexpected files: $(ls -A "$TMP/same")"
	else
		[[ "$rc" == 0 ]] || fail "$tool in the sender's directory: exit $rc"
		[[ "$(find "$TMP/same" -type f | wc -l)" == 2 ]] || fail "$tool in the sender's directory: files: $(ls -A "$TMP/same")"
		for f in "$TMP/same"/*; do
			cmp -s "$f" "$TMP/payload.bin" || fail "$tool in the sender's directory: $(basename "$f") differs"
		done
	fi
done

CODE="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/ABCDEFGHIJ")"
[[ "$CODE" == "404" ]] || fail "unknown slug: $CODE, expected 404"

# RS-2: upload from stdin, unknown length, filename from the path.
cat "$TMP/payload.bin" | curl -N -sS "${UP_AUTH[@]}" -T - "$BASE/mydir.tgz" > "$TMP/upload2.out" 2> "$TMP/upload2.err" &
UP2_PID=$!
UP_PIDS+=("$UP2_PID")
SLUG2="$(wait_slug "$TMP/upload2.out")"
[[ -n "$SLUG2" ]] || fail "RS-2: no slug within 10 s"
check_commands "$TMP/upload2.out" "$BASE" "$SLUG2"

# Download using the first printed row (curl -f -OJ): filename from Content-Disposition.
mkdir -p "$TMP/rs2"
run_printed curl "$TMP/upload2.out" "$TMP/rs2" || fail "RS-2 download failed"
RECV2_SUM="$(sha256sum "$TMP/rs2/mydir.tgz" | awk '{print $1}')"
[[ "$RECV2_SUM" == "$PAYLOAD_SUM" ]] || fail "RS-2 sha256 mismatch"
wait "$UP2_PID" || fail "RS-2 upload exited with an error"
# Final outcome also for the pipe with unknown length.
grep -qF "OK: download completed, $((MB * 1048576)) bytes" "$TMP/upload2.out" ||
	fail "RS-2: sender missing OK outcome: $(tail -n 2 "$TMP/upload2.out")"

# Non-ASCII filename: curl -OJ reads filename (UTF-8 bytes), not filename*.
curl -N -sS "${UP_AUTH[@]}" -T "$TMP/payload.bin" "$BASE/citt%C3%A0%20%C3%A8.bin" > "$TMP/utf8.out" 2> "$TMP/utf8.err" &
UTF8_PID=$!
UP_PIDS+=("$UTF8_PID")
UTF8_SLUG="$(wait_slug "$TMP/utf8.out")"
[[ -n "$UTF8_SLUG" ]] || fail "UTF-8 filename: no slug within 10 s"
mkdir -p "$TMP/utf8"
run_printed curl "$TMP/utf8.out" "$TMP/utf8" || fail "UTF-8 filename download failed"
cmp -s "$TMP/utf8/città è.bin" "$TMP/payload.bin" || fail "curl -OJ: UTF-8 filename not preserved: $(ls -A "$TMP/utf8")"
wait "$UTF8_PID" || fail "UTF-8 filename upload exited with an error"

# RS-3: download with wget using the second printed row.
if command -v wget > /dev/null; then
	curl -N -sS "${UP_AUTH[@]}" -T "$TMP/payload.bin" "$BASE/w.bin" > "$TMP/upload3.out" 2> "$TMP/upload3.err" &
	UP3_PID=$!
	UP_PIDS+=("$UP3_PID")
	SLUG3="$(wait_slug "$TMP/upload3.out")"
	[[ -n "$SLUG3" ]] || fail "RS-3: no slug within 10 s"
	mkdir -p "$TMP/rs3"
	run_printed wget "$TMP/upload3.out" "$TMP/rs3" || fail "wget download failed"
	cmp -s "$TMP/rs3/w.bin" "$TMP/payload.bin" || fail "wget download: content or filename mismatch: $(ls -A "$TMP/rs3")"
	wait "$UP3_PID" || fail "RS-3 upload exited with an error"
fi

# RS-3b: download with aria2c using the third printed row (if installed).
if command -v aria2c > /dev/null; then
	cat "$TMP/payload.bin" | curl -N -sS "${UP_AUTH[@]}" -T - "$BASE/a.bin" > "$TMP/upload3b.out" 2> "$TMP/upload3b.err" &
	UP3B_PID=$!
	UP_PIDS+=("$UP3B_PID")
	SLUG3B="$(wait_slug "$TMP/upload3b.out")"
	[[ -n "$SLUG3B" ]] || fail "RS-3b: no slug within 10 s"
	mkdir -p "$TMP/rs3b"
	run_printed aria2c "$TMP/upload3b.out" "$TMP/rs3b" || fail "aria2c download failed"
	cmp -s "$TMP/rs3b/a.bin" "$TMP/payload.bin" || fail "aria2c download: content or filename mismatch: $(ls -A "$TMP/rs3b")"
	wait "$UP3B_PID" || fail "RS-3b upload exited with an error"
fi

# RS-4: sender disconnects midway. Receiver must get an error, not a truncated file with exit 0.
mkfifo "$TMP/abort.fifo"
curl -N -sS "${UP_AUTH[@]}" -T - "$BASE/abort.bin" < "$TMP/abort.fifo" > "$TMP/upload4.out" 2> "$TMP/upload4.err" &
UP4_PID=$!
UP_PIDS+=("$UP4_PID")
exec 3> "$TMP/abort.fifo"
# curl waits for stdin data before sending the request: send the first block now and keep the pipe open.
head -c 1048576 /dev/urandom >&3 &
UP_PIDS+=($!)
SLUG4="$(wait_slug "$TMP/upload4.out")"
[[ -n "$SLUG4" ]] || fail "RS-4: no slug within 10 s"
curl -sS -o "$TMP/abort.dl" "$BASE/$SLUG4" 2> "$TMP/abort.dl.err" &
DL4_PID=$!
UP_PIDS+=("$DL4_PID")
sleep 1
kill "$UP4_PID" 2>/dev/null || true
exec 3>&-
if timeout 30 tail --pid="$DL4_PID" -f /dev/null; then
	if wait "$DL4_PID"; then fail "RS-4: download succeeded after sender disconnected"; fi
else
	fail "RS-4: receiver did not exit within 30 s after sender disconnected"
fi

# RS-5: receiver disconnects midway. Sender must exit with an error.
head -c $((64 * 1048576)) /dev/zero > "$TMP/zero.bin"
curl -N -sS "${UP_AUTH[@]}" -T "$TMP/zero.bin" "$BASE/rabort.bin" > "$TMP/upload5.out" 2> "$TMP/upload5.err" &
UP5_PID=$!
UP_PIDS+=("$UP5_PID")
SLUG5="$(wait_slug "$TMP/upload5.out")"
[[ -n "$SLUG5" ]] || fail "RS-5: no slug within 10 s"
curl -s --max-time 1 --limit-rate 1M -o /dev/null "$BASE/$SLUG5" || true
if timeout 30 tail --pid="$UP5_PID" -f /dev/null; then
	if wait "$UP5_PID"; then fail "RS-5: upload succeeded after receiver disconnected"; fi
else
	fail "RS-5: sender did not exit within 30 s after receiver disconnected"
fi
grep -qF "ERRORE: download NOT completed after " "$TMP/upload5.out" ||
	fail "RS-5: sender missing ERRORE outcome: $(tail -n 2 "$TMP/upload5.out")"
rm -f "$TMP/zero.bin"

head -c 1048576 /dev/urandom > "$TMP/par.bin"

# RS-7: sender disconnects while waiting for the receiver, with an unread body. Release the
# slug within seconds (410), not at BASHT_WAIT_TIMEOUT. Python sender: declare 1 MiB,
# send 1000 bytes, read the commands and close. With curl and a pipe, send timing depends
# on curl's stdin buffer.
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
[[ -n "$SLUG7" ]] || fail "RS-7: no slug"
# A single GET after waiting: without detection it would claim the slug, so repeated
# GET requests would prove nothing.
sleep 3
CODE="$(curl -s --max-time 5 -o /dev/null -w '%{http_code}' "$BASE/$SLUG7" || true)"
[[ "$CODE" == "410" ]] || fail "RS-7: disconnected sender slug: $CODE, expected 410 within 3 s"

# RS-6: parallel transfers, each with its own slug and intact content.
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
	wait "${PAR_PIDS[$i]}" || fail "RS-6: parallel transfer $((i + 1)) failed"
done

# RS-8: reproduce the page's browser upload with curl. Reservation (JSON POST /) returns
# NDJSON: link and token, then attached, progress and outcome. Send the body with PUT
# and the token; receiver uses the page's Download action (POST /<slug>).
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
	fail "RS-8: reservation missing link: $(cat "$TMP/web.ndjson" "$TMP/web.err")"
curl -sS -H "X-Bashtransfer-Token: $WEB_TOKEN" -T "$TMP/payload.bin" "$BASE/$WEB_SLUG" > "$TMP/web.put" 2>&1 &
WEB_PUT_PID=$!
UP_PIDS+=("$WEB_PUT_PID")
mkdir -p "$TMP/rs8"
(cd "$TMP/rs8" && curl -sS -f -OJ -X POST "$BASE/$WEB_SLUG") || fail "RS-8: POST download failed"
cmp -s "$TMP/rs8/web upload.bin" "$TMP/payload.bin" || fail "RS-8: content or filename mismatch: $(ls -A "$TMP/rs8")"
wait "$WEB_STREAM_PID" || fail "RS-8: reservation stream exited with an error"
wait "$WEB_PUT_PID" || fail "RS-8: PUT exited with an error"
grep -qx '{"event":"attached"}' "$TMP/web.ndjson" || fail "RS-8: missing attached event: $(cat "$TMP/web.ndjson")"
tail -n 1 "$TMP/web.ndjson" | grep -q "^{\"outcome\":\"ok\",\"bytes\":$WEB_SIZE," ||
	fail "RS-8: stream outcome: $(tail -n 1 "$TMP/web.ndjson")"
tail -n 1 "$TMP/web.put" | grep -q '^{"outcome":"ok"' || fail "RS-8: PUT outcome: $(tail -n 2 "$TMP/web.put")"

# Page cancellation: DELETE with the token closes the link immediately, even with an open stream.
curl -sS -N "${UP_AUTH[@]}" -H 'Content-Type: application/json' -d '{"name":"x.bin","size":1}' \
	"$BASE/" > "$TMP/cancel.ndjson" 2>&1 &
CANCEL_PID=$!
UP_PIDS+=("$CANCEL_PID")
for _ in $(seq 1 100); do
	[[ -s "$TMP/cancel.ndjson" ]] && break
	sleep 0.1
done
read -r C_SLUG C_TOKEN < <(python3 -c 'import json,sys; d=json.loads(open(sys.argv[1]).readline()); print(d["slug"], d["token"])' "$TMP/cancel.ndjson") ||
	fail "cancellation: reservation missing link: $(cat "$TMP/cancel.ndjson")"
CODE="$(curl -s -X DELETE -H "X-Bashtransfer-Token: $C_TOKEN" -o /dev/null -w '%{http_code}' "$BASE/$C_SLUG")"
[[ "$CODE" == "204" ]] || fail "cancellation: DELETE $CODE, expected 204"
CODE="$(curl -s -H 'Accept: text/html' -o /dev/null -w '%{http_code}' "$BASE/$C_SLUG")"
[[ "$CODE" == "410" ]] || fail "cancellation: link $CODE, expected 410"
wait "$CANCEL_PID" || true
tail -n 1 "$TMP/cancel.ndjson" | grep -q '^{"outcome":"sender_gone"' || fail "cancellation: stream outcome: $(tail -n 1 "$TMP/cancel.ndjson")"

if [[ -z "$EXTERNAL" || -n "$AUTH_CRED" ]]; then
# Auth (D9): only PUT is protected; missing credentials or wrong password: 401.
printf 'auth' > "$TMP/small.bin"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -T "$TMP/small.bin" "$AUTH_BASE/auth.bin")"
[[ "$CODE" == "401" ]] || fail "PUT without credentials: $CODE, expected 401"
CODE="$(curl -s -o /dev/null -w '%{http_code}' -u "${AUTH_CRED%%:*}:sbagliata" -T "$TMP/small.bin" "$AUTH_BASE/auth.bin")"
[[ "$CODE" == "401" ]] || fail "PUT with wrong password: $CODE, expected 401"

# Authorized upload with spaces in the filename (D5): commands use the sanitized name.
curl -N -sS -u "$AUTH_CRED" -T "$TMP/small.bin" "$AUTH_BASE/strano%20nome.zip" > "$TMP/auth.out" 2> "$TMP/auth.err" &
AUTH_UP_PID=$!
UP_PIDS+=("$AUTH_UP_PID")
AUTH_SLUG="$(wait_slug "$TMP/auth.out")"
[[ -n "$AUTH_SLUG" ]] || fail "auth: no slug within 10 s (auth.out: $(cat "$TMP/auth.out"))"
check_commands "$TMP/auth.out" "$AUTH_BASE" "$AUTH_SLUG"

# Download remains public: no credentials required.
mkdir -p "$TMP/authdl"
run_printed curl "$TMP/auth.out" "$TMP/authdl" || fail "public download with auth failed"
cmp -s "$TMP/authdl/strano nome.zip" "$TMP/small.bin" || fail "download with auth: content or filename mismatch: $(ls -A "$TMP/authdl")"
wait "$AUTH_UP_PID" || fail "authorized upload exited with an error"
fi

# Invariant: no receiving/sending command or incorrect variant changes the source
# (local server only: the script starts instances with its own configurations).
if [[ -z "$EXTERNAL" ]]; then
	python3 "$ROOT/scripts/invariant.py" "$TMP/bashtransfer" || fail "source invariant violated"
fi

echo "E2E OK"
