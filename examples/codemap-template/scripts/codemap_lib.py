#!/usr/bin/env python3
"""Shared validation and repository-resolution helpers for codemap artifacts."""

from __future__ import annotations

import json
import re
import subprocess
from pathlib import Path
from typing import Any, Iterable

SCHEMA_VERSION = "codeatlas-codemap-presentation/v1"
SNAPSHOT_VERSION = "codeatlas-codemap-source-snapshot/v1"
NODE_ID_RE = re.compile(r"^([1-9][0-9]*)([a-z]+)$")
REFERENCE_RE = re.compile(r"\[([1-9][0-9]*[a-z]+(?:\s*,\s*[1-9][0-9]*[a-z]+)*)\]")
PLACEHOLDER_RE = re.compile(r"<(?:[A-Z][A-Z0-9_ -]{1,80})>")


def load_json(path: Path) -> dict[str, Any]:
    with path.open("r", encoding="utf-8") as handle:
        value = json.load(handle)
    if not isinstance(value, dict):
        raise ValueError(f"expected a JSON object in {path}")
    return value


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def all_strings(value: Any) -> Iterable[str]:
    if isinstance(value, str):
        yield value
    elif isinstance(value, dict):
        for child in value.values():
            yield from all_strings(child)
    elif isinstance(value, list):
        for child in value:
            yield from all_strings(child)


def narrative_strings(data: dict[str, Any]) -> Iterable[tuple[str, str]]:
    meta = data.get("meta")
    if not isinstance(meta, dict):
        meta = {}
    for key in ("summary", "uncertainty"):
        value = meta.get(key)
        if isinstance(value, str):
            yield f"meta.{key}", value
    sections = data.get("sections")
    if not isinstance(sections, list):
        return
    for index, section in enumerate(sections):
        if not isinstance(section, dict):
            continue
        prefix = f"sections[{index}]"
        for key in ("summary",):
            value = section.get(key)
            if isinstance(value, str):
                yield f"{prefix}.{key}", value
        guide = section.get("guide")
        if isinstance(guide, dict):
            if isinstance(guide.get("motivation"), str):
                yield f"{prefix}.guide.motivation", guide["motivation"]
            details = guide.get("details")
            if not isinstance(details, list):
                continue
            for detail_index, block in enumerate(details):
                if not isinstance(block, dict):
                    continue
                if block.get("type") == "paragraph" and isinstance(block.get("text"), str):
                    yield f"{prefix}.guide.details[{detail_index}].text", block["text"]
                items = block.get("items")
                if not isinstance(items, list):
                    continue
                for item_index, item in enumerate(items):
                    if isinstance(item, str):
                        yield f"{prefix}.guide.details[{detail_index}].items[{item_index}]", item


def trace_node_ids(items: list[dict[str, Any]]) -> Iterable[str]:
    for item in items:
        if not isinstance(item, dict):
            continue
        if item.get("type") == "node" and isinstance(item.get("nodeId"), str):
            yield item["nodeId"]
        children = item.get("children")
        if isinstance(children, list):
            yield from trace_node_ids(children)


def validate_anchor(anchor: Any, label: str, errors: list[str]) -> None:
    if not isinstance(anchor, dict):
        errors.append(f"{label} must be an object")
        return
    path = anchor.get("path")
    if not isinstance(path, str) or not path:
        errors.append(f"{label}.path must be a non-empty string")
    elif path.startswith("/") or "\\" in path or ".." in Path(path).parts:
        errors.append(f"{label}.path must be a safe repository-relative / path")
    for key in ("startLine", "endLine", "targetLine"):
        value = anchor.get(key)
        if not isinstance(value, int) or value < 1:
            errors.append(f"{label}.{key} must be a positive integer")
    start = anchor.get("startLine")
    end = anchor.get("endLine")
    target = anchor.get("targetLine")
    if all(isinstance(value, int) for value in (start, end, target)) and not (start <= target <= end):
        errors.append(f"{label} must satisfy startLine <= targetLine <= endLine")
    snippet = anchor.get("snippet")
    if not isinstance(snippet, str) or not snippet:
        errors.append(f"{label}.snippet must be a non-empty string")
    elif "\n" in snippet or "\r" in snippet:
        errors.append(f"{label}.snippet must contain exactly one source line")


