#!/usr/bin/env bash
# Read-only health and post-mortem report for the Raspberry Pi 5. Changes nothing.
#   scripts/pi_doctor.sh            # PI=user@host to override the address
# Covers what has gone wrong so far or could: power (under-voltage, throttling), heat, the previous boot's last log
# lines (only if the journal is persistent), kernel errors (SD/NVMe, RCU stalls, hung tasks, OOM), disk, clock, the
# binary, services and restart counts, real-time scheduling on the engine, the recorder and the ledger state files.
set -uo pipefail
cd "$(dirname "$0")/.."
. scripts/pi_host.sh
HOST=$(pi_host) || { echo "Pi not reachable. It needs a hands-on power cycle; then run this again." >&2; exit 1; }
echo "Pi: $HOST"
ssh -o ConnectTimeout=10 "$HOST" 'bash -s' <<'REMOTE'
row() { printf '  %-26s %s\n' "$1" "$2"; }
echo "System"
row "up since" "$(uptime -s) ($(uptime -p))"
row "boots in journal" "$(journalctl --list-boots --no-pager 2>/dev/null | wc -l)"
row "journal storage" "$(test -d /var/log/journal && echo persistent || echo 'VOLATILE: last boot lost on reset')"
if command -v vcgencmd >/dev/null; then
  t=$(vcgencmd get_throttled | cut -d= -f2)
  flags=""
  for b in "0:UNDER-VOLTAGE now" "1:freq capped now" "2:THROTTLED now" "3:soft temp limit now" \
           "16:under-voltage since boot" "17:freq capped since boot" "18:throttled since boot" "19:soft temp limit since boot"; do
    (( (t >> ${b%%:*}) & 1 )) && flags="$flags ${b#*:};"
  done
  row "power/throttle" "$t${flags:- (none)}"
  row "temperature" "$(vcgencmd measure_temp | cut -d= -f2)"
  row "PSU max current" "$(vcgencmd get_config usb_max_current_enable 2>/dev/null | cut -d= -f2) (1 = 5A supply detected/allowed)"
fi
row "memory" "$(free -h | awk '/Mem/{print $3" used of "$2}')"
row "disk free (home)" "$(df -h ~ | awk 'NR==2{print $4" free, "$5" used"}')"
row "clock" "$(timedatectl show -p NTPSynchronized --value 2>/dev/null | sed 's/yes/NTP synced/;s/no/NOT synced/')"
row "kernel cmdline extras" "$(tr ' ' '\n' </proc/cmdline | grep -E 'isolcpus|nohz_full|rcu_nocbs|nosoftlockup' | tr '\n' ' ')"
row "hardware watchdog" "$(systemctl show -p RuntimeWatchdogUSec --value 2>/dev/null) (0 = off)"
echo "Kernel messages this boot (errors that matter)"
sudo dmesg 2>/dev/null | grep -i -E 'under-voltage|mmc.*(error|timeout)|nvme.*(error|timeout)|I/O error|EXT4-fs error|rcu.*stall|hung_task|blocked for more than|Out of memory|oom-kill|soft lockup' | tail -8 | sed 's/^/  /' || true
if [ -d /var/log/journal ]; then
  echo "Previous boot: last 12 lines"
  journalctl -b -1 -n 12 --no-pager 2>/dev/null | sed 's/^/  /'
fi
echo "Services"
for s in piquant piquant-collect cloudflared-tunnel; do
  row "$s" "$(systemctl is-active $s 2>/dev/null || echo inactive), restarts $(systemctl show -p NRestarts --value $s 2>/dev/null || echo '?'), since $(systemctl show -p ActiveEnterTimestamp --value $s 2>/dev/null || echo 'n/a')"
  row "  scheduling" "$(systemctl show -p CPUSchedulingPolicy -p CPUSchedulingPriority -p CPUAffinity --value $s 2>/dev/null | tr '\n' ' ')"
done
TIMER_ACTIVE=$(systemctl is-active piquant-discover.timer 2>/dev/null || echo inactive)
TIMER_NEXT=$(systemctl show -p NextElapseUSecRealtime --value piquant-discover.timer 2>/dev/null || echo n/a)
DISC_STATUS=$(systemctl show -p ExecMainStatus --value piquant-discover.service 2>/dev/null || echo n/a)
DISC_EXIT=$(systemctl show -p ExecMainExitTimestamp --value piquant-discover.service 2>/dev/null || echo never)
row "piquant-discover.timer" "$TIMER_ACTIVE (next: ${TIMER_NEXT:-n/a})"
row "  last discovery run" "exit=${DISC_STATUS:-n/a}, at=${DISC_EXIT:-never}"

