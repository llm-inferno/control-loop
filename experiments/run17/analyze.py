#!/usr/bin/env python3
"""run17 cross-arm analysis: offered load vs throughput, replicas, occupancy.

Reads experiments/run17/<arm>-cycles.jsonl for each arm present and summarizes
by load regime (baseline ~250 RPM, peak ~1250 RPM). Aggregated per-deployment
metrics only (the cycle log has no per-pod breakdown); per-replica figures are
derived as aggregate/replicas.

CAVEAT: `throughput` is the evaluator window completion rate, which under-counts
under saturation (requests in-flight at the window edge are not counted). Treat
absolute offered-vs-throughput "deficit" as indicative, and lean on RELATIVE
cross-arm comparison at equal offered load.
"""
import glob, json, os, statistics as st

ARMS = [
    ("A (search ~50)", "armA-search-cycles.jsonl"),
    ("B-low (M*=32)", "armB-low32-cycles.jsonl"),
    ("B-high (M*=128)", "armB-high128-cycles.jsonl"),
]
HERE = os.path.dirname(os.path.abspath(__file__))


def load(fn):
    p = os.path.join(HERE, fn)
    if not os.path.exists(p):
        return None
    rows = []
    for line in open(p):
        line = line.strip()
        if not line:
            continue
        r = json.loads(line)
        for s in r.get("servers", []):
            rows.append({
                "cycle": r.get("cycle"), "rpm": s.get("rpm", 0), "thr": s.get("throughput", 0) or 0,
                "repl": s.get("replicas", 0), "M": s.get("maxBatch", 0),
                "occ": s.get("occPerReplica", 0), "itl": s.get("itl", 0), "ttft": s.get("ttft", 0),
            })
    return rows


def avg(xs):
    xs = [x for x in xs if x is not None]
    return st.mean(xs) if xs else float("nan")


def summarize(rows, lo, hi):
    g = [r for r in rows if lo <= r["rpm"] <= hi]
    if not g:
        return None
    offered, thr, repl = avg([r["rpm"] for r in g]), avg([r["thr"] for r in g]), avg([r["repl"] for r in g])
    return {
        "n": len(g), "offered": offered, "thr": thr,
        "deficit%": 100 * (offered - thr) / offered if offered else 0,
        "repl": repl, "occ": avg([r["occ"] for r in g]), "M": avg([r["M"] for r in g]),
        "perReplThr": thr / repl if repl else 0, "perReplOffered": offered / repl if repl else 0,
        "itl": avg([r["itl"] for r in g]), "ttft": avg([r["ttft"] for r in g]),
    }


def row(label, s):
    if not s:
        return f"  {label:18s}  (no cycles in band)"
    return (f"  {label:18s} n={s['n']:2d} | repl {s['repl']:4.1f} M* {s['M']:5.1f} occ/repl {s['occ']:5.1f}"
            f" | offered {s['offered']:6.0f} thr {s['thr']:6.0f} deficit {s['deficit%']:5.1f}%"
            f" | perRepl: off {s['perReplOffered']:5.0f} thr {s['perReplThr']:5.0f}"
            f" | ITL {s['itl']:4.1f} TTFT {s['ttft']:4.0f}")


for band, (lo, hi) in [("BASELINE (rpm 150-350)", (150, 350)), ("PEAK (rpm >1000)", (1000, 99999))]:
    print(f"\n=== {band} ===")
    for label, fn in ARMS:
        rows = load(fn)
        if rows is None:
            print(f"  {label:18s}  (not run yet)")
            continue
        print(row(label, summarize(rows, lo, hi)))
