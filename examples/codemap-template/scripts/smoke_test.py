#!/usr/bin/env python3
"""Smoke-test the self-contained codemap with desktop and mobile browser interactions."""

from __future__ import annotations

import argparse
import json
import math
import re
import sys
from pathlib import Path

from browser_lib import browser_launch

DATA_RE = re.compile(r'<script id="codemap-data" type="application/json">(.*?)</script>', re.DOTALL)
SOURCE_RE = re.compile(r'<script id="codemap-sources" type="application/json">(.*?)</script>', re.DOTALL)
TOKENS = ("__CODEMAP_CSP__", "__CODEMAP_CSS__", "__CODEMAP_DATA__", "__CODEMAP_SOURCES__", "__CODEMAP_JS__")


def embedded_json(pattern: re.Pattern[str], html: str, label: str) -> tuple[dict, list[str]]:
    match = pattern.search(html)
    if not match:
        return {}, [f"embedded {label} payload was not found"]
    try:
        return json.loads(match.group(1).replace("<\\/", "</")), []
    except json.JSONDecodeError as exc:
        return {}, [f"embedded {label} payload is invalid: {exc}"]


def viewer_only(html: str) -> str:
    return SOURCE_RE.sub("", DATA_RE.sub("", html))


def static_checks(html: str) -> tuple[dict, dict, list[str]]:
    errors: list[str] = []
    for token in TOKENS:
        if token in html:
            errors.append(f"unresolved build token: {token}")
    for required in (
        'id="codemap-sections"',
        'id="map-stage"',
        'id="source-panel"',
        'Content-Security-Policy',
    ):
        if required not in html:
            errors.append(f"required viewer element missing: {required}")
    viewer = viewer_only(html)
    if "fetch(" in viewer or re.search(r"(?:src|href)=[\"\']https?://", viewer, re.IGNORECASE):
        errors.append("built viewer contains a network dependency")
    data, data_errors = embedded_json(DATA_RE, html, "codemap")
    sources, source_errors = embedded_json(SOURCE_RE, html, "source snapshot")
    errors.extend(data_errors)
    errors.extend(source_errors)
    return data, sources, errors


def interaction_targets(data: dict) -> tuple[dict[str, dict[str, str]], list[str]]:
    nodes = [
        {"id": node["id"], "title": str(node.get("title") or node["id"])}
        for node in data.get("nodes", [])
        if isinstance(node, dict) and isinstance(node.get("id"), str) and node["id"]
    ] if isinstance(data.get("nodes"), list) else []
    if not nodes:
        return {}, ["codemap has no usable nodes for browser interactions"]
    return {"guide": nodes[0], "map": nodes[-1], "mobile": nodes[0]}, []


def geometry_errors(boxes: list[dict]) -> list[str]:
    errors: list[str] = []
    for box in boxes:
        if any(not isinstance(box.get(key), (int, float)) or not math.isfinite(box[key]) for key in ("left", "top")):
            errors.append(f"map node {box.get('id')} is missing a finite inline position")
    if errors:
        return errors
    for index, first in enumerate(boxes):
        for second in boxes[index + 1 :]:
            intersects = not (
                first["left"] + first["width"] <= second["left"]
                or second["left"] + second["width"] <= first["left"]
                or first["top"] + first["height"] <= second["top"]
                or second["top"] + second["height"] <= first["top"]
            )
            if intersects:
                errors.append(f"map nodes overlap: {first['id']} and {second['id']}")
    return errors


def overlap_errors(page) -> list[str]:
    boxes = page.locator(".map-node-button").evaluate_all(
        """elements => elements.map(el => ({
          id: el.dataset.nodeId,
          left: Number.isFinite(parseFloat(el.style.left)) ? parseFloat(el.style.left) : null,
          top: Number.isFinite(parseFloat(el.style.top)) ? parseFloat(el.style.top) : null,
          width: el.offsetWidth,
          height: el.offsetHeight
        }))"""
    )
    return geometry_errors(boxes)


