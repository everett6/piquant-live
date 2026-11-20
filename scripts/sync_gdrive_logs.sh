#!/usr/bin/env bash
# Pull completed UTC daily market/research logs from the Raspberry Pi 5 (excluding today's
# active files) and archive them + Pi backups + daily CSVs to Google Drive when rclone `gdrive:`
# is configured (never disturbs live Pi 5 collection).
# Usage:
#   scripts/sync_gdrive_logs.sh [--dry-run] [--skip-pull] [--archive-dir <dir>]
set -euo pipefail
export PATH="$HOME/.local/bin:/usr/local/bin:$PATH"

DRY=0
SKIP_PULL=0
ARCHIVE_DIR="$HOME/piquant-collect/pi-archive"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run) DRY=1; shift ;;
    --skip-pull) SKIP_PULL=1; shift ;;
    --archive-dir)
      [ "$#" -ge 2 ] || { echo "missing argument for --archive-dir" >&2; exit 2; }
      ARCHIVE_DIR="$2"; shift 2 ;;
    -h|--help)
      echo "Usage: scripts/sync_gdrive_logs.sh [--dry-run] [--skip-pull] [--archive-dir <dir>]"
      exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(git -C "$SCRIPT_DIR/.." rev-parse --show-toplevel)
cd "$ROOT"

TODAY_UTC=$(date -u +%Y-%m-%d)

if [ "$SKIP_PULL" -eq 0 ]; then
  . scripts/pi_host.sh
  PI=$(pi_host) || { echo "Pi not reachable; skipping pull" >&2; exit 1; }
  mkdir -p "$ARCHIVE_DIR"
  RSYNC_FLAGS=(-az "--exclude=${TODAY_UTC}*")
  [ "$DRY" -eq 1 ] && RSYNC_FLAGS+=(--dry-run --itemize-changes)
  PULL_SOURCES=(
    "$PI:piquant-collect/prices"
    "$PI:piquant-collect/ticks"
    "$PI:piquant-collect/book"
    "$PI:piquant-collect/news"
    "$PI:PiQuant/piquant-live/research/quant_lab/results/auto_discover_log.jsonl"
  )
  if ssh -o ConnectTimeout=10 -o BatchMode=yes "$PI" 'test -d "$HOME/piquant-collect/ticks_backfill"' >/dev/null 2>&1; then
    PULL_SOURCES+=("$PI:piquant-collect/ticks_backfill")
  fi
  rsync "${RSYNC_FLAGS[@]}" "${PULL_SOURCES[@]}" "$ARCHIVE_DIR/"
fi

if command -v rclone >/dev/null 2>&1 && rclone listremotes 2>/dev/null | grep -qx 'gdrive:'; then
  if ! rclone lsd gdrive: --contimeout 5s --timeout 10s >/dev/null 2>&1; then
    echo "sync_gdrive_logs: skipping Drive upload (run '~/.local/bin/rclone config reconnect gdrive:' to authorize)"
    exit 0
  fi
  RCLONE_FLAGS=(copy --fast-list --transfers 4 --ignore-existing)
  [ "$DRY" -eq 1 ] && RCLONE_FLAGS+=(--dry-run)
  rclone "${RCLONE_FLAGS[@]}" "$ARCHIVE_DIR" "gdrive:PiQuant/LiveLogs"
  [ -d "$HOME/piquant-backups" ] && rclone "${RCLONE_FLAGS[@]}" "$HOME/piquant-backups" "gdrive:PiQuant/Backups"
  if [ -d "$HOME/piquant-collect/daily_csv" ]; then
    CSV_FLAGS=(copy --update)
    [ "$DRY" -eq 1 ] && CSV_FLAGS+=(--dry-run)
    rclone "${CSV_FLAGS[@]}" "$HOME/piquant-collect/daily_csv" "gdrive:PiQuant/Datasets"
  fi
  echo "sync_gdrive_logs: synced to rclone gdrive:PiQuant/"
else
  echo "sync_gdrive_logs: local archive updated at $ARCHIVE_DIR (configure 'rclone config' remote 'gdrive' for unattended Drive sync)"
fi
