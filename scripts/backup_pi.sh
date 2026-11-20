#!/usr/bin/env bash
# Back up live state, research shadow-queue artifacts, and config from the Pi over SSH.
# Usage:
#   scripts/backup_pi.sh [--dry-run] [--dest <dir>] [--keep <N>] [--help]
set -euo pipefail

CALLER_PWD=$(pwd)
DEST="$HOME/piquant-backups"
KEEP=14
DRY=0

usage() {
  cat <<'EOF'
Usage: scripts/backup_pi.sh [options]

Archive PiQuant state, shadow-queue results, and config from the Pi over SSH.

Options:
  --dry-run      Check SSH, print existing paths that would be archived and destination path
  --dest <dir>   Backup destination directory (default: $HOME/piquant-backups)
  --keep <N>     Number of newest backup pairs (.tar.gz + .sha256) to retain (default: 14)
  --help, -h     Show this help message
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dry-run)
      DRY=1
      shift
      ;;
    --dest)
      [ "$#" -ge 2 ] || { echo "missing argument for --dest" >&2; exit 2; }
      DEST="$2"
      shift 2
      ;;
    --dest=*)
      DEST="${1#--dest=}"
      shift
      ;;
    --keep)
      [ "$#" -ge 2 ] || { echo "missing argument for --keep" >&2; exit 2; }
      KEEP="$2"
      shift 2
      ;;
    --keep=*)
      KEEP="${1#--keep=}"
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

case "$DEST" in
  /*) ;;
  *) DEST="$CALLER_PWD/$DEST" ;;
esac

case "$KEEP" in
  ''|*[!0-9]*|0)
    echo "invalid --keep value: $KEEP (must be a positive integer)" >&2
    exit 2
    ;;
esac

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(git -C "$SCRIPT_DIR/.." rev-parse --show-toplevel)
cd "$ROOT"
. scripts/pi_host.sh
PI=$(pi_host) || { echo "Pi not reachable (tried \$PI, raspberrypi.local, cached and old IP)" >&2; exit 1; }

TARGETS=(
  "piquant-data/paper"
  "piquant-trend"
  "piquant-collect/carry"
  "PiQuant/piquant-live/research/quant_lab/results/auto_discover_state.json"
  "PiQuant/piquant-live/research/quant_lab/results/auto_discover_log.jsonl"
  "PiQuant/piquant-live/research/quant_lab/results/shadow_candidates.json"
  "PiQuant/piquant-live/research/quant_lab/results/shadow_paper_state.json"
  ".config/piquant"
)

SSH="ssh -o ConnectTimeout=10 $PI"
EXISTING_RAW=$($SSH "bash -s" -- "${TARGETS[@]}" <<'REMOTE'
set -euo pipefail
for p in "$@"; do
  if [ -e "$HOME/$p" ]; then
    printf '%s\n' "$p"
  fi
done
REMOTE
)

EXISTING=()
while IFS= read -r line; do
  [ -n "$line" ] && EXISTING+=("$line")
done <<< "$EXISTING_RAW"

TS=$(date -u +%Y%m%dT%H%M%SZ)
ARCHIVE="$DEST/piquant-pi-backup-$TS.tar.gz"

if [ "$DRY" = 1 ]; then
  echo "Pi host: $PI"
  echo "Destination: $ARCHIVE"
  if [ "${#EXISTING[@]}" -eq 0 ]; then
    echo "Existing paths to archive: (none)"
  else
    echo "Existing paths to archive:"
    for p in "${EXISTING[@]}"; do
      echo "  $p"
    done
  fi
  exit 0
fi

if [ "${#EXISTING[@]}" -eq 0 ]; then
  echo "no backup targets exist on $PI" >&2
  exit 1
fi

mkdir -p "$DEST"
TMP="${ARCHIVE}.tmp"
cleanup() {
  rm -f "$TMP"
}
trap cleanup EXIT

rc=0
$SSH "bash -s" -- "${EXISTING[@]}" <<'REMOTE' > "$TMP" || rc=$?
set -euo pipefail
rc=0
tar -C "$HOME" -czf - -- "$@" || rc=$?
if [ "$rc" -eq 1 ]; then
  echo "warning: tar reported a file changed while reading" >&2
  exit 0
fi
exit "$rc"
REMOTE

if [ "$rc" -ne 0 ]; then
  echo "backup archive transfer failed (exit $rc)" >&2
  exit "$rc"
fi

if ! [ -s "$TMP" ] || ! tar -tzf "$TMP" >/dev/null 2>&1; then
  echo "backup archive verification failed: $TMP" >&2
  exit 1
fi

chmod 600 "$TMP"
mv -f "$TMP" "$ARCHIVE"

SHA_FILE="${ARCHIVE}.sha256"
( cd "$DEST" && sha256sum "$(basename "$ARCHIVE")" > "$(basename "$SHA_FILE")" )
chmod 600 "$SHA_FILE"

shopt -s nullglob
RAW_BACKUPS=( "$DEST"/piquant-pi-backup-*.tar.gz )
BACKUPS=()
if [ "${#RAW_BACKUPS[@]}" -gt 0 ]; then
  while IFS= read -r b; do
    [ -n "$b" ] && BACKUPS+=("$b")
  done < <(printf '%s\n' "${RAW_BACKUPS[@]}" | LC_ALL=C sort)
fi

TOTAL=${#BACKUPS[@]}
if [ "$TOTAL" -gt "$KEEP" ]; then
  DROP=$((TOTAL - KEEP))
  for ((i = 0; i < DROP; i++)); do
    rm -f "${BACKUPS[$i]}" "${BACKUPS[$i]}.sha256"
  done
fi

echo "backup written: $ARCHIVE (sha256: $SHA_FILE, retained <= $KEEP pairs)"