def desktop_checks(browser, html: str, data: dict) -> list[str]:
    errors: list[str] = []
    targets, target_errors = interaction_targets(data)
    if target_errors:
        return target_errors
    page = browser.new_page(viewport={"width": 1440, "height": 1000}, device_scale_factor=1)
    page.on("console", lambda message: errors.append(f"console.{message.type}: {message.text}") if message.type == "error" else None)
    page.on("pageerror", lambda error: errors.append(f"pageerror: {error}"))
    page.set_content(html, wait_until="load", timeout=30_000)
    page.wait_for_selector("details.codemap-section", timeout=10_000)

    if page.locator("details.codemap-section").count() != len(data.get("sections", [])):
        errors.append("rendered section count does not match codemap data")
    if page.locator(".node-card").count() != len(data.get("nodes", [])):
        errors.append("rendered guide-node count does not match codemap data")
    if page.locator(".map-node-button").count() != len(data.get("nodes", [])):
        errors.append("rendered map-node count does not match codemap data")
    if page.locator(".edge-path").count() != len(data.get("edges", [])):
        errors.append("rendered edge count does not match codemap data")
    if page.locator("details.codemap-section > summary").count() != len(data.get("sections", [])):
        errors.append("sections are not implemented with native details/summary")

    guide_target = targets["guide"]
    guide_card = page.locator(f'.node-card[data-node-id="{guide_target["id"]}"]').first
    if guide_card.count() == 0:
        page.close()
        return [f"guide interaction node was not rendered: {guide_target['id']}"]
    owning_section = guide_card.locator("xpath=ancestor::details[contains(concat(' ', normalize-space(@class), ' '), ' codemap-section ')][1]")
    owning_section.evaluate("element => { element.open = true; }")
    guide_details = owning_section.locator("details.guide-details")
    if guide_details.count():
        guide_details.evaluate("element => { element.open = true; element.dispatchEvent(new Event('toggle')); }")
    guide_card.click()
    if not owning_section.evaluate("element => element.open"):
        errors.append("selecting a node collapsed its owning section")
    if guide_details.count() and not guide_details.evaluate("element => element.open"):
        errors.append("selecting a node collapsed the expanded guide")
    if page.evaluate("location.hash") != f"#{guide_target['id']}":
        errors.append("node selection did not update the stable hash")
    if page.locator("#source-panel-title").inner_text() != guide_target["title"]:
        errors.append("guide-node selection did not render the expected source title")
    if page.locator(".code-row.is-target").count() != 1:
        errors.append("source panel did not highlight exactly one target line")

    page.locator("#view-map").click()
    if not page.locator("#map-view").is_visible():
        errors.append("Map button did not activate map view")
    errors.extend(overlap_errors(page))
    before = page.locator("#map-world").evaluate("element => element.style.transform")
    page.locator("#map-zoom-in").click()
    after = page.locator("#map-world").evaluate("element => element.style.transform")
    if before == after:
        errors.append("map zoom control did not change the world transform")
    map_target = targets["map"]
    map_node = page.locator(f'.map-node-button[data-node-id="{map_target["id"]}"]').first
    if map_node.count() == 0:
        errors.append(f"map interaction node was not rendered: {map_target['id']}")
    else:
        map_node.click()
    if map_node.count() and page.locator("#source-panel-title").inner_text() != map_target["title"]:
        errors.append("map-node selection did not render the expected source title")

    theme_before = page.locator("html").get_attribute("data-theme")
    page.locator("#theme-toggle").click()
    theme_after = page.locator("html").get_attribute("data-theme")
    if theme_before == theme_after:
        errors.append("theme toggle did not change the selected theme")

    dimensions = page.evaluate("({scrollWidth: document.documentElement.scrollWidth, innerWidth: innerWidth})")
    if dimensions["scrollWidth"] > dimensions["innerWidth"] + 1:
        errors.append(f"desktop document overflows horizontally: {dimensions}")
    page.close()
    return errors


def mobile_checks(browser, html: str, data: dict) -> list[str]:
    errors: list[str] = []
    targets, target_errors = interaction_targets(data)
    if target_errors:
        return target_errors
    target = targets["mobile"]
    page = browser.new_page(viewport={"width": 390, "height": 844}, device_scale_factor=1)
    page.on("console", lambda message: errors.append(f"mobile console.{message.type}: {message.text}") if message.type == "error" else None)
    page.on("pageerror", lambda error: errors.append(f"mobile pageerror: {error}"))
    page.set_content(html, wait_until="load", timeout=30_000)
    selector = f'.node-card[data-node-id="{target["id"]}"]'
    page.wait_for_selector(selector, timeout=10_000)

    dimensions = page.evaluate("({scrollWidth: document.documentElement.scrollWidth, innerWidth: innerWidth})")
    if dimensions["scrollWidth"] > dimensions["innerWidth"] + 1:
        errors.append(f"mobile document overflows horizontally: {dimensions}")

    card = page.locator(selector)
    card.focus()
    card.press("Enter")
    if not page.locator("#source-panel").evaluate("element => element.classList.contains('is-open')"):
        errors.append("keyboard selection did not open the mobile source panel")
    active_id = page.evaluate("document.activeElement && document.activeElement.id")
    if active_id != "source-close":
        errors.append(f"mobile source panel did not receive focus; active element is {active_id!r}")
    page.keyboard.press("Escape")
    if page.locator("#source-panel").evaluate("element => element.classList.contains('is-open')"):
        errors.append("Escape did not close the mobile source panel")
    focused_node = page.evaluate("document.activeElement && document.activeElement.dataset && document.activeElement.dataset.nodeId")
    if focused_node != target["id"]:
        errors.append(f"focus did not return to the selected card after Escape; found {focused_node!r}")
    page.close()
    return errors


def browser_checks(html: str, data: dict, require_browser: bool) -> tuple[bool, list[str]]:
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        if require_browser:
            return False, ["Python Playwright is required but not installed"]
        return False, []

    with sync_playwright() as playwright:
        browser = browser_launch(playwright)
        try:
            try:
                errors = desktop_checks(browser, html, data)
            except Exception as exc:
                errors = [f"desktop browser interaction failed: {exc}"]
            try:
                errors.extend(mobile_checks(browser, html, data))
            except Exception as exc:
                errors.append(f"mobile browser interaction failed: {exc}")
        finally:
            browser.close()
    return True, errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", default="dist/index.html")
    parser.add_argument("--require-browser", action="store_true")
    args = parser.parse_args()

    path = Path(args.input).resolve()
    if not path.is_file():
        print(f"ERROR: built HTML not found: {path}", file=sys.stderr)
        return 2
    html = path.read_text(encoding="utf-8")
    data, _, errors = static_checks(html)
    ran_browser = False
    if not errors:
        ran_browser, browser_errors = browser_checks(html, data, args.require_browser)
        errors.extend(browser_errors)

    for error in errors:
        print(f"ERROR: {error}", file=sys.stderr)
    if errors:
        return 1
    suffix = " with desktop and mobile browser interactions" if ran_browser else " (static checks only)"
    print(f"OK: codemap viewer smoke test passed{suffix}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
