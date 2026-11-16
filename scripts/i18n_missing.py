#!/usr/bin/env python3
"""List t('key') keys used in the given UI files that zh-CN.json lacks."""
import re, json, sys, pathlib
root = pathlib.Path(__file__).resolve().parent.parent / "web"
d = json.load(open(root / "src/i18n/zh-CN.json"))
keys = set()
for f in sys.argv[1:]:
    keys |= set(re.findall(r"t\('([a-zA-Z0-9_.\-]+)'", open(f).read()))
for k in sorted(k for k in keys if k not in d and not k.endswith(('.', '_'))):
    print(k)
