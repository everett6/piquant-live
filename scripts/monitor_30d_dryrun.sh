#!/usr/bin/env bash
# ==============================================================================
# PiQuant 30-Day Dry Run Monitor & Auto-Recovery Script
# ==============================================================================
# Monitors the 30-day autonomous paper trading dry run, provides health checks,
# auto-restart on failure, and daily status reports.
# ==============================================================================
set -euo pipefail

SERVICE_NAME="piquant-dryrun-30d"
STATE_FILE="/var/lib/piquant/data/paper/dryrun_state.json"
JOURNAL_FILE="/var/lib/piquant/data/paper/dryrun_journal.jsonl"
LOG_DIR="/var/log/piquant"
REPORT_DIR="/var/lib/piquant/data/paper/reports"
MONITOR_LOG="${LOG_DIR}/dryrun_30d_monitor.log"
mkdir -p "$LOG_DIR" 2>/dev/null || true

# Colors for terminal output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

log() {
    local level="$1"
    local msg="$2"
    local timestamp="[$(date '+%Y-%m-%d %H:%M:%S')]"
    local colored_msg=""

    case "$level" in
        INFO)  colored_msg="${GREEN}${timestamp} [INFO]${NC} $msg" ;;
        WARN)  colored_msg="${YELLOW}${timestamp} [WARN]${NC} $msg" ;;
        ERROR) colored_msg="${RED}${timestamp} [ERROR]${NC} $msg" ;;
        *)     colored_msg="${BLUE}${timestamp} [${level}]${NC} $msg" ;;
    esac

    echo -e "$colored_msg"
    echo "${timestamp} [${level}] $msg" >> "$MONITOR_LOG"
}

check_service_active() {
    systemctl is-active --quiet "$SERVICE_NAME"
}

check_service_enabled() {
    systemctl is-enabled --quiet "$SERVICE_NAME"
}

get_service_status() {
    systemctl status "$SERVICE_NAME" --no-pager -l 2>/dev/null | head -n 20
}

check_state_file() {
    if [[ -f "$STATE_FILE" ]]; then
        local size=$(stat -c%s "$STATE_FILE" 2>/dev/null || echo 0)
        local mtime=$(stat -c%Y "$STATE_FILE" 2>/dev/null || echo 0)
        local now=$(date +%s)
        local age=$((now - mtime))

        if [[ $size -gt 100 ]]; then
            log INFO "State file OK: ${size} bytes, last updated $age seconds ago"
            if [[ $age -gt 300 ]]; then
                log ERROR "State file is stale (${age} seconds old)"
                return 1
            fi
            return 0
        else
            log WARN "State file exists but too small (${size} bytes) - may be corrupted"
            return 1
        fi
    else
        log WARN "State file not found at $STATE_FILE"
        return 1
    fi
}

check_journal_file() {
    if [[ -f "$JOURNAL_FILE" ]]; then
        local lines=$(wc -l < "$JOURNAL_FILE" 2>/dev/null || echo 0)
        local size=$(stat -c%s "$JOURNAL_FILE" 2>/dev/null || echo 0)
        log INFO "Journal file OK: ${lines} trades, ${size} bytes"
        return 0
    else
        log WARN "Journal file not found at $JOURNAL_FILE"
        return 1
    fi
}

check_disk_space() {
    local avail=$(df -h /var/lib/piquant | awk 'NR==2 {print $4}')
    local used_pct=$(df /var/lib/piquant | awk 'NR==2 {print $5}' | sed 's/%//')
    log INFO "Disk space: ${avail} available (${used_pct}% used)"

    if [[ $used_pct -gt 90 ]]; then
        log ERROR "Disk usage critical: ${used_pct}%"
        return 1
    elif [[ $used_pct -gt 80 ]]; then
        log WARN "Disk usage high: ${used_pct}%"
    fi
    return 0
}

check_memory() {
    local mem_info=$(free -m | awk 'NR==2 {print $3 " " $2}')
    local used=$(echo $mem_info | awk '{print $1}')
    local total=$(echo $mem_info | awk '{print $2}')
    local pct=$((used * 100 / total))
    log INFO "Memory: ${used}MB / ${total}MB (${pct}% used)"

    if [[ $pct -gt 90 ]]; then
        log ERROR "Memory usage critical: ${pct}%"
        return 1
    elif [[ $pct -gt 80 ]]; then
        log WARN "Memory usage high: ${pct}%"
    fi
    return 0
}

check_network() {
    if ping -q -c 1 -W 2 1.1.1.1 >/dev/null 2>&1 || ping -q -c 1 -W 2 8.8.8.8 >/dev/null 2>&1; then
        log INFO "Network connectivity: OK"
        return 0
    else
        log WARN "Network connectivity: FAILED (no internet)"
        return 1
    fi
}

print_dryrun_status() {
    if [[ -f "$STATE_FILE" ]]; then
        log INFO "=== 30-Day Dry Run Status ==="
        /usr/local/bin/piquant dry-run --status --data /var/lib/piquant/data 2>/dev/null | while IFS= read -r line; do
            log INFO "$line"
        done
    else
        log WARN "No state file found - dry run may not have started yet"
    fi
}

generate_daily_report() {
    log INFO "Generating daily EOD report..."
    /usr/local/bin/piquant dry-run --report --data /var/lib/piquant/data 2>/dev/null | head -n 100 | while IFS= read -r line; do
        log INFO "$line"
    done

    # Also save to reports directory
    local report_file="${REPORT_DIR}/dryrun_30d_$(date '+%Y%m%d').md"
    mkdir -p "$REPORT_DIR"
    /usr/local/bin/piquant dry-run --report --data /var/lib/piquant/data 2>/dev/null > "$report_file" 2>&1 || true
    log INFO "Report saved to $report_file"
}

