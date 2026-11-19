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
for s in piquant piquant-collect; do
  row "$s" "$(systemctl is-active $s), restarts $(systemctl show -p NRestarts --value $s), since $(systemctl show -p ActiveEnterTimestamp --value $s)"
  row "  scheduling" "$(systemctl show -p CPUSchedulingPolicy -p CPUSchedulingPriority -p CPUAffinity --value $s | tr '\n' ' ')"
done
B=/usr/local/bin/piquant
row "binary" "$(stat -c '%s bytes' $B 2>/dev/null) $($B version 2>/dev/null | head -1)"
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
PY
REMOTE
