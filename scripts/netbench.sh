#!/usr/bin/env bash
# Measure congestion algorithm and TCP buffer effects on the relay in Docker, with RTT, loss
# and bandwidth emulated by netem (default 1 Gbit/s each way). Measure each relay leg
# separately; transfer throughput is the minimum of the two.
#   download: local sender -> relay -> remote receiver. The relay profile (congestion
#             algorithm and tcp_wmem: relay sends) and receiver tcp_rmem matter.
#   upload:   remote sender -> relay -> local receiver. The sender profile
#             (algorithm and tcp_wmem) and relay tcp_rmem matter.
# The relay has two interfaces: "wan" with netem toward remote, "lan" without netem toward local.
# The path is direct HTTP to the app, without nginx or TLS: measure TCP transport only.
#
# Requirements: Docker; sch_netem loaded on the host (sudo modprobe sch_netem); an existing
# local image with sh, curl, tc, ethtool, pkill (BASHT_NB_IMAGE). No image pulls.
# Profiles: stock = cubic, tcp_rmem max 6 MiB, tcp_wmem max 4 MiB (Linux kernel defaults);
# bbr = bbr with stock buffers; big = cubic with 32 MiB max buffers; tuned = bbr, 32 MiB buffers,
# tcp_slow_start_after_idle=0; host = current values on the host running this script
# (e.g. the workstation used for sending and receiving).
# Defaults: RTT 20 ms (measured from the workstation to the Internet) and 40 ms (more distant VM);
# loss 0, 0.01% and 0.1% (congested network).
set -euo pipefail

IMG="${BASHT_NB_IMAGE:?set BASHT_NB_IMAGE: local image with sh, curl, tc, ethtool, pkill}"
DUR="${BASHT_NB_SECONDS:-10}"
RTTS="${BASHT_NB_RTTS:-20 40}"
LOSSES="${BASHT_NB_LOSSES:-0 0.01 0.1}"
RATE="${BASHT_NB_RATE:-1gbit}"
SERVER_PROFILES="${BASHT_NB_SERVER:-stock bbr big tuned}"
CLIENT_PROFILES="${BASHT_NB_CLIENT:-stock host}"
# Only relay tcp_rmem matters for upload: two profiles are enough.
UPLOAD_SERVER_PROFILES="${BASHT_NB_UPLOAD_SERVER:-stock tuned}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
P="btnb$$"
SRV="$P-srv"
REMOTE="$P-remote"
LOCAL="$P-local"

cleanup() {
	docker rm -f "$SRV" "$REMOTE" "$LOCAL" > /dev/null 2>&1 || true
	docker network rm "$P-wan" "$P-lan" > /dev/null 2>&1 || true
	rm -rf "$TMP"
}
trap cleanup EXIT

(cd "$ROOT" && CGO_ENABLED=0 go build -trimpath -o "$TMP/bashtransfer" ./cmd/bashtransfer)
docker network create "$P-wan" > /dev/null
docker network create "$P-lan" > /dev/null

STOCK_RMEM="4096 131072 6291456"
STOCK_WMEM="4096 16384 4194304"
BIG_RMEM="4096 131072 33554432"
BIG_WMEM="4096 16384 33554432"

# sysctl_args prints a profile's --sysctl arguments, one per line.
sysctl_args() {
	local cc rmem wmem ssai=1
	case "$1" in
	stock) cc=cubic rmem="$STOCK_RMEM" wmem="$STOCK_WMEM" ;;
	bbr) cc=bbr rmem="$STOCK_RMEM" wmem="$STOCK_WMEM" ;;
	big) cc=cubic rmem="$BIG_RMEM" wmem="$BIG_WMEM" ;;
	tuned) cc=bbr rmem="$BIG_RMEM" wmem="$BIG_WMEM" ssai=0 ;;
	host)
		cc="$(sysctl -n net.ipv4.tcp_congestion_control)"
		rmem="$(sysctl -n net.ipv4.tcp_rmem | tr -s '\t ' ' ')"
		wmem="$(sysctl -n net.ipv4.tcp_wmem | tr -s '\t ' ' ')"
		ssai="$(sysctl -n net.ipv4.tcp_slow_start_after_idle)"
		;;
	*) echo "unknown profile: $1" >&2; exit 1 ;;
	esac
	printf '%s\n' --sysctl "net.ipv4.tcp_congestion_control=$cc" \
		--sysctl "net.ipv4.tcp_rmem=$rmem" --sysctl "net.ipv4.tcp_wmem=$wmem" \
		--sysctl "net.ipv4.tcp_slow_start_after_idle=$ssai"
}