restart_service() {
    log WARN "Restarting $SERVICE_NAME..."
    systemctl restart "$SERVICE_NAME"
    sleep 5
    if check_service_active; then
        log INFO "Service restarted successfully"
        return 0
    else
        log ERROR "Service restart FAILED"
        get_service_status | while IFS= read -r line; do log ERROR "$line"; done
        return 1
    fi
}

recover_from_corruption() {
    log WARN "Attempting recovery from corrupted state..."

    # Check for .bak file
    if [[ -f "${STATE_FILE}.bak" ]]; then
        log INFO "Found .bak backup, attempting restore..."
        cp "${STATE_FILE}.bak" "$STATE_FILE"
        log INFO "Restored from .bak"
        return 0
    fi

    # Check for journal replay
    if [[ -f "$JOURNAL_FILE" ]]; then
        log INFO "Journal file exists - engine will auto-replay on next start"
        return 0
    fi

    log ERROR "No recovery sources available (.bak or journal)"
    return 1
}

main() {
    local action="${1:-monitor}"

    case "$action" in
        monitor)
            log INFO "=== Starting 30-Day Dry Run Monitor ==="
            log INFO "Service: $SERVICE_NAME"
            log INFO "State: $STATE_FILE"
            log INFO "Journal: $JOURNAL_FILE"

            # Run checks
            local all_ok=true

            if check_service_active; then
                log INFO "Service status: ACTIVE"
            else
                log ERROR "Service status: INACTIVE"
                all_ok=false
            fi

            if check_service_enabled; then
                log INFO "Service enabled: YES"
            else
                log WARN "Service enabled: NO (will not auto-start on boot)"
            fi

            check_state_file || all_ok=false
            check_journal_file || all_ok=false
            check_disk_space || all_ok=false
            check_memory || all_ok=false
            check_network || all_ok=false

            print_dryrun_status

            if [[ "$all_ok" == "true" ]]; then
                log INFO "=== All health checks PASSED ==="
                return 0
            else
                log ERROR "=== Some health checks FAILED ==="
                return 1
            fi
            ;;

        status)
            print_dryrun_status
            ;;

        report)
            generate_daily_report
            ;;

        restart)
            restart_service
            ;;

        recover)
            recover_from_corruption
            ;;

        watch)
            log INFO "Starting continuous monitor (Ctrl+C to stop)..."
            while true; do
                if main monitor; then
                    :
                fi
                sleep 60
            done
            ;;

        install)
            log INFO "Installing 30-day dry run service..."
            if [[ -x /usr/local/bin/piquant ]]; then
                log INFO "Existing executable found at /usr/local/bin/piquant"
            else
                if [[ -x /home/everett/PiQuant/piquant-live/bin/piquant ]]; then
                    sudo install -Dm755 /home/everett/PiQuant/piquant-live/bin/piquant /usr/local/bin/piquant
                elif command -v go >/dev/null 2>&1; then
                    make -C /home/everett/PiQuant/piquant-live build
                    sudo install -Dm755 /home/everett/PiQuant/piquant-live/bin/piquant /usr/local/bin/piquant
                else
                    log ERROR "Cannot install piquant: no Go toolchain and no release binary"
                    exit 1
                fi
            fi
            if [[ ! -x /usr/local/bin/piquant ]]; then
                log ERROR "Installed piquant executable is not usable"
                exit 1
            fi
            sudo cp \
                /home/everett/PiQuant/piquant-live/deploy/piquant-dryrun-30d.service \
                /home/everett/PiQuant/piquant-live/deploy/piquant-dryrun-30d-monitor.service \
                /home/everett/PiQuant/piquant-live/deploy/piquant-dryrun-30d-monitor.timer \
                /home/everett/PiQuant/piquant-live/deploy/piquant-dryrun-30d-report.service \
                /home/everett/PiQuant/piquant-live/deploy/piquant-dryrun-30d-report.timer \
                /etc/systemd/system/
            sudo systemctl daemon-reload
            sudo systemctl enable "$SERVICE_NAME" piquant-dryrun-30d-monitor.timer piquant-dryrun-30d-report.timer
            log INFO "Dry-run, monitor, and report units installed and enabled."
            ;;

        start)
            log INFO "Starting 30-day dry run..."
            sudo systemctl start "$SERVICE_NAME"
            sleep 3
            main monitor
            ;;

        stop)
            log INFO "Stopping 30-day dry run..."
            sudo systemctl stop "$SERVICE_NAME"
            ;;

        logs)
            journalctl -u "$SERVICE_NAME" -f --no-pager
            ;;

        *)
            echo "Usage: $0 {monitor|status|report|restart|recover|watch|install|start|stop|logs}"
            echo ""
            echo "Commands:"
            echo "  monitor  - Run all health checks once (default)"
            echo "  status   - Print dry run status from state file"
            echo "  report   - Generate daily EOD tear-sheet report"
            echo "  restart  - Restart the service"
            echo "  recover  - Attempt recovery from corrupted state"
            echo "  watch    - Continuous monitoring every 60 seconds"
            echo "  install  - Install systemd service"
            echo "  start    - Start the 30-day dry run"
            echo "  stop     - Stop the dry run"
            echo "  logs     - Follow service logs"
            exit 1
            ;;
    esac
}

main "$@"