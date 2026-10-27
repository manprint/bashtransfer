#!/usr/bin/env bash
# Misura D13: relay contro baseline a due hop su loopback, con buffer 64 KiB, 256 KiB e 1 MiB.
# Stampa ambiente, righe grezze e la mediana dei campioni MB/s per benchmark e buffer.
# Eseguire senza -race: il race detector altera il throughput.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$ROOT"

echo "nproc: $(nproc)"
echo "go: $(go version)"
echo "cpu: $(grep -m1 'model name' /proc/cpuinfo | cut -d: -f2- | sed 's/^ *//')"

for buf in 65536 262144 1048576; do
	echo "== BASHT_BENCH_BUF=$buf"
	out="$TMP/bench-$buf.txt"
	BASHT_BENCH_BUF="$buf" go test -run '^$' \
		-bench 'BenchmarkRelayLoopback|BenchmarkBaselineRelay' \
		-benchtime 3x -count 5 ./internal/server | tee "$out"
	# Mediana dei campioni MB/s per benchmark (campo precedente a "MB/s").
	awk '/^Benchmark.* MB\/s/ {
		name = $1; sub(/-[0-9]+$/, "", name)
		for (i = 2; i <= NF; i++) if ($i == "MB/s") v = $(i - 1)
		print name, v
	}' "$out" | sort -k1,1 -k2,2n | awk '
		{ n[$1]++; sorted[$1, n[$1]] = $2 }
		END {
			for (name in n) {
				mid = int((n[name] + 1) / 2)
				printf "median %s: %s MB/s (%d campioni)\n", name, sorted[name, mid], n[name]
			}
		}'
done
