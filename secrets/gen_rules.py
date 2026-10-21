#!/usr/bin/env python3
"""Regenerates rules.json from gitleaks' default configuration (gitleaks.toml, MIT licence).

    python3 gen_rules.py path/to/gitleaks/config/gitleaks.toml > rules.json

Only what the scanner uses is kept: the patterns, their keywords, entropy floors and allowlists. File paths mean nothing
to a terminal recording, so path rules and path allowlists are dropped.
"""
import json
import sys
import tomllib

src = tomllib.load(open(sys.argv[1], "rb"))


def allow(a):
    out = {k: a[k] for k in ("regexes", "stopwords", "regexTarget", "condition") if a.get(k)}
    return out


def rule(r):
    out = {"id": r["id"], "description": r["description"], "regex": r["regex"]}
    for k in ("keywords", "entropy", "secretGroup"):
        if r.get(k):
            out[k] = r[k]
    al = [x for x in (allow(a) for a in r.get("allowlists", [])) if x]
    if al:
        out["allowlists"] = al
    return out


doc = {
    "source": "gitleaks " + src.get("title", "") + " (minVersion " + src.get("minVersion", "?") + ")",
    "allowlist": allow(src.get("allowlist", {})),
    "rules": [rule(r) for r in src["rules"] if r.get("regex")],
}
json.dump(doc, sys.stdout, separators=(",", ":"), ensure_ascii=False)
print()
