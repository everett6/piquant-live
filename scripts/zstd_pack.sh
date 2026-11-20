#!/usr/bin/env bash
# Losslessly compress large data files with zstd, one file at a time. Each original is removed only after the
# decompressed .zst hashes identically to it. Restore a file with:  zstd -d --long=27 FILE.zst
# Usage: scripts/zstd_pack.sh [-l LEVEL] FILE...
set -euo pipefail
level=19
if [[ "${1:-}" == "-l" ]]; then level=$2; shift 2; fi
for f in "$@"; do
  [[ -f "$f" && "$f" != *.zst ]] || continue
  before=$(stat -c%s "$f")
  want=$(sha256sum "$f" | cut -d' ' -f1)
  zstd -q -f -T0 --long=27 "-$level" "$f" -o "$f.zst.part"
  got=$(zstd -q -dc --long=27 "$f.zst.part" | sha256sum | cut -d' ' -f1)
  if [[ "$want" != "$got" ]]; then
    echo "MISMATCH $f, original kept" >&2
    rm -f "$f.zst.part"
    exit 1
  fi
  mv "$f.zst.part" "$f.zst"
  touch -r "$f" "$f.zst"
  rm "$f"
  after=$(stat -c%s "$f.zst")
  echo "$(date +%T) $f $((before / 1048576)) MB -> $((after / 1048576)) MB"
done
