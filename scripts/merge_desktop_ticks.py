#!/usr/bin/env python3
"""One-off cutover (2026-11-13): merge the desktop's tick/book recording into the Pi's ~/piquant-collect.

Run on the Pi with both recorders stopped. IMPORT is a copy of the desktop's ticks/, book/ and
clock_offsets.jsonl. Days only the desktop has are moved in. A day both have is merged per feed in time order:
desktop rows before the Pi's first row, then the Pi file as is, then desktop rows after the Pi's last row.
Trades are deduplicated by trade ID; book snapshots split strictly by time, so nothing is counted twice. Clock checks
are unioned (the reader uses them as a set). Stdlib only.

Usage: merge_desktop_ticks.py IMPORT_DIR [COLLECT_DIR]
"""
import gzip
import io
import json
import os
import shutil
import sys


def rows(path):
    out = []
    with gzip.open(path, "rt") as f:
        try:
            for line in f:
                if line.strip():
                    out.append(line if line.endswith("\n") else line + "\n")
        except EOFError:  # truncated final member: keep what was read, as the Go reader does
            pass
    return out


def member(lines):
    buf = io.BytesIO()
    with gzip.GzipFile(fileobj=buf, mode="wb") as z:
        z.write("".join(lines).encode())
    return buf.getvalue()


def merge_day(desk, pi):
    """Return (bytes, n_before, n_after) for the merged file."""
    pr = rows(pi)
    dr = rows(desk)
    if not pr:
        return open(desk, "rb").read(), len(dr), 0
    pj = [json.loads(x) for x in pr]
    t0, t1 = pj[0]["t"], pj[-1]["t"]
    if "id" in pj[0]:  # trades: equal timestamps are common, so dedupe by trade ID
        ids = {r.get("id") for r in pj}
        dj = [(x, json.loads(x)) for x in dr]
        before = [x for x, r in dj if r["t"] <= t0 and r.get("id") not in ids]
        after = [x for x, r in dj if r["t"] >= t1 and r.get("id") not in ids]
    else:  # book snapshots: one per second, strict time split
        before = [x for x in dr if json.loads(x)["t"] < t0]
        after = [x for x in dr if json.loads(x)["t"] > t1]
    data = (member(before) if before else b"") + open(pi, "rb").read() + (member(after) if after else b"")
    return data, len(before), len(after)


def main():
    imp = sys.argv[1]
    col = sys.argv[2] if len(sys.argv) > 2 else os.path.expanduser("~/piquant-collect")
    for kind in ("ticks", "book"):
        for feed in sorted(os.listdir(os.path.join(imp, kind))):
            os.makedirs(os.path.join(col, kind, feed), exist_ok=True)
            for name in sorted(os.listdir(os.path.join(imp, kind, feed))):
                src, dst = os.path.join(imp, kind, feed, name), os.path.join(col, kind, feed, name)
                if not os.path.exists(dst):
                    shutil.copy2(src, dst)
                    print(f"{kind}/{feed}/{name}: desktop only, copied")
                    continue
                data, nb, na = merge_day(src, dst)
                tmp = dst + ".merge"
                with open(tmp, "wb") as f:
                    f.write(data)
                ts = [json.loads(x)["t"] for x in rows(tmp)]
                back = sum(a > b for a, b in zip(ts, ts[1:]))  # small natural jitter in exchange times is fine
                os.replace(tmp, dst)
                print(f"{kind}/{feed}/{name}: merged, {nb} desktop rows before the Pi's, {na} after, {len(ts)} total, {back} steps back in time")
    ck_imp, ck = os.path.join(imp, "clock_offsets.jsonl"), os.path.join(col, "clock_offsets.jsonl")
    if os.path.exists(ck_imp):
        lines = set(open(ck_imp).read().splitlines()) | (set(open(ck).read().splitlines()) if os.path.exists(ck)
                                                          else set())
        lines = sorted((x for x in lines if x.strip()), key=lambda x: json.loads(x)["at"])
        with open(ck + ".merge", "w") as f:
            f.write("\n".join(lines) + "\n")
        os.replace(ck + ".merge", ck)
        print(f"clock_offsets.jsonl: {len(lines)} checks after merge")


if __name__ == "__main__":
    main()
