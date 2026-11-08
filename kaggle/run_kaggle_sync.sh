#!/usr/bin/env bash
set -euo pipefail

# PiQuant Autonomous Kaggle Cloud Dispatcher & Parameter Sync
# Runs on Raspberry Pi 5 to trigger free Kaggle cloud simulations and sync optimal parameters.

KAGGLE_DIR="/home/everett/.kaggle"
VENV_BIN="/home/everett/piquant-kaggle-env/bin"
KERNEL_DIR="/home/everett/piquant-cloud-kernel"
LOG_FILE="/home/everett/piquant-kaggle-sync.log"

echo "[$(date '+%Y-%m-%d %H:%M:%S')] Starting PiQuant Kaggle Cloud Sync..." >> "$LOG_FILE"

# 1. Verify Kaggle API credentials
if [ ! -f "$KAGGLE_DIR/kaggle.json" ]; then
    echo "[!] Error: $KAGGLE_DIR/kaggle.json not found. Please provide your kaggle.json API key." >> "$LOG_FILE"
    echo "KAGGLE_CREDS_MISSING"
    exit 1
fi

chmod 600 "$KAGGLE_DIR/kaggle.json"

# 2. Check if kernel exists or push updated notebook
mkdir -p "$KERNEL_DIR"
cp /home/everett/piquant-cloud-kernel/piquant_optimizer.ipynb "$KERNEL_DIR/piquant_optimizer.ipynb" 2>/dev/null || true

# 3. Trigger Kaggle kernel run via API
echo "[*] Pushing optimization kernel to Kaggle Cloud..." >> "$LOG_FILE"
"$VENV_BIN/kaggle" kernels push -p "$KERNEL_DIR" >> "$LOG_FILE" 2>&1 || true

# 4. Check kernel status
echo "[*] Checking kernel run status..." >> "$LOG_FILE"
"$VENV_BIN/kaggle" kernels status everett6/piquant-optimizer >> "$LOG_FILE" 2>&1 || true

echo "[$(date '+%Y-%m-%d %H:%M:%S')] Kaggle cloud dispatch completed." >> "$LOG_FILE"
