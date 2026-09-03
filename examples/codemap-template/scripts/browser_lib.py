#!/usr/bin/env python3
"""Shared Chromium discovery and Playwright launch options."""

from __future__ import annotations

import shutil

CHROMIUM_NAMES = ("chromium", "chromium-browser", "google-chrome", "google-chrome-stable")
DEFAULT_BROWSER_ARGS = ["--no-sandbox", "--disable-dev-shm-usage"]


def browser_launch(playwright, args: list[str] | None = None):
    chromium = next((path for path in map(shutil.which, CHROMIUM_NAMES) if path), None)
    options: dict[str, object] = {
        "headless": True,
        "args": args if args is not None else DEFAULT_BROWSER_ARGS,
    }
    if chromium:
        options["executable_path"] = chromium
    return playwright.chromium.launch(**options)
