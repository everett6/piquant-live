#!/usr/bin/env bash
# Build the arm64 binary from the current commit and install it on the Pi, safely.
# Written after the 2026-11-13 outage: a hard reset right after an install left a 0-byte binary and both Pi services
# down for 18 hours (research/NEXT_TESTS.md).
#   scripts/deploy_pi.sh             # install, restart the paper engine only (the recorder keeps its old process)
#   scripts/deploy_pi.sh --collect   # also restart the tick recorder (a gap of a few seconds in the recording)
#   scripts/deploy_pi.sh --dry-run   # build, copy and checksum only; nothing is installed or restarted
#   PI=everett@192.168.0.109 scripts/deploy_pi.sh
# Steps: refuse uncommitted Go code -> build -> copy -> sync -> sha256 on the Pi's disk -> keep the old binary ->
# install by atomic rename -> sync -> sha256 again -> restart -> check both services and the recorder for 90 s ->
# roll back to the old binary if any check fails.
set -euo pipefail
PI=${PI:-everett@raspberrypi.local}
COLLECT=0; DRY=0
for a in "$@"; do
  case "$a" in
    --collect) COLLECT=1 ;;
    --dry-run) DRY=1 ;;
    *) echo "unknown option: $a" >&2; exit 2 ;;
  esac
done
cd "$(git rev-parse --show-toplevel)"
export PATH="$HOME/.local/go/bin:$PATH"
if ! git diff --quiet HEAD -- cmd internal go.mod go.sum; then
  echo "refusing: uncommitted changes under cmd/ internal/ or go.mod (commit first)" >&2; exit 1
fi
V=$(git rev-parse --short HEAD)
OUT=$(mktemp -d)/piquant-arm64
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$V" -o "$OUT" ./cmd/piquant
H=$(sha256sum "$OUT" | cut -d' ' -f1)
SIZE=$(stat -c %s "$OUT")
[ "$SIZE" -gt 1000000 ] || { echo "refusing: built binary is only $SIZE bytes" >&2; exit 1; }
echo "built $V ($SIZE bytes, sha256 ${H:0:12})"

SSH="ssh -o ConnectTimeout=10 $PI"
echo "before: $($SSH 'systemctl is-active piquant piquant-collect | tr "\n" " "; /usr/local/bin/piquant version 2>/dev/null | head -1')"
scp -q "$OUT" "$PI:/tmp/piquant.new"
$SSH "sync; test \"\$(sha256sum /tmp/piquant.new | cut -d' ' -f1)\" = '$H' && /tmp/piquant.new version | head -1 | grep -q '$V'" \
  || { echo "FAILED: the copy on the Pi does not match the build; nothing was installed" >&2; exit 1; }
echo "copy verified on the Pi's disk"
if [ "$DRY" = 1 ]; then echo "dry run: nothing installed"; exit 0; fi

UNITS="piquant"; [ "$COLLECT" = 1 ] && UNITS="piquant piquant-collect"
$SSH "COLLECT=$COLLECT UNITS='$UNITS' H='$H' V='$V' bash -s" <<'REMOTE'
set -u
BIN=/usr/local/bin/piquant
PREV=$BIN.rollback
fresh() {  # recorder status written in the last 150 s
  python3 - <<'PY'
import datetime, json, os, sys
s = json.load(open(os.path.expanduser("~/piquant-collect/status.json")))
t = datetime.datetime.fromisoformat(s["updated_at"][:26].rstrip("Z") + "+00:00")
sys.exit(0 if (datetime.datetime.now(datetime.timezone.utc) - t).total_seconds() < 150 else 1)
PY
}
healthy() {
  [ "$(systemctl is-active piquant)" = active ] && [ "$(systemctl is-active piquant-collect)" = active ] && fresh
}
if [ -s "$BIN" ]; then sudo cp -p "$BIN" "$PREV" && sync; else echo "warning: current binary is empty, no rollback copy"; fi
sudo install -m 755 /tmp/piquant.new "$BIN.new" && sync && sudo mv -f "$BIN.new" "$BIN" && sync
if [ "$(sha256sum "$BIN" | cut -d' ' -f1)" != "$H" ]; then
  echo "FAILED: installed binary does not match; rolling back"
  [ -s "$PREV" ] && sudo cp -p "$PREV" "$BIN" && sync
  exit 1
fi
sudo systemctl restart $UNITS
ok=0
for i in $(seq 1 18); do
  sleep 5
  if healthy; then ok=$((ok + 1)); else ok=0; fi
  [ "$ok" -ge 6 ] && break   # healthy for 30 s in a row
done
if [ "$ok" -ge 6 ]; then
  echo "OK: $($BIN version | head -1) installed; piquant and piquant-collect active, recorder fresh"
  echo "rollback copy kept at $PREV"
  exit 0
fi
echo "FAILED health check after restart: $(systemctl is-active piquant piquant-collect | tr '\n' ' ')"
if [ -s "$PREV" ]; then
  sudo cp -p "$PREV" "$BIN" && sync && sudo systemctl restart piquant piquant-collect
  sleep 10
  echo "rolled back to $($BIN version | head -1): $(systemctl is-active piquant piquant-collect | tr '\n' ' ')"
fi
exit 1
REMOTE
