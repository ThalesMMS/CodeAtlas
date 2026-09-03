#!/usr/bin/env python3
"""Validate a canonical codeatlas-codemap-presentation/v1 JSON file."""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

from codemap_lib import load_json, validate_codemap


def schema_errors(data: dict, schema_path: Path, require_schema: bool = False) -> list[str]:
    try:
        import jsonschema
    except ImportError:
        message = "jsonschema is not installed; schema validation was skipped"
        if require_schema:
            return [message]
        print(f"WARNING: {message}")
        return []
    try:
        schema = load_json(schema_path)
        validator = jsonschema.Draft202012Validator(schema, format_checker=jsonschema.FormatChecker())
    except Exception as exc:  # pragma: no cover - defensive packaging guard
        return [f"cannot initialize JSON Schema validation: {exc}"]
    errors: list[str] = []
    for error in sorted(validator.iter_errors(data), key=lambda item: list(item.absolute_path)):
        path = ".".join(str(part) for part in error.absolute_path) or "<root>"
        errors.append(f"schema {path}: {error.message}")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("codemap", nargs="?", default="codemap.json")
    parser.add_argument("--schema", default=None)
    parser.add_argument("--strict", action="store_true")
    parser.add_argument("--require-schema", action="store_true")
    args = parser.parse_args()

    codemap_path = Path(args.codemap).resolve()
    schema_path = Path(args.schema).resolve() if args.schema else codemap_path.with_name("codemap.schema.json")
    try:
        data = load_json(codemap_path)
    except Exception as exc:
        print(f"ERROR: cannot read {codemap_path}: {exc}", file=sys.stderr)
        return 2

    if schema_path.is_file():
        errors = schema_errors(data, schema_path, require_schema=args.require_schema)
    elif args.require_schema or args.schema:
        errors = [f"schema file not found: {schema_path}"]
    else:
        print(f"WARNING: schema file not found; schema validation was skipped: {schema_path}")
        errors = []
    semantic_errors, warnings = validate_codemap(data, strict=args.strict)
    errors.extend(semantic_errors)

    for warning in warnings:
        print(f"WARNING: {warning}")
    for error in errors:
        print(f"ERROR: {error}", file=sys.stderr)
    if errors:
        print(f"FAILED: {len(errors)} error(s), {len(warnings)} warning(s)", file=sys.stderr)
        return 1

    print(
        "OK: codemap is valid "
        f"({len(data.get('sections', []))} sections, {len(data.get('nodes', []))} nodes, "
        f"{len(data.get('edges', []))} edges; {len(warnings)} warning(s))"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
