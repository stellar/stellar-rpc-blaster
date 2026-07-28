#!/usr/bin/env bash
#
# Run one tx-submission benchmark phase end to end.
#
# For each mode: restart stellar-rpc so the Prometheus sliding window holds
# exactly one mode, run `tx-load-test bench`, scrape /metrics, copy the RPC log
# out of the container, and harvest a summary JSON. Everything lands in --out.
#
# See .claude/docs/869-tx-submission-plan.md.

set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)

OUT=""
CONTAINER="stellar"
MODES="sac-transfer,oz-transfer,soroswap"
TARGET_RPS="10"
DURATION="120s"
RAMP_UP="10s"
STATE_FILE=""
RPC_URL="http://localhost:8000/rpc"
METRICS_URL="http://localhost:6061/metrics"
BINARY="./tx-load-test"

HEALTH_TIMEOUT_SECONDS="${HEALTH_TIMEOUT_SECONDS:-120}"
HEALTH_POLL_SECONDS=2

usage() {
    cat <<'EOF'
usage: run-phase.sh --out <bundle-dir> [options]

  --out <dir>            result bundle directory (required, created if absent)
  --container <name>     quickstart container name          (default: stellar)
  --modes <a,b,c>        comma-separated benchmark modes
                         (default: sac-transfer,oz-transfer,soroswap)
  --target-rps <n>       Soroban steady-state RPS           (default: 10)
  --duration <d>         benchmark duration per mode        (default: 120s)
  --ramp-up <d>          linear ramp per mode               (default: 10s)
  --state-file <path>    tx-load-test state file            (default: tool default)
  --rpc-url <url>        JSON-RPC endpoint polled for getHealth
                         (default: http://localhost:8000/rpc)
  --metrics-url <url>    admin /metrics endpoint
                         (default: http://localhost:6061/metrics)
  --binary <path>        tx-load-test binary                (default: ./tx-load-test)
  -h, --help             this text

FEE_PAYER must be exported before running. bench takes its RPC URL from the
state file; --rpc-url only drives the health poll.

HEALTH_TIMEOUT_SECONDS overrides the wait for getHealth after each RPC restart
(default 120). Set it higher on testnet: the restarted captive core must catch
up to the network before RPC reports healthy.
EOF
}

die() {
    echo "run-phase.sh: $*" >&2
    exit 1
}

while [ $# -gt 0 ]; do
    key="$1"
    case "$key" in
        -h|--help)
            usage
            exit 0
            ;;
        --out|--container|--modes|--target-rps|--duration|--ramp-up|--state-file|--rpc-url|--metrics-url|--binary)
            [ $# -ge 2 ] || die "$key needs a value"
            value="$2"
            shift 2
            ;;
        *)
            usage >&2
            die "unknown argument: $key"
            ;;
    esac
    case "$key" in
        --out)          OUT="$value" ;;
        --container)    CONTAINER="$value" ;;
        --modes)        MODES="$value" ;;
        --target-rps)   TARGET_RPS="$value" ;;
        --duration)     DURATION="$value" ;;
        --ramp-up)      RAMP_UP="$value" ;;
        --state-file)   STATE_FILE="$value" ;;
        --rpc-url)      RPC_URL="$value" ;;
        --metrics-url)  METRICS_URL="$value" ;;
        --binary)       BINARY="$value" ;;
    esac
done

[ -n "$OUT" ] || { usage >&2; die "--out is required"; }
[ -n "${FEE_PAYER:-}" ] || die "FEE_PAYER is not set — export the seed that \`setup\` printed"
[ -x "$BINARY" ] || die "tx-load-test binary not found or not executable: $BINARY"
[ -f "$SCRIPT_DIR/harvest.py" ] || die "harvest.py not found next to this script"
command -v python3 >/dev/null 2>&1 || die "python3 is required"
command -v docker >/dev/null 2>&1 || die "docker is required"
command -v curl >/dev/null 2>&1 || die "curl is required"

mkdir -p "$OUT"

now_rfc3339() {
    date -u +%Y-%m-%dT%H:%M:%SZ
}

# Poll getHealth until the RPC reports healthy, or give up.
wait_healthy() {
    local deadline=$((SECONDS + HEALTH_TIMEOUT_SECONDS))
    local body
    while [ "$SECONDS" -lt "$deadline" ]; do
        body=$(curl -s -m 5 -X POST -H 'Content-Type: application/json' \
            -d '{"jsonrpc":"2.0","id":1,"method":"getHealth"}' \
            "$RPC_URL" 2>/dev/null || true)
        if printf '%s' "$body" | tr -d ' \n\t' | grep -q '"status":"healthy"'; then
            return 0
        fi
        sleep "$HEALTH_POLL_SECONDS"
    done
    die "RPC did not report healthy on $RPC_URL within ${HEALTH_TIMEOUT_SECONDS}s"
}

# --- meta.json -------------------------------------------------------------

IMAGE=$(docker inspect --format '{{.Config.Image}}' "$CONTAINER" 2>/dev/null || true)
BANNER=$(docker logs "$CONTAINER" 2>&1 | head -40 |
    grep -i -m1 -E 'stellar-rpc.*[0-9]+\.[0-9]+\.[0-9]+' || true)