B=/usr/local/bin/piquant
BIN_VER=$($B version 2>/dev/null | head -1)
SYNCED=$(cat ~/PiQuant/piquant-live/.synced_commit 2>/dev/null || echo none)
LOCKSTEP="UNKNOWN"
if [ "$SYNCED" != "none" ] && printf '%s' "$BIN_VER" | grep -q "$SYNCED"; then
  LOCKSTEP="MATCH ($SYNCED)"
else
  LOCKSTEP="MISMATCH (bin='$BIN_VER' synced='$SYNCED')"
fi
row "binary" "$(stat -c '%s bytes' $B 2>/dev/null) $BIN_VER"
row "commit lockstep" "$LOCKSTEP"
row "rollback binary" "$(stat -c '%s bytes' $B.rollback 2>/dev/null || echo none)"
echo "Data"
python3 - <<'PY'
import datetime as dt, json, os, glob
def row(k, v): print(f"  {k:26s} {v}")
now = dt.datetime.now(dt.timezone.utc)
try:
    s = json.load(open(os.path.expanduser("~/piquant-collect/ticks_status.json")))
    t = dt.datetime.fromisoformat(s["updated_at"][:26].rstrip("Z")).replace(tzinfo=dt.timezone.utc)
    row("recorder status age", f"{(now - t).total_seconds():.0f} s")
except Exception as e:
    row("recorder status", f"UNREADABLE {e!r}")
for p in [os.path.expanduser("~/piquant-data/trend/trend_state.json")] + glob.glob(os.path.expanduser("~/piquant-data/carry*/*.json")):
    try:
        st = json.load(open(p))
        extra = f"last {st.get('last_date')}, {len(st.get('ledgers', {}))} ledgers" if "ledgers" in st else "valid JSON"
        row(os.path.basename(p), f"{os.path.getsize(p)} bytes, {extra}")
    except Exception as e:
        row(os.path.basename(p), f"BROKEN ({e.__class__.__name__}); backup {'present' if os.path.exists(p + '.bak') else 'none'}")

res_dir = os.path.expanduser("~/PiQuant/piquant-live/research/quant_lab/results")
ad_p = os.path.join(res_dir, "auto_discover_state.json")
if os.path.exists(ad_p):
    try:
        ad = json.load(open(ad_p))
        row("auto_discover_state", f"trials={ad.get('n_trials')}, runs={ad.get('runs')}, acc={ad.get('accepted')}, last={str(ad.get('last_run_utc',''))[:19]}")
    except Exception as e:
        row("auto_discover_state", f"UNREADABLE ({e.__class__.__name__})")
else:
    row("auto_discover_state", "absent")

sq_p = os.path.join(res_dir, "shadow_candidates.json")
if os.path.exists(sq_p):
    try:
        sq = json.load(open(sq_p))
        row("shadow_candidates", f"{len(sq) if isinstance(sq, list) else '?'} queued")
    except Exception as e:
        row("shadow_candidates", f"UNREADABLE ({e.__class__.__name__})")
else:
    row("shadow_candidates", "absent")

sp_p = os.path.join(res_dir, "shadow_paper_state.json")
if os.path.exists(sp_p):
    try:
        sp = json.load(open(sp_p))
        row("shadow_paper_state", f"count={sp.get('count')}, health={sp.get('health_counts')}")
    except Exception as e:
        row("shadow_paper_state", f"UNREADABLE ({e.__class__.__name__})")
else:
    row("shadow_paper_state", "absent")

hwm_p = os.path.expanduser("~/piquant-data/paper/exec_hwm.json")
if os.path.exists(hwm_p):
    try:
        hwm = json.load(open(hwm_p))
        row("exec_hwm", f"peak=${hwm.get('peak_nav_usd')}, last=${hwm.get('last_nav_usd')}, date={hwm.get('daily_date_utc')}")
    except Exception as e:
        row("exec_hwm", f"UNREADABLE ({e.__class__.__name__})")
else:
    row("exec_hwm", "absent")
PY
REMOTE
