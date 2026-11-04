# PiQuant — Dual-Node Deployment Guide

This guide details the deployment of PiQuant across two Raspberry Pi nodes:
* **Raspberry Pi 5** (`piquant-core.local`): Authoritative Research Computer.
* **Raspberry Pi 3B** (`piquant.local`): Dashboard & Lightweight Worker.

---

## Architecture Overview

```
                      +-----------------------------+
                      |   Raspberry Pi 3B           |
                      |   Hostname: piquant.local   |
                      |                             |
                      |   • Web Interface / Assets  |
                      |   • Status & Telemetry View |
                      |   • Searchable Log Viewer   |
                      |   • Lightweight Worker      |
                      +--------------+--------------+
                                     |
                                     | mDNS Discovery + Mutual Bearer Auth
                                     | Automatic Heartbeat & SSE Sync
                                     v
                      +-----------------------------+
                      |   Raspberry Pi 5            |
                      |   Hostname:                 |
                      |   piquant-core.local        |
                      |                             |
                      |   • Historical Replay Engine|
                      |   • Feature Generation      |
                      |   • Model Registry & Train  |
                      |   • Parameter Search        |
                      |   • Experiment Scheduler    |
                      |   • Source-of-Truth Store   |
                      |   • Fallback Web Dashboard  |
                      +-----------------------------+
```

---

## 1. Network & Hostname Zero-Configuration

Both devices use mDNS (Avahi) to eliminate manual IP configuration.

### On the Raspberry Pi 5:
```bash
sudo hostnamectl set-hostname piquant-core
sudo systemctl restart avahi-daemon
```

### On the Raspberry Pi 3B:
```bash
sudo hostnamectl set-hostname piquant
sudo systemctl restart avahi-daemon
```

Both boards run the UDP broadcast announcer on port 8090, advertising their presence (`role: engine` vs `role: dashboard`). When the shared peer token is configured, beacons are signed with HMAC-SHA256 to ensure node authentication.

---

## 2. Automated Installation

### On Raspberry Pi 5 (Research Computer)
Run:
```bash
cd /home/pi/piquant-live
sudo ./scripts/install.sh agent
```
Or via Makefile:
```bash
make install ROLE=agent
```

This:
1. Installs `/usr/local/bin/piquant`.
2. Creates system user `piquant`.
3. Creates directories `/var/lib/piquant/data`, `/var/log/piquant`, `/etc/piquant`.
4. Initializes secrets in `/etc/piquant/secrets` (0600 permissions).
5. Configures and starts systemd service `piquant-agent.service`.

### On Raspberry Pi 3B (Dashboard)
Run:
```bash
cd /home/pi/piquant-live
sudo ./scripts/install.sh dashboard
```
Or via Makefile:
```bash
make install ROLE=dashboard
```

This:
1. Compiles or installs the armv7-optimized binary to `/usr/local/bin/piquant`.
2. Sets memory bounds (`MemoryMax=500M`, `CPUQuota=85%`) to preserve RAM for the operating system.
3. Configures and starts systemd service `piquant-dashboard.service`.

---

## 3. Pairing and First-Time Provisioning

1. Obtain the shared peer token from the Pi 5:
   ```bash
   sudo piquant secrets get peer-token --dir /etc/piquant
   ```
2. On the Pi 3B, store the peer token:
   ```bash
   sudo piquant secrets set peer-token "<TOKEN_FROM_PI5>" --dir /etc/piquant
   sudo systemctl restart piquant-dashboard
   ```
3. The Pi 3B automatically discovers `piquant-core.local`, verifies HMAC signatures, and displays the research engine as **verified** and **live**.

---

## 4. Diagnostics & Verification

Run `piquant doctor` on either board at any time to verify system health without mutating any files:
```bash
piquant doctor
```

Output checks:
- OS architecture and kernel version
- Memory headroom and swap health
- CPU frequency governor & thermal limits
- Undervoltage / throttling flags
- Storage headroom and filesystem mounts
- Peer discovery and authenticated reachability

---

## 5. Fallback Dashboard

If the Raspberry Pi 3B is powered down or network connectivity is lost:
1. The Raspberry Pi 5 continues all research, parameter searches, and evaluations uninterrupted.
2. The user can access the fallback dashboard directly on the Pi 5 at:
   `http://piquant-core.local:8080` (or `http://<pi5-ip>:8080`).
3. When the Pi 3B comes back online, it reconnects automatically with exponential backoff.
