#!/usr/bin/env bash
# Make the Pi recover by itself and leave evidence when it fails. Prints the plan by default; changes the Pi only
# with --apply. Every change is a drop-in file that can be deleted to undo it (listed at the end).
#   scripts/pi_harden.sh            # show what would change
#   scripts/pi_harden.sh --apply    # apply it (asks for sudo on the Pi)
# What it does:
#  1. Persistent journal, capped at 300 MB: the cause of a crash survives the reboot (both outages so far had none).
#  2. Hardware watchdog (15 s): if the Pi freezes, the chip reboots it instead of it staying dead until someone
#     pulls the plug. On reboot both services start again on their own.
#  3. Services never give up restarting (no start-rate limit), 10 s apart.
#  4. The engine and the recorder run at normal priority, each pinned to its own reserved core (engine 3,
#     recorder 2; pinning at normal priority is safe). A real-time FIFO/RR policy on isolated cores (as in
#     docs/KERNEL_TUNING.md) can starve the kernel's own threads on those cores and hang the whole Pi; nothing in
#     the engine needs it now that it only paper-trades on candles.
# It does not change the boot command line, the power supply or the network; the doctor report shows those.
set -euo pipefail
cd "$(dirname "$0")/.."
APPLY=0; [ "${1:-}" = "--apply" ] && APPLY=1
. scripts/pi_host.sh
HOST=$(pi_host) || { echo "Pi not reachable. It needs a hands-on power cycle first." >&2; exit 1; }
echo "Pi: $HOST"
ssh -t -o ConnectTimeout=10 "$HOST" "APPLY=$APPLY bash -s" <<'REMOTE'
set -euo pipefail
put() {  # put <path> <content>
  if [ "$APPLY" = 1 ]; then
    sudo mkdir -p "$(dirname "$1")"; printf '%s\n' "$2" | sudo tee "$1" >/dev/null; echo "  wrote $1"
  else
    echo "  would write $1:"; printf '%s\n' "$2" | sed 's/^/      /'
  fi
}
echo "1. persistent journal"
put /etc/systemd/journald.conf.d/piquant-persistent.conf "[Journal]
Storage=persistent
SystemMaxUse=300M"
echo "2. hardware watchdog"
put /etc/systemd/system.conf.d/piquant-watchdog.conf "[Manager]
RuntimeWatchdogSec=15
RebootWatchdogSec=2min"
for s in piquant piquant-collect; do
  echo "3/4. $s"
  # Normal priority, but each service pinned to one of the cores the boot line reserves (isolcpus=2,3), so
  # the desktop, browser and Pi-hole on cores 0-1 never delay the recorder or the engine.
  core=3; [ "$s" = piquant-collect ] && core=2
  extra="
CPUSchedulingPolicy=other
CPUSchedulingPriority=0
CPUAffinity=$core"
  # time-sync.target: no RTC battery, so never record or trade on the stale boot clock (needs
  # systemd-time-wait-sync enabled, checked below).
  put /etc/systemd/system/$s.service.d/zz-piquant-resilience.conf "[Unit]
StartLimitIntervalSec=0
Wants=time-sync.target
After=time-sync.target

[Service]
Restart=always
RestartSec=10$extra"
done
if [ "$APPLY" = 1 ]; then
  sudo systemctl enable systemd-time-wait-sync.service
  sudo mkdir -p /var/log/journal && sudo systemd-tmpfiles --create --prefix /var/log/journal
  sudo systemctl restart systemd-journald
  sudo systemctl daemon-reexec    # picks up the watchdog setting
  sudo systemctl daemon-reload
  sudo systemctl restart piquant  # the recorder keeps its process; its drop-in applies at its next restart
  sleep 5
  echo "now: piquant $(systemctl is-active piquant), piquant-collect $(systemctl is-active piquant-collect), watchdog $(systemctl show -p RuntimeWatchdogUSec --value)"
  echo "undo: sudo rm /etc/systemd/journald.conf.d/piquant-persistent.conf /etc/systemd/system.conf.d/piquant-watchdog.conf /etc/systemd/system/piquant*.service.d/zz-piquant-resilience.conf && sudo systemctl daemon-reexec"
else
  echo "nothing changed (run with --apply)"
fi
REMOTE
