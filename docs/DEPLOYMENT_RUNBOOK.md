# PiQuant Live — Operator Deployment & Go-Live Runbook

## 1. Raspberry Pi 5 Core-Pinning & Service Topology

Live workloads are strictly separated by CPU affinity and memory ceilings so research never interferes with market capture or paper/live execution:

| Unit | CPU Affinity | Priority / Cap | Schedule | Purpose |
|---|---|---|---|---|
| `piquant-collect.service` | Core `2` | Normal (`other`) | Continuous | Tick + top-10 book recorder (`11/11` feeds) |
| `piquant.service` | Core `3` | Normal (`other`) | Continuous | Autonomous paper trading & dashboard server (`:8080`) |
| `piquant-discover.service` | Cores `0 1` | `Nice=15`, `IOSchedulingClass=idle`, `MemoryMax=1800M` | Daily `00:35 UTC` (`piquant-discover.timer`) | `refresh_daily_csv.py --fetch-public` → LFM3 v2 `auto_discover.py --venue spot --llama-threads 2` → `shadow_paper_eval.py` |
| `cloudflared-tunnel.service` | Any | System default | Continuous | Tunnel to Pi dashboard (`:8080`) |

On the desktop (`everett` user), `piquant-backup.timer` runs daily at `01:15 UTC` to pull encrypted-permission (`0600`) `.tar.gz` + `.sha256` snapshots off the Pi to `~/piquant-backups`.

---

## 2. Routine Operations Commands

Run from the repo root `/home/everett/PiQuant/piquant-live`:

- **Full read-only Pi health check (power, temp, services, timer, lockstep commit, HWM, shadow queue):**
  ```bash
  ./scripts/pi_doctor.sh
  ```
- **Deploy committed & pushed `HEAD` to Raspberry Pi 5 (atomic swap, SHA-256 check, 30s health gate, research script sync, `.synced_commit` update):**
  ```bash
  ./scripts/deploy_pi.sh
  ```
- **Manual off-Pi state/config/research backup (retains 14 newest verified archives):**
  ```bash
  ./scripts/backup_pi.sh
  ```
- **Human recovery after a `KILL_LOCK` halt (must stop engine first):**
  ```bash
  ssh everett@raspberrypi.local 'sudo systemctl stop piquant && /usr/local/bin/piquant unlock --status'
  # Inspect reason, then reset only when safe:
  ssh everett@raspberrypi.local '/usr/local/bin/piquant unlock && sudo systemctl start piquant'
  ```

---

## 3. Go-Live Readiness Checklist (Before Enabling Real Capital)

Never set `PIQUANT_LIVE_ARM=YES` or attach live exchange keys until every item below is checked:

1. **Cloudflare Access SSO Gate:** Protect your Cloudflare Tunnel hostname with an interactive Cloudflare Zero Trust email/SSO Access policy in front of the Pi dashboard token (`~/.config/piquant/secrets`).
2. **30–60 Day Forward Burn-In:** Confirm stable `piquant`, `piquant-collect`, and `piquant-discover.timer` runs via `./scripts/pi_doctor.sh` with zero corruptions or missed trade-id gaps.
3. **Verify Venue & Fee Tier:**
   - **Coinbase Advanced Trade Spot:** Create a trade-only API key with **withdrawals disabled** and **IP allowlist enabled**, stored in `~/.config/piquant/coinbase.env` (`chmod 600`):
     ```env
     COINBASE_API_KEY=...
     COINBASE_API_SECRET=...
     ```
     Match `--round-trip-fee-bps` to your actual tier (`80`–`120` bps on starter volume tiers, `10`–`20` bps on VIP/high-volume maker tiers).
   - **Schwab ETF Alternative (`$0` commission):** Use `piquant schwab --portfolio q42_dualspeed` (`IBIT`/`ETHA`/`SGOV`) to avoid high retail crypto taker/maker fees on small balances.
4. **Dry-Run Pre-Flight with Real Balances & Live Latency:**
   ```bash
   ssh everett@raspberrypi.local '/usr/local/bin/piquant execute \
     --dry-run \
     --venue coinbase-spot \
     --portfolio q42_dualspeed \
     --max-daily-usd 250 \
     --max-order-usd 100 \
     --max-drawdown-pct 15 \
     --round-trip-fee-bps 80 \
     --tax-rate-pct 33.3 \
     --order-ttl 60s'
   ```
   Confirm `reconcile.matched=true`, realistic `ping_plan`, accurate `peak_nav_usd` / `drawdown_pct`, and sane target deltas.
5. **Dual-Factor Live Arming (Explicit Human Opt-In Only):**
   Live orders remain blocked (`ErrLiveNotArmed`, `post_only: true` always) unless **both** are explicitly supplied:
   ```bash
   PIQUANT_LIVE_ARM=YES /usr/local/bin/piquant execute \
     --dry-run=false \
     --confirm-live ARM_LIVE_EXECUTION \
     --venue coinbase-spot \
     --portfolio q42_dualspeed \
     --max-daily-usd 250 \
     --max-order-usd 100 \
     --max-drawdown-pct 15 \
     --round-trip-fee-bps 80 \
     --tax-rate-pct 33.3 \
     --order-ttl 60s
   ```
