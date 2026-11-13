#!/usr/bin/env bash
# Pack a finished month of tick/book recording into one zstd archive, or unpack it again for a test.
#
#   collect_archive.sh pack    YYYY-MM [COLLECT_DIR]   # ticks/ + book/ days of that month -> archive/YYYY-MM.tar.zst
#   collect_archive.sh restore YYYY-MM [COLLECT_DIR]   # archive -> the usual <kind>/<feed>/<day>.jsonl.gz files
#   collect_archive.sh drop    YYYY-MM [COLLECT_DIR]   # delete the restored day files again (archive stays)
#
# The archive holds the plain JSONL (zstd -19 --long sees across days: ~25% smaller than the daily gzip files).
# Restore writes fast gzip files so every reader (run_round17/18, dq_ticks) works unchanged.
# pack refuses the current month and any month listed in COLLECT_DIR/KEEP_MONTHS (data an open pre-registered
# test still reads). It deletes the originals only after the archive decompresses to identical content.
set -euo pipefail

cmd=${1:?pack|restore|drop}
month=${2:?YYYY-MM}
dir=${3:-$HOME/piquant-collect}
[[ $month =~ ^[0-9]{4}-[0-9]{2}$ ]] || { echo "month must be YYYY-MM" >&2; exit 2; }
arc="$dir/archive/$month.tar.zst"
cd "$dir"

day_files() { find ticks book -name "$month-[0-3][0-9].jsonl.gz" 2>/dev/null | sort; }

case $cmd in
pack)
  [[ $month < $(date -u +%Y-%m) ]] || { echo "refusing: $month is not a finished month" >&2; exit 1; }
  if [[ -f KEEP_MONTHS ]] && grep -qx "$month" KEEP_MONTHS; then
    echo "refusing: $month is listed in KEEP_MONTHS (an open test reads it)" >&2; exit 1
  fi
  [[ ! -e $arc ]] || { echo "refusing: $arc already exists" >&2; exit 1; }
  mapfile -t files < <(day_files)
  ((${#files[@]})) || { echo "no files for $month" >&2; exit 1; }
  tmp=$(mktemp -d "$dir/.pack.XXXX"); trap 'rm -rf "$tmp"' EXIT
  for f in "${files[@]}"; do mkdir -p "$tmp/$(dirname "$f")"; zcat "$f" > "$tmp/${f%.gz}"; done
  mkdir -p archive
  (cd "$tmp" && find ticks book -type f | sort | tar -cf - -T -) | nice -n 19 zstd -q -19 --long=27 -T0 -o "$arc.part"
  # verify: the archive must decompress to exactly the content of the gzip files
  want=$(for f in "${files[@]}"; do printf '%s %s\n' "$(zcat "$f" | md5sum | cut -d' ' -f1)" "${f%.gz}"; done)
  chk=$(mktemp -d "$dir/.chk.XXXX"); trap 'rm -rf "$tmp" "$chk"' EXIT
  zstd -dq --long=27 -c "$arc.part" | tar -xf - -C "$chk"
  got=$(for f in "${files[@]}"; do printf '%s %s\n' "$(md5sum < "$chk/${f%.gz}" | cut -d' ' -f1)" "${f%.gz}"; done)
  [[ $want == "$got" ]] || { echo "verify FAILED, originals kept" >&2; rm -f "$arc.part"; exit 1; }
  mv "$arc.part" "$arc"
  before=$(du -cb "${files[@]}" | tail -1 | cut -f1)
  rm -f "${files[@]}"
  echo "packed ${#files[@]} files: $before -> $(stat -c %s "$arc") bytes ($arc)"
  ;;
restore)
  [[ -f $arc ]] || { echo "no archive $arc" >&2; exit 1; }
  tmp=$(mktemp -d "$dir/.restore.XXXX"); trap 'rm -rf "$tmp"' EXIT
  zstd -dq --long=27 -c "$arc" | tar -xf - -C "$tmp"
  n=0
  while IFS= read -r f; do
    dst="$dir/$f.gz"
    [[ -e $dst ]] && { echo "exists, kept: $dst"; continue; }
    mkdir -p "$(dirname "$dst")"; gzip -1 -c "$tmp/$f" > "$dst.part" && mv "$dst.part" "$dst"; n=$((n + 1))
  done < <(cd "$tmp" && find ticks book -type f | sort)
  echo "restored $n files for $month"
  ;;
drop)
  [[ -f $arc ]] || { echo "refusing: no archive for $month, the day files are the only copy" >&2; exit 1; }
  mapfile -t files < <(day_files)
  rm -f "${files[@]}"
  echo "dropped ${#files[@]} restored files for $month (archive kept: $arc)"
  ;;
*) echo "usage: $0 pack|restore|drop YYYY-MM [COLLECT_DIR]" >&2; exit 2 ;;
esac