def validate_codemap(data: dict[str, Any], strict: bool = False) -> tuple[list[str], list[str]]:
    errors: list[str] = []
    warnings: list[str] = []

    if data.get("schemaVersion") != SCHEMA_VERSION:
        errors.append(f"schemaVersion must be {SCHEMA_VERSION!r}")

    meta = data.get("meta")
    if not isinstance(meta, dict):
        errors.append("meta must be an object")
        meta = {}
    if strict and meta.get("template") is not False:
        errors.append("strict mode requires meta.template=false")
    if not strict and meta.get("template") is True:
        warnings.append("meta.template=true: this is an editable placeholder, not a final codemap")
    if strict:
        placeholders = sorted({match.group(0) for text in all_strings(data) for match in PLACEHOLDER_RE.finditer(text)})
        if placeholders:
            errors.append(f"unresolved placeholders remain: {', '.join(placeholders)}")
        repository_meta = meta.get("repository", {}) if isinstance(meta.get("repository"), dict) else {}
        revision = repository_meta.get("revision")
        if not isinstance(revision, str) or revision.startswith("fixture-") or "PLACEHOLDER" in revision.upper():
            errors.append("strict mode requires a real repository revision")
        if repository_meta.get("workingTree") == "unknown":
            errors.append("strict mode requires repository.workingTree to be clean or dirty")
        marker_fields = {
            "meta.id": meta.get("id"),
            "meta.title": meta.get("title"),
            "meta.summary": meta.get("summary"),
            "meta.repository.name": repository_meta.get("name"),
        }
        for location, value in marker_fields.items():
            if not isinstance(value, str):
                continue
            lowered = value.lower()
            if "placeholder" in lowered or lowered.startswith("example/"):
                errors.append(f"strict mode rejects template marker in {location}: {value!r}")

    sections = data.get("sections")
    groups = data.get("groups")
    nodes = data.get("nodes")
    edges = data.get("edges")
    if not isinstance(sections, list) or not sections:
        errors.append("sections must be a non-empty array")
        sections = []
    if not isinstance(groups, list) or not groups:
        errors.append("groups must be a non-empty array")
        groups = []
    if not isinstance(nodes, list) or not nodes:
        errors.append("nodes must be a non-empty array")
        nodes = []
    if not isinstance(edges, list):
        errors.append("edges must be an array")
        edges = []

    section_ids: set[str] = set()
    section_numbers: set[int] = set()
    section_number_by_id: dict[str, int] = {}
    traced: list[str] = []
    for index, section in enumerate(sections):
        label = f"sections[{index}]"
        if not isinstance(section, dict):
            errors.append(f"{label} must be an object")
            continue
        section_id = section.get("id")
        number = section.get("number")
        if not isinstance(section_id, str) or not section_id:
            errors.append(f"{label}.id must be a non-empty string")
        elif section_id in section_ids:
            errors.append(f"duplicate section id: {section_id}")
        else:
            section_ids.add(section_id)
        if not isinstance(number, int) or number < 1:
            errors.append(f"{label}.number must be a positive integer")
        elif number in section_numbers:
            errors.append(f"duplicate section number: {number}")
        else:
            section_numbers.add(number)
            if isinstance(section_id, str):
                section_number_by_id[section_id] = number
        trace = section.get("trace", {})
        if not isinstance(trace, dict) or not isinstance(trace.get("items"), list):
            errors.append(f"{label}.trace.items must be an array")
        else:
            traced.extend(trace_node_ids(trace["items"]))
    if section_numbers and section_numbers != set(range(1, len(section_numbers) + 1)):
        errors.append("section numbers must be contiguous and start at 1")

    group_ids: set[str] = set()
    group_section: dict[str, str] = {}
    for index, group in enumerate(groups):
        label = f"groups[{index}]"
        if not isinstance(group, dict):
            errors.append(f"{label} must be an object")
            continue
        group_id = group.get("id")
        if not isinstance(group_id, str) or not group_id:
            errors.append(f"{label}.id must be a non-empty string")
        elif group_id in group_ids:
            errors.append(f"duplicate group id: {group_id}")
        else:
            group_ids.add(group_id)
        section_id = group.get("sectionId")
        if not isinstance(section_id, str) or section_id not in section_ids:
            errors.append(f"{label}.sectionId references unknown section {section_id!r}")
        if isinstance(group_id, str) and isinstance(section_id, str):
            group_section[group_id] = section_id

    presentation = data.get("presentation", {})
    map_config = presentation.get("map", {}) if isinstance(presentation, dict) else {}
    order = map_config.get("groupOrder") if isinstance(map_config, dict) else None
    if not isinstance(order, list):
        errors.append("presentation.map.groupOrder must be an array")
    else:
        unknown = [repr(group_id) for group_id in order if not isinstance(group_id, str) or group_id not in group_ids]
        valid_order = {group_id for group_id in order if isinstance(group_id, str)}
        missing = sorted(group_ids - valid_order)
        if unknown:
            errors.append(f"presentation.map.groupOrder contains unknown groups: {', '.join(unknown)}")
        if missing:
            errors.append(f"presentation.map.groupOrder omits groups: {', '.join(missing)}")
    featured = map_config.get("featuredGroupId") if isinstance(map_config, dict) else None
    if featured is not None and (not isinstance(featured, str) or featured not in group_ids):
        errors.append(f"presentation.map.featuredGroupId references unknown group {featured!r}")

    node_ids: set[str] = set()
    for index, node in enumerate(nodes):
        label = f"nodes[{index}]"
        if not isinstance(node, dict):
            errors.append(f"{label} must be an object")
            continue
        node_id = node.get("id")
        match = NODE_ID_RE.fullmatch(node_id) if isinstance(node_id, str) else None
        if not match:
            errors.append(f"{label}.id must match <section-number><lowercase-letter>, for example 2b")
        elif node_id in node_ids:
            errors.append(f"duplicate node id: {node_id}")
        else:
            node_ids.add(node_id)
        section_id = node.get("sectionId")
        group_id = node.get("groupId")
        if not isinstance(section_id, str) or section_id not in section_ids:
            errors.append(f"{label}.sectionId references unknown section {section_id!r}")
        if not isinstance(group_id, str) or group_id not in group_ids:
            errors.append(f"{label}.groupId references unknown group {group_id!r}")
        elif group_section.get(group_id) != section_id:
            errors.append(f"{label} sectionId does not match the owning group's section")
        if match and section_number_by_id.get(section_id) != int(match.group(1)):
            errors.append(f"node {node_id} numeric prefix must match its section number")
        if "map" in node or any(key in node for key in ("x", "y", "width", "height")):
            errors.append(f"{label} must not contain manual map coordinates; layout is derived")
        validate_anchor(node.get("source"), f"{label}.source", errors)

    traced_counts: dict[str, int] = {}
    for node_id in traced:
        traced_counts[node_id] = traced_counts.get(node_id, 0) + 1
        if node_id not in node_ids:
            errors.append(f"trace references unknown node {node_id!r}")
    for node_id in sorted(node_ids):
        count = traced_counts.get(node_id, 0)
        if count == 0:
            errors.append(f"node {node_id} is not present in any section trace")
        elif count > 1:
            warnings.append(f"node {node_id} appears {count} times in traces")

    for location, text in narrative_strings(data):
        for reference_group in REFERENCE_RE.findall(text):
            for node_id in [item.strip() for item in reference_group.split(",")]:
                if node_id not in node_ids:
                    errors.append(f"{location} references unknown node [{node_id}]")

    edge_ids: set[str] = set()
    connected: set[str] = set()
    edge_signatures: set[tuple[str, str, str]] = set()
    for index, edge in enumerate(edges):
        label = f"edges[{index}]"
        if not isinstance(edge, dict):
            errors.append(f"{label} must be an object")
            continue
        edge_id = edge.get("id")
        if not isinstance(edge_id, str) or not edge_id:
            errors.append(f"{label}.id must be a non-empty string")
        elif edge_id in edge_ids:
            errors.append(f"duplicate edge id: {edge_id}")
        else:
            edge_ids.add(edge_id)
        source = edge.get("from")
        target = edge.get("to")
        if not isinstance(source, str) or source not in node_ids:
            errors.append(f"{label}.from references unknown node {source!r}")
        if not isinstance(target, str) or target not in node_ids:
            errors.append(f"{label}.to references unknown node {target!r}")
        if isinstance(source, str) and source == target and source in node_ids:
            warnings.append(f"{label} is a self-edge")
        if isinstance(source, str) and source in node_ids:
            connected.add(source)
        if isinstance(target, str) and target in node_ids:
            connected.add(target)
        signature = (str(source), str(target), str(edge.get("label")))
        if signature in edge_signatures:
            warnings.append(f"duplicate edge relationship: {signature[0]} -> {signature[1]} ({signature[2]})")
        edge_signatures.add(signature)
        status = edge.get("status")
        evidence = edge.get("evidence")
        if not isinstance(evidence, list):
            errors.append(f"{label}.evidence must be an array")
            evidence = []
        if status == "verified" and not evidence:
            errors.append(f"{label} is verified but has no direct relationship evidence")
        if status == "inferred" and not isinstance(edge.get("reason"), str):
            errors.append(f"{label} is inferred and requires a reason")
        for evidence_index, item in enumerate(evidence):
            evidence_label = f"{label}.evidence[{evidence_index}]"
            validate_anchor(item, evidence_label, errors)
            if not isinstance(item, dict) or not isinstance(item.get("claim"), str) or not item.get("claim"):
                errors.append(f"{evidence_label}.claim must be a non-empty string")

    disconnected = sorted(node_ids - connected)
    if disconnected and len(node_ids) > 1:
        warnings.append(f"nodes without any explicit edge: {', '.join(disconnected)}")
    inferred_count = sum(1 for edge in edges if isinstance(edge, dict) and edge.get("status") == "inferred")
    if inferred_count:
        warnings.append(f"codemap contains {inferred_count} inferred edge(s); verify that guide wording preserves uncertainty")

    return errors, warnings


