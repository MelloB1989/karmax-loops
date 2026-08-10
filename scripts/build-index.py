#!/usr/bin/env python3
"""Regenerate index.json from what is actually in the repo.

index.json is what `karmax loops browse` reads, so merging a PR is publishing.
Generating it means the catalogue cannot drift from the loops it lists — the
failure mode of a hand-maintained index is an entry pointing at a version that
no longer exists, which surfaces as a failed install for somebody else.

Workflow digests come from the built artifacts, which are attached to a GitHub
Release rather than committed. Pass --artifacts to say where they are:

    ./scripts/build-index.py --artifacts ./dist
"""

import argparse
import glob
import hashlib
import json
import os
import re
import sys

RELEASES = "https://github.com/MelloB1989/karmax-loops/releases/download"

# What a fresh KARMAX starts with. The recipes are embedded in the binary and
# wa-monitor is carried by the installer; this flag is what tells the CLI not to
# offer them as though they were missing.
SHIPPED = {"tech-news", "hot-sync", "profile-refresh", "daily-briefing", "wa-monitor"}


def sha256(path):
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def scalar(text, key):
    m = re.search(rf"^{key}:\s*(.+)$", text, re.M)
    return m.group(1).strip().strip('"') if m else ""


def folded(text, key):
    """Read a YAML folded block (`key: >`) as one line."""
    m = re.search(rf"^{key}:\s*>\s*\n((?:[ \t]+.*\n?)+)", text, re.M)
    if m:
        return " ".join(l.strip() for l in m.group(1).splitlines() if l.strip())
    return scalar(text, key)


def existing_pins():
    """The digests already published, keyed by (name, version)."""
    if not os.path.exists("index.json"):
        return {}
    try:
        index = json.load(open("index.json"))
    except json.JSONDecodeError:
        return {}
    return {(e.get("name"), e.get("version")): e["sha256"]
            for e in index.get("entries", []) if e.get("sha256")}


def workflows(artifacts_dir, strict):
    published = existing_pins()
    for path in sorted(glob.glob("workflows/*/loop.yaml")):
        name = os.path.basename(os.path.dirname(path))
        text = open(path).read()
        version = scalar(text, "version") or "1.0.0"

        entry = {
            "name": name,
            "kind": "workflow",
            "version": version,
            "description": folded(text, "description"),
            "author": scalar(text, "author") or "MelloB1989",
            "source": f"workflows/{name}",
            "artifact": f"{RELEASES}/{name}-{version}/{name}-{version}.kloop",
        }

        artifact = os.path.join(artifacts_dir, f"{name}-{version}.kloop")
        if os.path.exists(artifact):
            entry["sha256"] = sha256(artifact)
        elif published.get((name, version)):
            # The pin already in index.json, kept. Regenerating after building
            # ONE workflow used to silently unpin every other entry — which is
            # worse than never having pinned them, because the diff looks like
            # routine churn and nobody reviewing it sees a digest disappear.
            entry["sha256"] = published[(name, version)]
        elif strict:
            sys.exit(f"no built artifact for {name} {version} at {artifact}")
        else:
            print(f"warning: no artifact for {name} {version} and no pin on record; leaving it unpinned",
                  file=sys.stderr)

        # The tools it declares, so "this needs WhatsApp set up" is answerable
        # before installing rather than after it quietly does nothing.
        tools = re.findall(r"^\s*-\s*([a-z_]+\.[a-z_]+|google_workspace)\s*$", text, re.M)
        if tools:
            entry["requires"] = sorted(set(tools))
        if name in SHIPPED:
            entry["ship_with_karmax"] = True
        yield entry


def recipes():
    for path in sorted(glob.glob("recipes/*.yaml")):
        name = os.path.basename(path)[:-5]
        text = open(path).read()
        # The leading comment is the description. A recipe's first line is
        # written for whoever opens the file, which is the same audience.
        description = ""
        for line in text.splitlines():
            if line.startswith("#"):
                description = line.lstrip("# ").strip()
                break
        entry = {
            "name": name,
            "kind": "recipe",
            "version": "1.0.0",
            "description": description,
            "author": "MelloB1989",
            "source": f"recipes/{name}.yaml",
            "artifact": f"recipes/{name}.yaml",
            "sha256": sha256(path),
        }
        if name in SHIPPED:
            entry["ship_with_karmax"] = True
        yield entry


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--artifacts", default="dist",
                    help="where the built .kloop files are")
    ap.add_argument("--registry-key", default="",
                    help="the countersigning key this registry publishes")
    ap.add_argument("--strict", action="store_true",
                    help="fail if a workflow has no built artifact to pin")
    args = ap.parse_args()

    key = args.registry_key
    if not key and os.path.exists("index.json"):
        key = json.load(open("index.json")).get("registry_key", "")

    index = {
        "version": 1,
        "registry_key": key,
        "entries": list(workflows(args.artifacts, args.strict)) + list(recipes()),
    }
    with open("index.json", "w") as f:
        f.write(json.dumps(index, indent=2) + "\n")
    print(f"index.json: {len(index['entries'])} entries")


if __name__ == "__main__":
    main()
