# PIQuant-Live — build plan

Goal: a pure-Go, zero-allocation, microsecond-latency market feature + execution
engine for paper trading on **Raspberry Pi 5** (Cortex-A76, 4 cores, 8 GB),
developed and tested on x86_64 Linux.

## Status

| Phase | Scope | State |
|-------|-------|-------|
| 1 | Scaffold, event types, SPSC ring buffer, system abstractions | done (event, ringbuffer); system pending |
| 2 | Order book engine (L1–L20, int64), sequence auditor, synthetic replayer | pending |
| 3 | Zero-alloc features: imbalance, OFI, microprice, volatility, LOCF, Welford z-score | pending |
| 4 | Models: ridge / logistic / MLP, ARM64 NEON asm + generic fallback | pending |
| 5 | Execution (paper router, risk, kill switch), storage, deploy scripts | pending |
| 6 | RPi 5 hardening: watchdog, isolcpus, tmpfs, NVMe layout, cross-build checks | pending |
| 7 | Live public-feed connector (read-only), strategy doc | pending |

## Working rules

* Paper money only. The engine never holds exchange credentials for order entry;
  live mode is read-only market data, orders are simulated.
* `go test ./...` and `go test -race ./...` must pass on amd64; every package
  must cross-compile with `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./...`.
  Parquet writing is deliberately isolated behind a no-CGO encoder so this holds.
* Hot-path packages (ringbuffer, orderbook, features, models) carry benchmarks
  asserting `0 allocs/op`.
* RPi 5 target: cache line 64 B, 4 cores → cores 0/1 housekeeping, 2 ingest,
  3 inference/exec. `GOMEMLIMIT=2GiB`, `GOGC=off` during active sessions.
* No stubs: code that ships is complete and tested.

## Decisions

* Segment ring buffer instead of a byte ring: multi-package consumers (book,
  features, storage) each need the full event stream; a segment ring gives each
  consumer its own cursor over one shared, write-once backing store.
