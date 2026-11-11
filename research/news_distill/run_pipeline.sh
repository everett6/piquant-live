#!/usr/bin/env bash
# Wait for bulk teacher labels, then rebuild labels, fine-tune LFM3, evaluate, merge and quantize to GGUF.
set -euo pipefail
PY="${PY:-/home/everett/Gemma 4 12B Distill/.venv/bin/python}"
DIR="${DIR:-$HOME/piquant-collect}"
OUT="${OUT:-$HOME/models/piquant-news-lfm3-v1}"
LLAMA="${LLAMA:-$HOME/llama.cpp-lfm3}"
HERE="$(cd "$(dirname "$0")" && pwd)"
while pgrep -f -- "--input $DIR/train/teacher_input.jsonl" >/dev/null; do sleep 60; done
echo "[pipeline] teacher labels: $(zcat "$DIR"/teacher/*.jsonl.gz | wc -l)"
"$PY" -I "$HERE/build_labels.py" --dir "$DIR"
PYTORCH_CUDA_ALLOC_CONF=expandable_segments:True "$PY" -I "$HERE/train_lfm3.py" --dir "$DIR" --out "$OUT" --merge
(cd "$LLAMA" && PYTHONPATH="$LLAMA/gguf-py:$LLAMA" "$PY" convert_hf_to_gguf.py "$OUT/merged" --outtype bf16 --outfile "$OUT/piquant-news-lfm3-v1-bf16.gguf")
"$LLAMA/build/bin/llama-quantize" "$OUT/piquant-news-lfm3-v1-bf16.gguf" "$OUT/piquant-news-lfm3-v1-Q4_K_M.gguf" Q4_K_M
ls -la "$OUT"
echo "[pipeline] done"
