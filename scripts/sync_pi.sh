#!/usr/bin/env bash
# Copy this repo's code to the Pi's research copy (~/PiQuant/piquant-live), so Pi runs use the same code.
# No .git, caches or graphify output. Refuses if research/ has uncommitted changes, so every Pi run maps to a
# commit (a pre-registered run must use committed code). PI defaults to raspberrypi.local.
#   scripts/sync_pi.sh            # sync
#   PI=everett@192.168.0.109 scripts/sync_pi.sh
set -euo pipefail
PI=${PI:-everett@raspberrypi.local}
cd "$(git rev-parse --show-toplevel)"
if ! git diff --quiet HEAD -- research scripts; then
  echo "refusing: uncommitted changes under research/ or scripts/ (commit first)" >&2; exit 1
fi
ssh -o ConnectTimeout=10 "$PI" 'mkdir -p ~/PiQuant/piquant-live'
rsync -a --delete --exclude .git --exclude __pycache__ --exclude graphify-out ./ "$PI:PiQuant/piquant-live/"
c=$(git rev-parse --short HEAD)
ssh "$PI" "echo $c > ~/PiQuant/piquant-live/.synced_commit"
echo "synced $c to $PI"
