#!/usr/bin/env bash
set -euo pipefail

ROOT="${PIQUANT_CLOUD_ROOT:-/data}"
COLLECT_DIR="$ROOT/collect"
DATA_DIR="$ROOT/piquant-data"
TREND_DIR="$DATA_DIR/trend"
CARRY_DIR="$COLLECT_DIR/carry"
CONFIG_DIR="$ROOT/config"
LOG_DIR="$ROOT/logs"
MACRO_DIR="$DATA_DIR/macro"
DTB3_PATH="$MACRO_DIR/DTB3.csv"

mkdir -p "$COLLECT_DIR" "$DATA_DIR" "$TREND_DIR" "$CARRY_DIR" "$CONFIG_DIR" "$LOG_DIR" "$MACRO_DIR"

# Fetch latest FRED 3-Month T-bill yield CSV non-fatally when missing or >24h old.
refresh_dtb3() {
  local need=0
  if [ ! -s "$DTB3_PATH" ]; then
    need=1
  elif find "$DTB3_PATH" -mtime +0 2>/dev/null | grep -q .; then
    need=1
  fi
  if [ "$need" -eq 1 ]; then
    curl -fsSL --max-time 15 "https://fred.stlouisfed.org/graph/fredgraph.csv?id=DTB3" -o "${DTB3_PATH}.tmp" 2>/dev/null \
      && mv "${DTB3_PATH}.tmp" "$DTB3_PATH" || rm -f "${DTB3_PATH}.tmp"
  fi
}

refresh_dtb3 &

export PIQUANT_DATA_DIR="$DATA_DIR"
export PIQUANT_LOG_DIR="$LOG_DIR"
export PIQUANT_CARRY_DIR="$CARRY_DIR"
export PIQUANT_DTB3_PATH="$DTB3_PATH"
export PIQUANT_AUTO_SETUP="${PIQUANT_AUTO_SETUP:-1}"

MIN_FREE_GB="${PIQUANT_MIN_FREE_GB:-0.5}"
BACKFILL_DAYS="${PIQUANT_BACKFILL_DAYS:-90}"
GDELT_DAYS="${PIQUANT_GDELT_DAYS:-14}"
LISTEN_ADDR=":${PORT:-8080}"

echo "[CLOUD] Starting piquant collect -> $COLLECT_DIR (min_free_gb=$MIN_FREE_GB)"
/usr/local/bin/piquant collect \
  --dir "$COLLECT_DIR" \
  --symbols "BTCUSDT,ETHUSDT" \
  --backfill-days "$BACKFILL_DAYS" \
  --gdelt-days "$GDELT_DAYS" \
  --min-free-gb "$MIN_FREE_GB" \
  --ticks=true \
  > "$LOG_DIR/collect.log" 2>&1 &
COLLECT_PID=$!

cleanup() {
  echo "[CLOUD] Shutting down cleanly..."
  kill -TERM "$COLLECT_PID" 2>/dev/null || true
  if [ -n "${DRYRUN_PID:-}" ]; then
    kill -TERM "$DRYRUN_PID" 2>/dev/null || true
    wait "$DRYRUN_PID" 2>/dev/null || true
  fi
  wait "$COLLECT_PID" 2>/dev/null || true
}
trap cleanup SIGTERM SIGINT

echo "[CLOUD] Starting piquant dryrun + dashboard -> $LISTEN_ADDR"
/usr/local/bin/piquant dry-run \
  -addr "$LISTEN_ADDR" \
  -secrets-dir "$CONFIG_DIR" \
  -data "$DATA_DIR" \
  -log-dir "$LOG_DIR" \
  -trend-dir "$TREND_DIR" \
  -carry-dir "$CARRY_DIR" \
  -dtb3 "$DTB3_PATH" \
  -no-discovery=true \
  -region us-futures \
  -symbols BTCUSDT,ETHUSDT \
  -capital 200 \
  -window 30d \
  -monthly-target 7 \
  -target-win-rate 60 \
  -max-drawdown 40 \
  -tax-rate 35.0 \
  -cloud-params=false \
  -flatten-on-shutdown=false \
  -mft-entries=false &
DRYRUN_PID=$!

wait "$DRYRUN_PID"