META_OUT="$OUT/meta.json" \
META_HOST="$(uname -a)" \
META_DATE="$(now_rfc3339)" \
META_CONTAINER="$CONTAINER" \
META_IMAGE="$IMAGE" \
META_BANNER="$BANNER" \
META_MODES="$MODES" \
META_TARGET_RPS="$TARGET_RPS" \
META_DURATION="$DURATION" \
META_RAMP_UP="$RAMP_UP" \
META_STATE_FILE="$STATE_FILE" \
META_RPC_URL="$RPC_URL" \
META_METRICS_URL="$METRICS_URL" \
META_BINARY="$BINARY" \
python3 -c '
import json, os
meta = {
    "generated_at": os.environ["META_DATE"],
    "host": os.environ["META_HOST"],
    "container": os.environ["META_CONTAINER"],
    "image": os.environ["META_IMAGE"] or None,
    "rpc_version_banner": os.environ["META_BANNER"] or None,
    "flags": {
        "modes": os.environ["META_MODES"].split(","),
        "target_rps": int(os.environ["META_TARGET_RPS"]),
        "classic_rps": 0,
        "duration": os.environ["META_DURATION"],
        "ramp_up": os.environ["META_RAMP_UP"],
        "state_file": os.environ["META_STATE_FILE"] or None,
        "rpc_url": os.environ["META_RPC_URL"],
        "metrics_url": os.environ["META_METRICS_URL"],
        "binary": os.environ["META_BINARY"],
    },
}
with open(os.environ["META_OUT"], "w") as fh:
    json.dump(meta, fh, indent=2)
    fh.write("\n")
'
echo "wrote $OUT/meta.json"

# --- per-mode loop ---------------------------------------------------------

for MODE in $(printf '%s' "$MODES" | tr ',' ' '); do
    echo
    echo "=== $MODE ==="

    echo "restarting stellar-rpc in $CONTAINER"
    docker exec "$CONTAINER" supervisorctl restart stellar-rpc
    wait_healthy
    echo "RPC healthy"

    WINDOW_START=$(now_rfc3339)

    bench_args=(bench
        --mode "$MODE"
        --target-rps "$TARGET_RPS"
        --classic-rps 0
        --duration "$DURATION"
        --ramp-up "$RAMP_UP"
        --metrics-file "$OUT/client-$MODE.ndjson")
    if [ -n "$STATE_FILE" ]; then
        bench_args=("${bench_args[@]}" --state-file "$STATE_FILE")
    fi

    echo "running: $BINARY ${bench_args[*]}"
    if ! "$BINARY" "${bench_args[@]}"; then
        die "bench failed for mode $MODE — stopping, the bundle is incomplete"
    fi

    WINDOW_END=$(now_rfc3339)

    curl -s -m 30 "$METRICS_URL" > "$OUT/metrics-$MODE.prom"
    [ -s "$OUT/metrics-$MODE.prom" ] || die "empty scrape from $METRICS_URL"

    RPC_LOG=$(docker exec "$CONTAINER" sh -c \
        'ls -t /var/log/supervisor/stellar-rpc-stdout* | head -1' | tr -d '\r')
    [ -n "$RPC_LOG" ] || die "no stellar-rpc stdout log found in $CONTAINER"
    docker cp "$CONTAINER:$RPC_LOG" "$OUT/rpc-log-$MODE.log"

    EXPECTED=$(NDJSON="$OUT/client-$MODE.ndjson" WORKLOAD="$MODE" python3 -c '
import json, os, sys
target = os.environ["WORKLOAD"]
with open(os.environ["NDJSON"]) as fh:
    for line in fh:
        line = line.strip()
        if not line:
            continue
        try:
            record = json.loads(line)
        except ValueError:
            continue
        if record.get("record_type") == "summary" and record.get("workload") == target:
            value = record.get("submission_submitted")
            if value is not None:
                sys.stdout.write(str(int(value)))
            break
')

    harvest_args=(--log "$OUT/rpc-log-$MODE.log"
        --metrics "$OUT/metrics-$MODE.prom"
        --mode "$MODE"
        --window-start "$WINDOW_START"
        --window-end "$WINDOW_END"
        --out "$OUT/summary-$MODE.json")
    if [ -n "$EXPECTED" ]; then
        harvest_args=("${harvest_args[@]}" --expected-count "$EXPECTED")
    else
        echo "warning: no submission_submitted for workload $MODE in the client NDJSON" >&2
    fi

    python3 "$SCRIPT_DIR/harvest.py" "${harvest_args[@]}"

    SUMMARY="$OUT/summary-$MODE.json" python3 -c '
import json, os
with open(os.environ["SUMMARY"]) as fh:
    summary = json.load(fh)
handler = summary["handler"]


def ms(value):
    return "n/a" if value is None else "%.3f ms" % (value / 1e6)


print("%-14s count=%-6d p50=%-12s p99=%-12s warnings=%d" % (
    summary["run"]["mode"], handler["count"],
    ms(handler["p50_ns"]), ms(handler["p99_ns"]), len(summary["warnings"])))
for warning in summary["warnings"]:
    print("  ! %s" % warning)
'
done

echo
echo "bundle written to $OUT"
