#!/usr/bin/env python3
"""Write apptest results into the catalog's verified blocks.

  python3 scripts/apps/apply_results.py results.json
"""
import json, os, re, sys

ROOT = os.path.join(os.path.dirname(__file__), "..", "..", "internal", "appstore", "apps")
results = json.load(open(sys.argv[1]))
for r in results:
    p = os.path.join(ROOT, r["id"], "berth.yaml")
    if not os.path.exists(p):
        continue
    if r["result"] == "ok":
        v = {"arm64_16k": "ok", "at": r["at"]}
    elif r["result"] == "crash":
        note = "runs in 4K compatibility mode" if r.get("krun") == "ok" else r.get("note", "")
        v = {"arm64_16k": "crash", "at": r["at"], "note": note[:200]}
    else:
        v = {"arm64_16k": "untested", "at": r["at"], "note": ("test failed: " + r.get("note", ""))[:200]}
    txt = open(p, encoding="utf-8").read()
    txt = re.sub(r"^verified: .*$", lambda _m: "verified: " + json.dumps(v, ensure_ascii=False), txt, count=1, flags=re.M)
    open(p, "w", encoding="utf-8").write(txt)
    print(f'{r["id"]:18} {v["arm64_16k"]}')
