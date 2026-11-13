#!/usr/bin/env bash
# One-screen health check of the data pipeline: Pi services, today's recording, clock, disk, desktop mirror.
#   scripts/pipeline_status.sh     (PI defaults to raspberrypi.local)
set -uo pipefail
PI=${PI:-everett@raspberrypi.local}
ok() { printf '  %-28s %s\n' "$1" "$2"; }

echo "Pi ($PI)"
ssh -o ConnectTimeout=10 "$PI" 'bash -s' <<'REMOTE' || echo "  UNREACHABLE"
for s in piquant piquant-collect; do printf "  %-28s %s\n" "service $s" "$(systemctl is-active $s)"; done
printf "  %-28s %s\n" "network" "$(ip -br link | awk '$2=="UP"{printf "%s ",$1}')"
printf "  %-28s %s\n" "disk free" "$(df -h ~ | awk 'NR==2{print $4" ("$5" used)"}')"
printf "  %-28s %s\n" "temperature" "$(awk '{printf "%.0f C", $1/1000}' /sys/class/thermal/thermal_zone0/temp)"
printf "  %-28s %s\n" "alert.env" "$(test -s ~/.config/piquant/alert.env && echo present || echo MISSING - alerts go to log only)"
python3 - <<'PY'
import gzip, json, os, time, datetime as dt
c = os.path.expanduser("~/piquant-collect")
day = dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%d")
try:
    st = json.load(open(os.path.join(c, "ticks_status.json")))
    age = time.time() - dt.datetime.fromisoformat(st["updated_at"][:26].rstrip("Z")).replace(
        tzinfo=dt.timezone.utc).timestamp()
    print(f"  {'recorder status age':28s} {age:.0f} s" + ("  STALE" if age > 300 else ""))
    print(f"  {'venues connected':28s} " + " ".join(f"{k}={'up' if v else 'DOWN'}" for k, v in st["connected"].items()))
    gaps = {k: v for k, v in st.get("trade_id_gaps", {}).items() if v}
    print(f"  {'trade-ID gaps (session)':28s} {gaps or 'none'}")
except Exception as e:
    print(f"  {'recorder status':28s} UNREADABLE {e!r}")
worst = []
for feed in sorted(os.listdir(os.path.join(c, "book"))):
    p = os.path.join(c, "book", feed, day + ".jsonl.gz")
    if not os.path.exists(p):
        worst.append((feed, "no file today")); continue
    ts = []
    try:
        with gzip.open(p, "rt") as f:
            for line in f:
                ts.append(json.loads(line)["t"])
    except EOFError:
        pass
    lost = sum(b - a for a, b in zip(ts, ts[1:]) if b - a > 5000) / 60000
    if lost > 0:
        worst.append((feed, f"{lost:.1f} min in gaps >5 s"))
print(f"  {'book gaps today (UTC)':28s} " + ("none" if not worst else "; ".join(f"{a}: {b}" for a, b in worst)))
ck = os.path.join(c, "clock_offsets.jsonl")
if os.path.exists(ck):
    last = json.loads(open(ck).read().strip().splitlines()[-1])
    print(f"  {'last clock check':28s} {last['at'][:19]}Z offset {last['offset_ms']:.0f} ms ok={last['ok']}")
PY
REMOTE

echo "Desktop"
m=~/piquant-backups/pi-collect
if [[ -d $m ]]; then
  newest=$(find "$m" -type f -printf '%T@\n' | sort -n | tail -1)
  ok "tick-data mirror" "$(du -sh "$m" | cut -f1), newest file $(( ( $(date +%s) - ${newest%.*} ) / 3600 )) h old"
else
  ok "tick-data mirror" "MISSING ($m)"
fi
ok "collector" "$(systemctl --user is-active piquant-collect)"
ok "last backup line" "$(grep -E '\[BACKUP\]' ~/piquant-collect/collect.log 2>/dev/null | tail -1 | cut -c1-90)"
ok "disk free" "$(df -h ~ | awk 'NR==2{print $4" ("$5" used)"}')"
