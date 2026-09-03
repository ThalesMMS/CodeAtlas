#!/usr/bin/env python3
"""Build a deterministic, self-contained codemap HTML file with a hashed CSP."""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import re
import sys
from pathlib import Path

from codemap_lib import load_json, resolve_sources, validate_codemap, write_json

TOKENS = (
    "__CODEMAP_CSP__",
    "__CODEMAP_CSS__",
    "__CODEMAP_DATA__",
    "__CODEMAP_SOURCES__",
    "__CODEMAP_JS__",
)
TOKEN_RE = re.compile("|".join(re.escape(token) for token in TOKENS))


def csp_hash(content: str) -> str:
    digest = hashlib.sha256(content.encode("utf-8")).digest()
    return "sha256-" + base64.b64encode(digest).decode("ascii")


def script_safe_json(value: object) -> str:
    return (
        json.dumps(value, ensure_ascii=False, separators=(",", ":"))
        .replace("</", "<\\/")
        .replace("\u2028", "\\u2028")
        .replace("\u2029", "\\u2029")
    )


def replace_tokens(template: str, replacements: dict[str, str]) -> str:
    missing = [token for token in TOKENS if token not in template]
    if missing:
        raise ValueError(f"template is missing build tokens: {', '.join(missing)}")
    return TOKEN_RE.sub(lambda match: replacements[match.group(0)], template)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default=None)
    parser.add_argument("--data", default="codemap.json")
    parser.add_argument("--repository", default=None)
    parser.add_argument("--snapshot", default=None)
    parser.add_argument("--output", default="dist/index.html")
    parser.add_argument("--strict", action="store_true")
    parser.add_argument("--allow-dirty", action="store_true")
    parser.add_argument("--allow-non-git", action="store_true")
    args = parser.parse_args()

    root = Path(args.root).resolve() if args.root else Path(__file__).resolve().parents[1]
    data_path = (root / args.data).resolve() if not Path(args.data).is_absolute() else Path(args.data).resolve()
    output_path = (root / args.output).resolve() if not Path(args.output).is_absolute() else Path(args.output).resolve()
    snapshot_path = None
    if args.snapshot:
        snapshot_path = (root / args.snapshot).resolve() if not Path(args.snapshot).is_absolute() else Path(args.snapshot).resolve()

    try:
        data = load_json(data_path)
    except Exception as exc:
        print(f"ERROR: cannot read {data_path}: {exc}", file=sys.stderr)
        return 2

    errors, warnings = validate_codemap(data, strict=args.strict)
    for warning in warnings:
        print(f"WARNING: {warning}")
    if errors:
        for error in errors:
            print(f"ERROR: {error}", file=sys.stderr)
        return 1

    snapshot: dict
    if snapshot_path:
        if not snapshot_path.is_file():
            print(f"ERROR: source snapshot not found: {snapshot_path}", file=sys.stderr)
            return 2
        try:
            snapshot = load_json(snapshot_path)
        except Exception as exc:
            print(f"ERROR: cannot read source snapshot {snapshot_path}: {exc}", file=sys.stderr)
            return 2
    else:
        repository = Path(args.repository).resolve() if args.repository else None
        if repository is None and data.get("meta", {}).get("template") is True:
            fixture = root / "fixtures" / "example-repository"
            if fixture.is_dir():
                repository = fixture
        if repository is None:
            cached = root / ".build" / "source-snapshot.json"
            if cached.is_file():
                try:
                    snapshot = load_json(cached)
                except Exception as exc:
                    print(f"ERROR: cannot read source snapshot {cached}: {exc}", file=sys.stderr)
                    return 2
            else:
                print("ERROR: provide --repository or --snapshot; final builds must derive source context from a checkout", file=sys.stderr)
                return 2
        else:
            snapshot, source_errors, source_warnings = resolve_sources(
                data,
                repository,
                allow_dirty=args.allow_dirty,
                allow_non_git=args.allow_non_git or data.get("meta", {}).get("template") is True,
            )
            for warning in source_warnings:
                print(f"WARNING: {warning}")
            if source_errors:
                for error in source_errors:
                    print(f"ERROR: {error}", file=sys.stderr)
                return 1
            write_json(root / ".build" / "source-snapshot.json", snapshot)

    missing_snapshots = sorted({node["id"] for node in data.get("nodes", [])} - set(snapshot.get("nodes", {})))
    if missing_snapshots:
        print(f"ERROR: source snapshot is missing nodes: {', '.join(missing_snapshots)}", file=sys.stderr)
        return 1

    template = (root / "assets" / "viewer.html").read_text(encoding="utf-8")
    css = (root / "assets" / "viewer.css").read_text(encoding="utf-8").rstrip() + "\n"
    javascript = (root / "assets" / "viewer.js").read_text(encoding="utf-8").rstrip() + "\n"
    csp = "; ".join(
        [
            "default-src 'none'",
            "base-uri 'none'",
            "object-src 'none'",
            "form-action 'none'",
            "connect-src 'none'",
            "img-src data:",
            "font-src data:",
            f"style-src-elem '{csp_hash(css)}'",
            "style-src-attr 'unsafe-inline'",
            f"script-src-elem '{csp_hash(javascript)}'",
            "script-src-attr 'none'",
        ]
    )

    replacements = {
        "__CODEMAP_CSP__": csp,
        "__CODEMAP_CSS__": css,
        "__CODEMAP_DATA__": script_safe_json(data),
        "__CODEMAP_SOURCES__": script_safe_json(snapshot),
        "__CODEMAP_JS__": javascript,
    }
    try:
        html = replace_tokens(template, replacements)
    except ValueError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 1

    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(html, encoding="utf-8", newline="\n")
    digest = hashlib.sha256(output_path.read_bytes()).hexdigest()
    print(f"Built {output_path}")
    print(f"SHA-256 {digest}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
