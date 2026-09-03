#!/usr/bin/env python3
"""Write a package manifest and create a deterministic codemap ZIP archive."""

from __future__ import annotations

import argparse
import hashlib
import os
import re
import stat
import sys
import zipfile
from pathlib import Path

EXCLUDED_PARTS = {".git", ".build", "__pycache__", ".pytest_cache", ".mypy_cache"}
EXCLUDED_SUFFIXES = {".pyc", ".pyo", ".zip"}
FIXED_TIME = (1980, 1, 1, 0, 0, 0)
MANIFEST_LINE_RE = re.compile(r"^([0-9a-f]{64})  (.+)$")


def included_files(root: Path) -> list[Path]:
    files: list[Path] = []
    for path in root.rglob("*"):
        if path.is_symlink():
            continue
        if not path.is_file():
            continue
        relative = path.relative_to(root)
        if relative.name == "MANIFEST.sha256":
            continue
        if any(part in EXCLUDED_PARTS for part in relative.parts):
            continue
        if path.suffix in EXCLUDED_SUFFIXES or relative.name in {".DS_Store"}:
            continue
        files.append(path)
    return sorted(files, key=lambda item: item.relative_to(root).as_posix())


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def write_manifest(root: Path, files: list[Path]) -> Path:
    manifest = root / "MANIFEST.sha256"
    lines = [f"{sha256(path)}  {path.relative_to(root).as_posix()}" for path in files]
    manifest.write_text("\n".join(lines) + "\n", encoding="utf-8", newline="\n")
    return manifest


def manifest_errors(root: Path) -> list[str]:
    root = root.resolve()
    manifest = root / "MANIFEST.sha256"
    if not manifest.is_file():
        return [f"manifest file not found: {manifest}"]
    errors: list[str] = []
    listed_files: set[str] = set()
    for line_number, line in enumerate(manifest.read_text(encoding="utf-8").splitlines(), start=1):
        match = MANIFEST_LINE_RE.fullmatch(line)
        if not match:
            errors.append(f"invalid manifest entry at line {line_number}")
            continue
        expected, relative = match.groups()
        candidate = (root / relative).resolve()
        if not candidate.is_relative_to(root):
            errors.append(f"manifest path escapes root at line {line_number}: {relative}")
            continue
        normalized = candidate.relative_to(root).as_posix()
        listed_files.add(normalized)
        if not candidate.is_file():
            errors.append(f"missing file: {relative}")
        else:
            actual = sha256(candidate)
            if actual != expected:
                errors.append(f"hash mismatch: {relative} (expected {expected}, found {actual})")
    packaged_files = {path.relative_to(root).as_posix() for path in included_files(root)}
    for relative in sorted(packaged_files - listed_files):
        errors.append(f"unlisted file: {relative}")
    return errors


def add_file(archive: zipfile.ZipFile, path: Path, arcname: str) -> None:
    data = path.read_bytes()
    info = zipfile.ZipInfo(arcname, date_time=FIXED_TIME)
    info.compress_type = zipfile.ZIP_DEFLATED
    info.create_system = 3
    executable = bool(path.stat().st_mode & stat.S_IXUSR)
    permissions = 0o755 if executable else 0o644
    info.external_attr = permissions << 16
    archive.writestr(info, data, compresslevel=9)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default=".")
    parser.add_argument("--output", default=None)
    parser.add_argument("--folder-name", default=None)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()

    root = Path(args.root).resolve()
    if not root.is_dir():
        print(f"ERROR: root directory not found: {root}", file=sys.stderr)
        return 2
    if args.check:
        errors = manifest_errors(root)
        for error in errors:
            print(f"ERROR: {error}", file=sys.stderr)
        if errors:
            return 1
        print(f"OK: verified {sum(1 for _ in (root / 'MANIFEST.sha256').read_text(encoding='utf-8').splitlines())} manifest entries")
        return 0
    if not args.output:
        parser.error("--output is required unless --check is used")
    output = Path(args.output).resolve()
    if output.is_relative_to(root):
        print("ERROR: output ZIP must be outside the packaged root", file=sys.stderr)
        return 2

    files = included_files(root)
    manifest = write_manifest(root, files)
    files = sorted([*files, manifest], key=lambda item: item.relative_to(root).as_posix())
    folder = args.folder_name or root.name
    output.parent.mkdir(parents=True, exist_ok=True)
    temporary = output.with_suffix(output.suffix + ".tmp")
    temporary.unlink(missing_ok=True)
    try:
        with zipfile.ZipFile(temporary, "w") as archive:
            for path in files:
                relative = path.relative_to(root).as_posix()
                add_file(archive, path, f"{folder}/{relative}")
        os.replace(temporary, output)
    except BaseException:
        temporary.unlink(missing_ok=True)
        raise
    print(f"Packaged {len(files)} files into {output}")
    print(f"SHA-256 {sha256(output)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