# iface_on prints container $1's interface on network $2.
iface_on() {
	local ip
	ip="$(docker inspect -f "{{(index .NetworkSettings.Networks \"$2\").IPAddress}}" "$1")"
	docker exec "$1" sh -c "ip -o -4 addr show | awk -v ip='$ip' '\$4 ~ \"^\" ip \"/\" {print \$2}'"
}

# netem applies RTT/2 delay, loss and bandwidth to an interface's egress. Disable offloading:
# netem would see 64 KiB GSO packets, with packet loss affecting dozens of segments.
netem() {
	local c="$1" ifc="$2" rtt="$3" loss="$4" lossarg=""
	if [[ "$loss" != "0" ]]; then lossarg="loss ${loss}%"; fi
	docker exec "$c" sh -c "ethtool -K $ifc tso off gso off gro off > /dev/null 2>&1 || true
		tc qdisc replace dev $ifc root netem delay $(awk -v r="$rtt" 'BEGIN{print r/2}')ms rate $RATE limit 100000 $lossarg"
}

start_server() {
	docker rm -f "$SRV" > /dev/null 2>&1 || true
	mapfile -t args < <(sysctl_args "$1")
	docker run -d --name "$SRV" --user 0 --network "$P-wan" --cap-add NET_ADMIN "${args[@]}" \
		-v "$TMP/bashtransfer:/bashtransfer:ro" -e BASHT_LISTEN=:8080 \
		-e "BASHT_PUBLIC_URL=http://$SRV:8080" --entrypoint /bashtransfer "$IMG" > /dev/null
	docker network connect "$P-lan" "$SRV"
	SRV_WAN="$(iface_on "$SRV" "$P-wan")"
}

start_remote() {
	docker rm -f "$REMOTE" > /dev/null 2>&1 || true
	mapfile -t args < <(sysctl_args "$1")
	docker run -d --name "$REMOTE" --user 0 --network "$P-wan" --cap-add NET_ADMIN "${args[@]}" \
		--entrypoint sleep "$IMG" infinity > /dev/null
	REMOTE_WAN="$(iface_on "$REMOTE" "$P-wan")"
}

# measure sends a stream from $1 to $2 for DUR seconds and prints MB/s.
measure() {
	local sender="$1" receiver="$2" out slug=""
	docker exec "$sender" sh -c 'pkill -x curl; rm -f /tmp/up.out' > /dev/null 2>&1 || true
	docker exec -d "$sender" sh -c "head -c 1000000000000 /dev/zero | curl -sS -N -T - http://$SRV:8080/nb.bin > /tmp/up.out 2>&1"
	for _ in $(seq 1 100); do
		slug="$(docker exec "$sender" sh -c 'cat /tmp/up.out 2>/dev/null' | sed -n 's#^| curl *| curl -f -OJ ".*/\([0-9A-Za-z]\{10\}\)" *|$#\1#p' | head -n 1)"
		[[ -n "$slug" ]] && break
		sleep 0.1
	done
	[[ -n "$slug" ]] || { echo "no slug from sender $sender" >&2; exit 1; }
	out="$(docker exec "$receiver" curl -s -o /dev/null --max-time "$DUR" -w '%{size_download} %{time_total}' "http://$SRV:8080/$slug" || true)"
	docker exec "$sender" pkill -x curl > /dev/null 2>&1 || true
	awk -v o="$out" 'BEGIN{split(o, a, " "); if (a[2] > 0) printf "%.1f", a[1] / a[2] / 1e6; else printf "0"}'
}

docker run -d --name "$LOCAL" --network "$P-lan" --entrypoint sleep "$IMG" infinity > /dev/null

echo "image: $IMG  rate: $RATE  duration: ${DUR}s  host cc: $(sysctl -n net.ipv4.tcp_congestion_control)  kernel: $(uname -r)"
printf 'leg\trtt_ms\tloss_pct\tserver\tclient\tMB/s\n'
for sp in $SERVER_PROFILES; do
	start_server "$sp"
	for cp in $CLIENT_PROFILES; do
		start_remote "$cp"
		for rtt in $RTTS; do
			for loss in $LOSSES; do
				netem "$SRV" "$SRV_WAN" "$rtt" "$loss"
				netem "$REMOTE" "$REMOTE_WAN" "$rtt" "$loss"
				printf 'download\t%s\t%s\t%s\t%s\t%s\n' "$rtt" "$loss" "$sp" "$cp" "$(measure "$LOCAL" "$REMOTE")"
				if [[ " $UPLOAD_SERVER_PROFILES " == *" $sp "* ]]; then
					printf 'upload\t%s\t%s\t%s\t%s\t%s\n' "$rtt" "$loss" "$sp" "$cp" "$(measure "$REMOTE" "$LOCAL")"
				fi
			done
		done
	done
done