def git_output(repository: Path, *args: str) -> str | None:
    try:
        result = subprocess.run(
            ["git", "-C", str(repository), *args],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
            text=True,
            timeout=30,
        )
    except (OSError, subprocess.SubprocessError):
        return None
    return result.stdout.strip()


def repository_state(repository: Path) -> tuple[str | None, str]:
    head = git_output(repository, "rev-parse", "HEAD")
    if head is None:
        return None, "unknown"
    status = git_output(repository, "status", "--porcelain")
    return head, "dirty" if status else "clean"


def resolve_sources(
    data: dict[str, Any],
    repository: Path,
    *,
    allow_dirty: bool = False,
    allow_non_git: bool = False,
) -> tuple[dict[str, Any], list[str], list[str]]:
    errors: list[str] = []
    warnings: list[str] = []
    repository = repository.resolve()
    if not repository.is_dir():
        return {}, [f"repository not found: {repository}"], warnings

    declared = data.get("meta", {}).get("repository", {})
    actual_head, actual_state = repository_state(repository)
    declared_revision = declared.get("revision") if isinstance(declared, dict) else None
    declared_state = declared.get("workingTree") if isinstance(declared, dict) else None
    template_mode = data.get("meta", {}).get("template") is True

    if actual_head is None:
        if not allow_non_git and not template_mode:
            errors.append("repository is not a Git checkout; pass --allow-non-git only for intentional non-Git analysis")
        else:
            warnings.append("repository is not a Git checkout; revision matching was skipped")
    else:
        if isinstance(declared_revision, str) and declared_revision not in {"working-tree", actual_head}:
            if not (len(declared_revision) >= 7 and actual_head.startswith(declared_revision)):
                errors.append(f"declared revision {declared_revision!r} does not match HEAD {actual_head}")
        if declared_state in {"clean", "dirty"} and declared_state != actual_state:
            errors.append(f"declared workingTree={declared_state!r} does not match checkout state {actual_state!r}")
        if actual_state == "dirty" and not allow_dirty and declared_revision != "working-tree":
            warnings.append("repository has uncommitted changes; record workingTree=dirty or revision=working-tree")

    snapshot: dict[str, Any] = {
        "schemaVersion": SNAPSHOT_VERSION,
        "repository": {
            "name": declared.get("name", repository.name) if isinstance(declared, dict) else repository.name,
            "revision": actual_head or declared_revision or "non-git",
            "workingTree": actual_state if actual_head else declared_state or "unknown",
        },
        "nodes": {},
        "edgeEvidence": {},
    }

    def resolve_anchor(anchor: dict[str, Any], label: str) -> dict[str, Any] | None:
        relative = anchor.get("path")
        if not isinstance(relative, str):
            errors.append(f"{label}.path is invalid")
            return None
        candidate = (repository / relative).resolve()
        try:
            candidate.relative_to(repository)
        except ValueError:
            errors.append(f"{label}.path escapes the repository: {relative}")
            return None
        if not candidate.is_file():
            errors.append(f"{label}.path does not exist: {relative}")
            return None
        try:
            lines = candidate.read_text(encoding="utf-8").splitlines()
        except UnicodeDecodeError:
            errors.append(f"{label}.path is not valid UTF-8: {relative}")
            return None
        start = int(anchor.get("startLine", 0))
        end = int(anchor.get("endLine", 0))
        target = int(anchor.get("targetLine", 0))
        if start < 1 or end > len(lines) or target < start or target > end:
            errors.append(f"{label} range {start}-{end} target {target} is outside {relative} (1-{len(lines)})")
            return None
        actual = lines[target - 1]
        expected = str(anchor.get("snippet", ""))
        if actual.strip() != expected.strip():
            errors.append(
                f"{label}.snippet mismatch at {relative}:{target}: expected {expected!r}, found {actual.strip()!r}"
            )
            return None
        if end - start + 1 > 80:
            warnings.append(f"{label} spans {end - start + 1} lines; prefer a smaller source range")
        return {
            "path": relative,
            "startLine": start,
            "endLine": end,
            "targetLine": target,
            "lines": lines[start - 1 : end],
        }

    for node in data.get("nodes", []):
        if not isinstance(node, dict) or not isinstance(node.get("source"), dict):
            continue
        resolved = resolve_anchor(node["source"], f"node {node.get('id')}")
        if resolved is not None:
            snapshot["nodes"][node["id"]] = resolved

    for edge in data.get("edges", []):
        if not isinstance(edge, dict):
            continue
        resolved_items = []
        for index, evidence in enumerate(edge.get("evidence", [])):
            if not isinstance(evidence, dict):
                continue
            resolved = resolve_anchor(evidence, f"edge {edge.get('id')} evidence[{index}]")
            if resolved is not None:
                resolved["claim"] = evidence.get("claim", "")
                resolved_items.append(resolved)
        snapshot["edgeEvidence"][edge.get("id", f"edge-{len(snapshot['edgeEvidence'])}")] = resolved_items

    return snapshot, errors, warnings
