#!/usr/bin/env python3
"""Convert a legacy codeatlas-codemap/v3 payload into the hybrid presentation contract.

The converter preserves section prose and source ranges but intentionally marks imported
relationships as inferred: legacy edges do not contain direct relationship evidence.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from collections import defaultdict
from pathlib import Path
from typing import Any

from codemap_lib import NODE_ID_RE, SCHEMA_VERSION, load_json, write_json

SLUG_RE = re.compile(r"[^a-z0-9]+")


def slug(value: str) -> str:
    cleaned = SLUG_RE.sub("-", value.lower()).strip("-")
    return cleaned or "group"


def letter_suffix(index: int) -> str:
    letters = ""
    index += 1
    while index:
        index, remainder = divmod(index - 1, 26)
        letters = chr(ord("a") + remainder) + letters
    return letters


def mapping(value: Any) -> dict[str, Any]:
    return value if isinstance(value, dict) else {}


def sequence(value: Any) -> list[Any]:
    return value if isinstance(value, list) else []


def positive_line(value: Any, fallback: int) -> int:
    if isinstance(value, bool):
        return fallback
    try:
        line = int(value)
    except (TypeError, ValueError):
        return fallback
    return line if line > 0 else fallback


def first_source_line(step: dict[str, Any]) -> tuple[int, str]:
    source = mapping(step.get("source"))
    start = positive_line(source.get("startLine"), 1)
    snippet = str(step.get("snippet", ""))
    for offset, line in enumerate(snippet.splitlines()):
        if line.strip():
            return start + offset, line.strip()
    return start, str(step.get("title", "source anchor"))


def edge_kind(value: str) -> str:
    normalized = value.lower()
    if normalized in {"calls", "returns", "dispatches", "emits", "awaits", "invokes"}:
        return "temporal"
    if normalized in {"reads", "writes", "passes", "transforms", "loads", "serializes"}:
        return "data"
    return "structural"


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("input")
    parser.add_argument("output")
    parser.add_argument("--language", choices=("en", "pt-BR"), default="en")
    args = parser.parse_args()

    try:
        legacy = load_json(Path(args.input).resolve())
    except Exception as exc:
        print(f"ERROR: cannot read legacy codemap: {exc}", file=sys.stderr)
        return 2
    if legacy.get("schemaVersion") != "codeatlas-codemap/v3":
        print("ERROR: input is not codeatlas-codemap/v3", file=sys.stderr)
        return 2

    legacy_nodes = {node.get("id"): node for node in sequence(legacy.get("nodes")) if isinstance(node, dict)}
    step_by_legacy_node: dict[str, str] = {}
    sections: list[dict[str, Any]] = []
    groups: list[dict[str, Any]] = []
    nodes: list[dict[str, Any]] = []
    group_ids: set[str] = set()
    node_ids: set[str] = set()

    tones = ("sand", "rose", "blue", "mint", "lavender", "gray")
    for section_index, raw_section in enumerate(sequence(legacy.get("sections")), start=1):
        section = mapping(raw_section)
        try:
            number = int(section.get("number", section_index))
            if number < 1:
                raise ValueError
        except (TypeError, ValueError):
            print(f"ERROR: legacy section {section_index} has an invalid number", file=sys.stderr)
            return 2
        section_id = f"section-{number}"
        flow = mapping(section.get("flow"))
        trace_groups: dict[str, list[dict[str, str]]] = defaultdict(list)
        for step_index, raw_step in enumerate(sequence(flow.get("steps"))):
            step = mapping(raw_step)
            generated_index = step_index
            generated_id = f"{number}{letter_suffix(generated_index)}"
            while generated_id in node_ids:
                generated_index += 1
                generated_id = f"{number}{letter_suffix(generated_index)}"
            label = step.get("label")
            label_match = NODE_ID_RE.fullmatch(label.strip().lower()) if isinstance(label, str) else None
            node_id = label.strip().lower() if label_match and int(label_match.group(1)) == number and label.strip().lower() not in node_ids else generated_id
            node_ids.add(node_id)
            legacy_node_id = str(step.get("nodeId", ""))
            legacy_node = legacy_nodes.get(legacy_node_id, {})
            group_label = str(legacy_node.get("group") or flow.get("title") or section.get("title") or "Flow")
            group_id = f"group-{number}-{slug(group_label)}"
            if group_id not in group_ids:
                groups.append(
                    {
                        "id": group_id,
                        "label": group_label,
                        "sectionId": section_id,
                        "tone": tones[(len(groups)) % len(tones)],
                    }
                )
                group_ids.add(group_id)
            target_line, target_snippet = first_source_line(step)
            source = mapping(step.get("source"))
            start_line = positive_line(source.get("startLine"), target_line)
            end_line = max(start_line, positive_line(source.get("endLine"), start_line))
            nodes.append(
                {
                    "id": node_id,
                    "sectionId": section_id,
                    "groupId": group_id,
                    "title": str(step.get("title") or legacy_node.get("label") or node_id),
                    "kind": str(legacy_node.get("kind") or "source"),
                    "status": "verified",
                    "source": {
                        "path": str(source.get("path", "REPLACE/path")),
                        "startLine": start_line,
                        "endLine": end_line,
                        "targetLine": target_line,
                        "symbol": str(legacy_node.get("label") or step.get("title") or node_id),
                        "snippet": target_snippet,
                    },
                }
            )
            trace_groups[group_id].append({"type": "node", "nodeId": node_id})
            if legacy_node_id and legacy_node_id not in step_by_legacy_node:
                step_by_legacy_node[legacy_node_id] = node_id

        guide = mapping(section.get("guide"))
        raw_details = guide.get("details")
        detail_text = raw_details.strip() if isinstance(raw_details, str) else ""
        sections.append(
            {
                "id": section_id,
                "number": number,
                "title": str(section.get("title") or f"Section {number}"),
                "summary": str(section.get("summary") or "Imported section requiring review."),
                "defaultOpen": number <= 2,
                "guide": {
                    "motivation": str(guide.get("motivation") or "Imported motivation requiring review."),
                    "details": [{"type": "paragraph", "text": detail_text or "Imported details requiring review."}],
                },
                "trace": {
                    "title": str(flow.get("title") or section.get("title") or f"Section {number}"),
                    "items": [
                        {
                            "type": "group",
                            "label": next(group["label"] for group in groups if group["id"] == group_id),
                            "children": children,
                        }
                        for group_id, children in trace_groups.items()
                    ],
                },
            }
        )

    edges: list[dict[str, Any]] = []
    for index, raw_edge in enumerate(sequence(legacy.get("edges")), start=1):
        edge = mapping(raw_edge)
        source = step_by_legacy_node.get(str(edge.get("source", "")))
        target = step_by_legacy_node.get(str(edge.get("target", "")))
        if not source or not target:
            continue
        edges.append(
            {
                "id": f"edge-imported-{index}",
                "from": source,
                "to": target,
                "label": str(edge.get("label") or edge.get("type") or "relates"),
                "kind": edge_kind(str(edge.get("type", "structural"))),
                "status": "inferred",
                "reason": "Imported from codeatlas-codemap/v3, which does not carry direct relationship evidence. Verify the linkage against the repository before finalization.",
                "evidence": [],
            }
        )

    repository = mapping(legacy.get("repository"))
    data = {
        "schemaVersion": SCHEMA_VERSION,
        "meta": {
            "id": str(legacy.get("id") or "codemap:imported"),
            "template": True,
            "title": str(legacy.get("title") or "Imported CodeAtlas Codemap"),
            "summary": str(legacy.get("overview") or "Imported codemap requiring evidence review."),
            "query": "Review and finalize the imported CodeAtlas codemap against the target repository.",
            "createdAt": str(legacy.get("generatedAt") or "1970-01-01T00:00:00Z"),
            "language": args.language,
            "defaultView": "guide",
            "uncertainty": "All imported edges are inferred until direct relationship evidence is added.",
            "repository": {
                "name": str(repository.get("name") or "unknown/repository"),
                "revision": str(repository.get("revision") or "PLACEHOLDER-REVISION"),
                "workingTree": "unknown",
            },
        },
        "presentation": {
            "theme": "system",
            "sourcePanel": "split",
            "map": {
                "direction": "LR",
                "groupColumns": min(3, max(1, len(groups))),
                "groupOrder": [group["id"] for group in groups],
            },
        },
        "sections": sections,
        "groups": groups,
        "nodes": nodes,
        "edges": edges,
    }
    write_json(Path(args.output).resolve(), data)
    print(
        f"Converted {len(sections)} sections, {len(nodes)} nodes and {len(edges)} inferred edges. "
        "Run validation and replace imported relationship assumptions with repository evidence."
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
