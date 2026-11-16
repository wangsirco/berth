#!/usr/bin/env python3
"""Merge backend message entries into internal/panel/i18n/{zh-CN,zh-TW,en}.json.

Usage: scripts/i18n_add.py entries.tsv   (lines: key<TAB>zh-CN<TAB>zh-TW<TAB>en)
Existing keys are overwritten; files stay sorted.
"""
import json, sys, pathlib
root = pathlib.Path(__file__).resolve().parent.parent / "internal/panel/i18n"
root.mkdir(parents=True, exist_ok=True)
locs = ["zh-CN", "zh-TW", "en"]
data = {}
for l in locs:
    p = root / f"{l}.json"
    data[l] = json.loads(p.read_text()) if p.exists() else {}
for line in open(sys.argv[1], encoding="utf-8"):
    line = line.rstrip("\n")
    if not line.strip() or line.startswith("#"):
        continue
    parts = line.split("\t")
    if len(parts) != 4:
        sys.exit(f"bad line (need 4 tab-separated fields): {line!r}")
    for l, v in zip(locs, parts[1:]):
        data[l][parts[0]] = v
for l in locs:
    (root / f"{l}.json").write_text(json.dumps(dict(sorted(data[l].items())), ensure_ascii=False, indent=1) + "\n")
print({l: len(d) for l, d in data.items()})
