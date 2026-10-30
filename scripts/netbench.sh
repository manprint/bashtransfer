#!/usr/bin/env bash
# Misura in Docker l'effetto di algoritmo di congestione e buffer TCP sul relay, con RTT, perdita
# e banda emulati da netem (default 1 Gbit/s per direzione). Le due gambe del relay sono misurate
# separatamente; il throughput di un trasferimento è il minimo delle due.
#   download: mittente locale -> relay -> ricevente remoto. Conta il profilo del relay (algoritmo
#             di congestione e tcp_wmem: il relay invia) e tcp_rmem del ricevente.
#   upload:   mittente remoto -> relay -> ricevente locale. Conta il profilo del mittente
#             (algoritmo e tcp_wmem) e tcp_rmem del relay.
# Il relay ha due interfacce: "wan" con netem verso il remoto, "lan" senza netem verso il locale.
# Il percorso è HTTP diretto verso l'app, senza nginx né TLS: si misura solo il trasporto TCP.
#
# Requisiti: docker; modulo sch_netem caricato sull'host (sudo modprobe sch_netem); un'immagine
# già presente in locale con sh, curl, tc, ethtool, pkill (BASHT_NB_IMAGE). Nessun pull.
# Profili: stock = cubic, tcp_rmem max 6 MiB, tcp_wmem max 4 MiB (default del kernel Linux);
# bbr = bbr con buffer stock; big = cubic con buffer max 32 MiB; tuned = bbr, buffer 32 MiB,
# tcp_slow_start_after_idle=0; host = i valori attuali dell'host che esegue lo script (per
# esempio la workstation da cui si invia e si riceve).
# Default: RTT 20 ms (misurato dalla workstation verso Internet) e 40 ms (VM più lontana);
# perdita 0, 0.01% e 0.1% (rete congestionata).
set -euo pipefail

IMG="${BASHT_NB_IMAGE:?impostare BASHT_NB_IMAGE: immagine locale con sh, curl, tc, ethtool, pkill}"
DUR="${BASHT_NB_SECONDS:-10}"
RTTS="${BASHT_NB_RTTS:-20 40}"
LOSSES="${BASHT_NB_LOSSES:-0 0.01 0.1}"
RATE="${BASHT_NB_RATE:-1gbit}"
SERVER_PROFILES="${BASHT_NB_SERVER:-stock bbr big tuned}"
CLIENT_PROFILES="${BASHT_NB_CLIENT:-stock host}"
# Per l'upload conta solo tcp_rmem del relay: due profili bastano.
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

# sysctl_args stampa gli argomenti --sysctl di un profilo, uno per riga.
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
	*) echo "profilo sconosciuto: $1" >&2; exit 1 ;;
	esac
	printf '%s\n' --sysctl "net.ipv4.tcp_congestion_control=$cc" \
		--sysctl "net.ipv4.tcp_rmem=$rmem" --sysctl "net.ipv4.tcp_wmem=$wmem" \
		--sysctl "net.ipv4.tcp_slow_start_after_idle=$ssai"
}

# iface_on stampa l'interfaccia del container $1 sulla rete $2.
iface_on() {
	local ip
	ip="$(docker inspect -f "{{(index .NetworkSettings.Networks \"$2\").IPAddress}}" "$1")"
	docker exec "$1" sh -c "ip -o -4 addr show | awk -v ip='$ip' '\$4 ~ \"^\" ip \"/\" {print \$2}'"
}

# netem applica RTT/2 di ritardo, perdita e banda all'uscita di un'interfaccia. Gli offload sono
# disattivati: netem vedrebbe pacchetti GSO da 64 KiB e la perdita colpirebbe decine di segmenti.
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

# measure invia uno stream da $1 e lo riceve in $2 per DUR secondi; stampa MB/s.
measure() {
	local sender="$1" receiver="$2" out slug=""
	docker exec "$sender" sh -c 'pkill -x curl; rm -f /tmp/up.out' > /dev/null 2>&1 || true
	docker exec -d "$sender" sh -c "head -c 1000000000000 /dev/zero | curl -sS -N -T - http://$SRV:8080/nb.bin > /tmp/up.out 2>&1"
	for _ in $(seq 1 100); do
		slug="$(docker exec "$sender" sh -c 'cat /tmp/up.out 2>/dev/null' | sed -n 's#^| curl *| curl -f -OJ ".*/\([0-9A-Za-z]\{10\}\)" *|$#\1#p' | head -n 1)"
		[[ -n "$slug" ]] && break
		sleep 0.1
	done
	[[ -n "$slug" ]] || { echo "nessuno slug dal mittente $sender" >&2; exit 1; }
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
