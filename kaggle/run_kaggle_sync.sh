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
mkdir -p "$KERNEL_DIR" "$KERNEL_DIR/output"
REPO_NB="/home/everett/PiQuant/piquant-live/kaggle/piquant_optimizer.ipynb"
if [ -f "$REPO_NB" ]; then
    cp "$REPO_NB" "$KERNEL_DIR/piquant_optimizer.ipynb"
fi

# 3. Trigger Kaggle kernel run via API
echo "[*] Pushing optimization kernel to Kaggle Cloud..." >> "$LOG_FILE"
"$VENV_BIN/kaggle" kernels push -p "$KERNEL_DIR" >> "$LOG_FILE" 2>&1 || true

# 4. Check kernel status
echo "[*] Checking kernel run status..." >> "$LOG_FILE"
STATUS=$("$VENV_BIN/kaggle" kernels status everetts12/piquant-optimizer 2>&1 || true)
echo "$STATUS" >> "$LOG_FILE"

# 5. If complete, pull optimal params
if echo "$STATUS" | grep -qi "COMPLETE"; then
    echo "[*] Pulling updated optimal parameters from Kaggle..." >> "$LOG_FILE"
    "$VENV_BIN/kaggle" kernels output everetts12/piquant-optimizer -p "$KERNEL_DIR/output" -o >> "$LOG_FILE" 2>&1 || true
    if [ -f "$KERNEL_DIR/output/optimal_params.json" ]; then
        CONFIG_DEST="/home/everett/piquant-data/config"
        mkdir -p "$CONFIG_DEST"
        cp "$KERNEL_DIR/output/optimal_params.json" "$CONFIG_DEST/optimal_params.json.tmp"
        mv "$CONFIG_DEST/optimal_params.json.tmp" "$CONFIG_DEST/optimal_params.json"
        echo "[✓] Successfully synced optimal parameters!" >> "$LOG_FILE"
    fi
fi

echo "[$(date '+%Y-%m-%d %H:%M:%S')] Kaggle cloud dispatch completed." >> "$LOG_FILE"
