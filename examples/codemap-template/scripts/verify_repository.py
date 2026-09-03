#!/usr/bin/env python3
"""Verify all codemap anchors against a repository and write a derived source snapshot."""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

from codemap_lib import load_json, resolve_sources, validate_codemap, write_json


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("codemap", nargs="?", default="codemap.json")
    parser.add_argument("--repository", required=True)
    parser.add_argument("--write-snapshot", default=None)
    parser.add_argument("--allow-dirty", action="store_true")
    parser.add_argument("--allow-non-git", action="store_true")
    parser.add_argument("--strict", action="store_true", help="also apply final-artifact structural rules")
    args = parser.parse_args()

    codemap_path = Path(args.codemap).resolve()
    repository = Path(args.repository).resolve()
    try:
        data = load_json(codemap_path)
    except Exception as exc:
        print(f"ERROR: cannot read {codemap_path}: {exc}", file=sys.stderr)
        return 2

    structural_errors, structural_warnings = validate_codemap(data, strict=args.strict)
    for warning in structural_warnings:
        print(f"WARNING: {warning}")
    if structural_errors:
        for error in structural_errors:
            print(f"ERROR: {error}", file=sys.stderr)
        return 1

    snapshot, errors, warnings = resolve_sources(
        data,
        repository,
        allow_dirty=args.allow_dirty,
        allow_non_git=args.allow_non_git,
    )
    for warning in warnings:
        print(f"WARNING: {warning}")
    for error in errors:
        print(f"ERROR: {error}", file=sys.stderr)
    if errors:
        print(f"FAILED: repository verification found {len(errors)} error(s)", file=sys.stderr)
        return 1

    output = Path(args.write_snapshot).resolve() if args.write_snapshot else codemap_path.parent / ".build" / "source-snapshot.json"
    write_json(output, snapshot)
    print(f"OK: verified {len(snapshot.get('nodes', {}))} node anchor(s) and wrote {output}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
