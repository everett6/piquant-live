#!/usr/bin/env bash
# Print a reachable SSH target for the Pi, or exit 1. Sourced by pi_doctor.sh, pi_harden.sh, backup_pi.sh and deploy_pi.sh.
# Order: $PI if set; cached wired IP; wired Ethernet (192.168.0.109); Tailscale; raspberrypi.local fallback.
pi_host() {
  local cache="$HOME/.config/piquant/pi_host" c
  local cands=()
  [ -n "${PI:-}" ] && cands+=("$PI")
  [ -s "$cache" ] && cands+=("$(cat "$cache")")
  cands+=("everett@192.168.0.109" "everett@100.66.132.28" "everett@raspberrypi.local")
  for c in "${cands[@]}"; do
    if ssh -o ConnectTimeout=4 -o BatchMode=yes "$c" true 2>/dev/null; then
      local ip
      ip=$(ssh -o ConnectTimeout=4 -o BatchMode=yes "$c" "hostname -I | awk '{print \$1}'" 2>/dev/null)
      [ -n "$ip" ] && mkdir -p "$(dirname "$cache")" && echo "${c%@*}@$ip" > "$cache"
      echo "$c"
      return 0
    fi
  done
  return 1
}
if [ "${BASH_SOURCE[0]}" = "$0" ]; then pi_host || { echo "Pi not reachable (tried \$PI, raspberrypi.local, cached and old IP)" >&2; exit 1; }; fi
