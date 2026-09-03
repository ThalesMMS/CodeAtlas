#!/usr/bin/env python3
"""Render deterministic guide, map, or mobile previews from a built codemap HTML file."""

from __future__ import annotations

import argparse
import math
import sys
from pathlib import Path

from browser_lib import browser_launch


def positive_int(value: str) -> int:
    parsed = int(value)
    if parsed <= 0:
        raise argparse.ArgumentTypeError("must be a positive integer")
    return parsed


def positive_float(value: str) -> float:
    parsed = float(value)
    if not math.isfinite(parsed) or parsed <= 0:
        raise argparse.ArgumentTypeError("must be a positive finite number")
    return parsed


def resolve_dimensions(width: int | None, height: int | None, mobile: bool) -> tuple[int, int]:
    resolved_width = width if width is not None else (390 if mobile else 1440)
    resolved_height = height if height is not None else (844 if mobile else 1000)
    return resolved_width, resolved_height


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", default="dist/index.html")
    parser.add_argument("--output", required=True)
    parser.add_argument("--view", choices=("guide", "map"), default="guide")
    parser.add_argument("--theme", choices=("light", "dark", "system"), default="light")
    parser.add_argument("--node", default=None, help="Stable node ID to select before capture")
    parser.add_argument("--width", type=positive_int, default=None)
    parser.add_argument("--height", type=positive_int, default=None)
    parser.add_argument("--device-scale-factor", type=positive_float, default=1.0)
    parser.add_argument("--full-page", action="store_true")
    parser.add_argument("--mobile", action="store_true", help="Use a 390x844 mobile viewport unless dimensions are explicit")
    args = parser.parse_args()

    input_path = Path(args.input).resolve()
    output_path = Path(args.output).resolve()
    if not input_path.is_file():
        print(f"ERROR: built HTML not found: {input_path}", file=sys.stderr)
        return 2
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("ERROR: Python Playwright is required to render previews", file=sys.stderr)
        return 2

    width, height = resolve_dimensions(args.width, args.height, args.mobile)
    html = input_path.read_text(encoding="utf-8")
    output_path.parent.mkdir(parents=True, exist_ok=True)
    errors: list[str] = []

    with sync_playwright() as playwright:
        browser = browser_launch(playwright, ["--no-sandbox", "--disable-dev-shm-usage", "--hide-scrollbars"])
        try:
            page = browser.new_page(
                viewport={"width": width, "height": height},
                device_scale_factor=args.device_scale_factor,
                color_scheme="dark" if args.theme == "dark" else "light",
                reduced_motion="reduce",
            )
            try:
                page.on("console", lambda message: errors.append(f"console.{message.type}: {message.text}") if message.type == "error" else None)
                page.on("pageerror", lambda error: errors.append(f"pageerror: {error}"))
                # set_content exercises the self-contained payload without network access.
                page.set_content(html, wait_until="load", timeout=30_000)
                page.wait_for_selector("details.codemap-section", timeout=10_000)
                page.evaluate(
                    """({ theme, view }) => {
                        document.documentElement.dataset.theme = theme;
                        const button = document.getElementById(view === 'map' ? 'view-map' : 'view-guide');
                        if (button) button.click();
                    }""",
                    {"theme": args.theme, "view": args.view},
                )
                if args.node:
                    selector = (
                        f'.map-node-button[data-node-id="{args.node}"]'
                        if args.view == "map"
                        else f'.node-card[data-node-id="{args.node}"]'
                    )
                    locator = page.locator(selector).first
                    if locator.count() == 0:
                        errors.append(f"requested preview node was not rendered: {args.node}")
                    else:
                        locator.click()
                        if args.mobile:
                            page.locator("#source-close").click()
                if args.view == "map":
                    page.locator("#map-reset").click()
                elif args.view == "guide":
                    page.evaluate("window.scrollTo(0, 0)")
                page.mouse.move(4, 4)
                page.wait_for_timeout(150)
                page.screenshot(path=str(output_path), full_page=args.full_page, animations="disabled")
            finally:
                page.close()
        finally:
            browser.close()

    if errors:
        for error in errors:
            print(f"ERROR: {error}", file=sys.stderr)
        return 1
    print(f"Rendered {output_path} ({width}x{height}, {args.view}, {args.theme})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
