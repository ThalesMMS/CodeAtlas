#!/usr/bin/env python3
"""Run the codemap structural, repository, build, interaction, test, and preview gates."""

from __future__ import annotations

import argparse
import shutil
import subprocess
import sys
from pathlib import Path


def run(command: list[str], cwd: Path) -> None:
    print("+", " ".join(command), flush=True)
    subprocess.run(command, cwd=cwd, check=True)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", default=None)
    parser.add_argument("--repository", default=None)
    parser.add_argument("--strict", action="store_true")
    parser.add_argument("--allow-dirty", action="store_true")
    parser.add_argument("--allow-non-git", action="store_true")
    parser.add_argument("--require-browser", action="store_true")
    parser.add_argument("--render-previews", action="store_true")
    args = parser.parse_args()

    root = Path(args.root).resolve() if args.root else Path(__file__).resolve().parents[1]
    python = sys.executable
    repository = Path(args.repository).resolve() if args.repository else root / "fixtures" / "example-repository"
    if not repository.is_dir():
        print("ERROR: provide --repository for a scaffold without the example fixture", file=sys.stderr)
        return 2

    common_verify: list[str] = []
    if args.allow_dirty:
        common_verify.append("--allow-dirty")
    if args.allow_non_git or repository == root / "fixtures" / "example-repository":
        common_verify.append("--allow-non-git")

    try:
        validate = [python, "scripts/validate.py", "codemap.json"]
        if args.strict:
            validate.append("--strict")
        run(validate, root)

        verify = [
            python,
            "scripts/verify_repository.py",
            "codemap.json",
            "--repository",
            str(repository),
            "--write-snapshot",
            ".build/source-snapshot.json",
            *common_verify,
        ]
        if args.strict:
            verify.append("--strict")
        run(verify, root)

        build = [
            python,
            "scripts/build.py",
            "--root",
            str(root),
            "--repository",
            str(repository),
            *common_verify,
        ]
        if args.strict:
            build.append("--strict")
        run(build, root)

        node = shutil.which("node")
        if node:
            run([node, "--check", "assets/viewer.js"], root)
        run([python, "-m", "unittest", "discover", "-s", "tests", "-p", "test_*.py"], root)

        smoke = [python, "scripts/smoke_test.py", "--input", "dist/index.html"]
        if args.require_browser:
            smoke.append("--require-browser")
        run(smoke, root)

        if args.render_previews:
            run(
                [python, "scripts/render_preview.py", "--input", "dist/index.html", "--output", "preview-guide.png", "--view", "guide", "--node", "1c"],
                root,
            )
            run(
                [python, "scripts/render_preview.py", "--input", "dist/index.html", "--output", "preview-map.png", "--view", "map", "--node", "3c"],
                root,
            )
            run(
                [python, "scripts/render_preview.py", "--input", "dist/index.html", "--output", "preview-mobile.png", "--view", "guide", "--node", "1a", "--mobile"],
                root,
            )
    except subprocess.CalledProcessError as exc:
        return exc.returncode or 1

    print("OK: all codemap quality gates passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
