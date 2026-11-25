#!/bin/sh
# Writes the VPS snapshot that Tunas shows in /status. Run from cron on the
# VPS every 2 minutes as the user that may use Docker:
#
#   */2 * * * * /srv/tunas/host-stats.sh /srv/monitor/host.json
#
# The Tunas container mounts /srv/monitor read-only (HOST_STATS_FILE=/monitor/host.json);
# it never gets docker.sock or the host's /proc.
#
# The file holds numbers and container names only: no environment variables,
# no arguments, no hostnames, no secrets. The server rejects any other field.
#
# For tests the sources can be replaced: PROC_DIR, DOCKER_BIN, DISK_MOUNTS.
set -eu

OUT=${1:-/srv/monitor/host.json}
PROC=${PROC_DIR:-/proc}
DOCKER=${DOCKER_BIN:-docker}
DISKS=${DISK_MOUNTS:-"/ /srv"}

TMP=$(mktemp -d "${TMPDIR:-/tmp}/tunas-host.XXXXXX")
trap 'rm -rf "$TMP"' EXIT

now=$(date +%s)
uptime_s=$(cut -d. -f1 "$PROC/uptime")
set -- $(cat "$PROC/loadavg")
l1=$1 l2=$2 l3=$3
cpus=$(grep -c '^processor' "$PROC/cpuinfo" 2>/dev/null || true)
[ "${cpus:-0}" -ge 1 ] 2>/dev/null || cpus=1

# /proc/meminfo is in kB
mem() { awk -v k="$1" '$1 == k":" { printf "%d", $2 / 1024 }' "$PROC/meminfo"; }
mem_total=$(mem MemTotal)
mem_avail=$(mem MemAvailable)
swap_total=$(mem SwapTotal)
swap_free=$(mem SwapFree)
mem_used=$((mem_total - mem_avail))
swap_used=$((swap_total - swap_free))

# disks: used percent and free GB per mount (skipped when df fails)
disks=""
for m in $DISKS; do
	# find the "NN%" column instead of counting from the left: a filesystem name can hold spaces
	line=$(df -Pk "$m" 2>/dev/null | awk 'NR == 2 { for (i = NF; i > 2; i--) if ($i ~ /^[0-9]+%$/) { p = $i; sub("%", "", p); printf "%d %.1f", p, $(i - 1) / 1048576; exit } }') || line=""
	[ -n "$line" ] || continue
	set -- $line
	[ -z "$disks" ] || disks="$disks,"
	disks="$disks{\"mount\":\"$m\",\"used_pct\":$1,\"free_gb\":$2}"
done

# containers: stats (memory, cpu), state and health. Failures leave the list empty.
: > "$TMP/ps"; : > "$TMP/health"; : > "$TMP/stats"
"$DOCKER" ps -a --format '{{.Names}}|{{.State}}' > "$TMP/ps" 2>/dev/null || : > "$TMP/ps"
ids=$("$DOCKER" ps -q 2>/dev/null || true)
if [ -n "$ids" ]; then
	# --format picks two fields on purpose: never the Env or Cmd of a container
	# shellcheck disable=SC2086
	"$DOCKER" inspect --format '{{.Name}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}' $ids > "$TMP/health" 2>/dev/null || : > "$TMP/health"
fi
"$DOCKER" stats --no-stream --format '{{.Name}}|{{.MemUsage}}|{{.CPUPerc}}' > "$TMP/stats" 2>/dev/null || : > "$TMP/stats"

containers=$(awk -F'|' -v total="$mem_total" '
function mb(s,   n, u) {            # "38.2MiB", "1.9GiB", "512kB" -> MB
	n = s + 0; u = s; gsub(/[0-9. ]/, "", u)
	if (u == "GiB" || u == "GB") return n * 1024
	if (u == "MiB" || u == "MB") return n
	if (u == "KiB" || u == "kB" || u == "KB") return n / 1024
	return n / 1048576
}
function clean(s) { sub(/^\//, "", s); gsub(/[^A-Za-z0-9_.-]/, "", s); return s }
function word(s) { gsub(/[^a-z-]/, "", s); return s }
FILENAME == ARGV[1] { st[clean($1)] = word($2); order[++n] = clean($1); next }
FILENAME == ARGV[2] { hl[clean($1)] = word($2); next }
{
	name = clean($1); split($2, m, " / "); cpu = $3; gsub("%", "", cpu)
	use[name] = mb(m[1]); lim[name] = mb(m[2]); pct[name] = cpu + 0
}
END {
	for (i = 1; i <= n; i++) {
		c = order[i]; if (c == "") continue
		l = lim[c]; if (l >= total * 0.9) l = 0     # docker shows (about) the host RAM when there is no limit
		out = out (out == "" ? "" : ",") sprintf("{\"name\":\"%s\",\"status\":\"%s\",\"health\":\"%s\",\"mem_mb\":%.1f,\"mem_limit_mb\":%.1f,\"cpu_pct\":%.2f}", c, st[c], hl[c], use[c], l, pct[c])
	}
	print out
}' "$TMP/ps" "$TMP/health" "$TMP/stats")

umask 022
mkdir -p "$(dirname "$OUT")"
tmpfile="$OUT.tmp.$$"
printf '{"ts":%s,"uptime_s":%s,"load":[%s,%s,%s],"cpus":%s,"mem_total_mb":%s,"mem_used_mb":%s,"mem_avail_mb":%s,"swap_total_mb":%s,"swap_used_mb":%s,"disks":[%s],"containers":[%s]}\n' \
	"$now" "$uptime_s" "$l1" "$l2" "$l3" "$cpus" "$mem_total" "$mem_used" "$mem_avail" "$swap_total" "$swap_used" "$disks" "$containers" > "$tmpfile"
mv "$tmpfile" "$OUT"   # atomic: the server never sees half a file
