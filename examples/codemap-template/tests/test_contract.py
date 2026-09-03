from __future__ import annotations

import argparse
import copy
import contextlib
import importlib
import inspect
import io
import json
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
SCRIPTS = ROOT / "scripts"
sys.path.insert(0, str(SCRIPTS))

import build as build_script  # noqa: E402
import import_codeatlas as import_script  # noqa: E402
import package as package_script  # noqa: E402
import render_preview  # noqa: E402
import smoke_test  # noqa: E402
import validate as validate_script  # noqa: E402
from codemap_lib import git_output, load_json, resolve_sources, validate_codemap  # noqa: E402
from validate import schema_errors  # noqa: E402


class ContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.sample = load_json(ROOT / "codemap.json")
        cls.schema = ROOT / "codemap.schema.json"
        cls.fixture = ROOT / "fixtures" / "example-repository"

    def finalized(self) -> dict:
        data = copy.deepcopy(self.sample)
        data["meta"].update(
            {
                "id": "codemap:feature-delivery",
                "template": False,
                "title": "Feature Delivery Codemap",
                "summary": "A verified trace of feature delivery across boundaries [1a, 2a, 3a].",
                "query": "How does feature delivery reach durable side effects?",
            }
        )
        data["meta"]["repository"] = {
            "name": "owner/repository",
            "revision": "0123456789abcdef0123456789abcdef01234567",
            "workingTree": "clean",
        }
        return data

    def test_sample_is_valid_in_template_mode(self) -> None:
        errors, warnings = validate_codemap(self.sample)
        self.assertEqual(errors, [])
        self.assertTrue(any("template=true" in item for item in warnings))
        self.assertEqual(schema_errors(self.sample, self.schema), [])

    def test_strict_mode_rejects_placeholder(self) -> None:
        errors, _ = validate_codemap(self.sample, strict=True)
        self.assertTrue(any("meta.template=false" in item for item in errors))
        self.assertTrue(any("unresolved placeholders" in item for item in errors))
        self.assertTrue(any("real repository revision" in item for item in errors))

    def test_finalized_copy_passes_strict_semantics(self) -> None:
        errors, _ = validate_codemap(self.finalized(), strict=True)
        self.assertEqual(errors, [])

    def test_manual_coordinates_are_rejected(self) -> None:
        data = self.finalized()
        data["nodes"][0]["x"] = 120
        errors, _ = validate_codemap(data, strict=True)
        self.assertTrue(any("manual map coordinates" in item for item in errors))
        self.assertTrue(any("Additional properties" in item for item in schema_errors(data, self.schema)))

    def test_unknown_narrative_reference_is_rejected(self) -> None:
        data = self.finalized()
        data["meta"]["summary"] += " Unknown path [9z]."
        errors, _ = validate_codemap(data, strict=True)
        self.assertTrue(any("unknown node [9z]" in item for item in errors))

    def test_verified_edge_requires_relationship_evidence(self) -> None:
        data = self.finalized()
        data["edges"][0]["evidence"] = []
        errors, _ = validate_codemap(data, strict=True)
        self.assertTrue(any("verified but has no direct relationship evidence" in item for item in errors))

    def test_inferred_edge_requires_reason(self) -> None:
        data = self.finalized()
        inferred = next(edge for edge in data["edges"] if edge["status"] == "inferred")
        inferred.pop("reason", None)
        errors, _ = validate_codemap(data, strict=True)
        self.assertTrue(any("is inferred and requires a reason" in item for item in errors))

    def test_repository_resolution_derives_context(self) -> None:
        snapshot, errors, warnings = resolve_sources(self.sample, self.fixture, allow_non_git=True)
        self.assertEqual(errors, [])
        self.assertTrue(warnings)
        self.assertEqual(set(snapshot["nodes"]), {node["id"] for node in self.sample["nodes"]})
        target = snapshot["nodes"]["2b"]
        self.assertEqual(target["targetLine"], 16)
        self.assertIn("featureRepository.find", "\n".join(target["lines"]))

    def test_repository_resolution_detects_snippet_mismatch(self) -> None:
        data = copy.deepcopy(self.sample)
        data["nodes"][0]["source"]["snippet"] = "not the source line"
        _, errors, _ = resolve_sources(data, self.fixture, allow_non_git=True)
        self.assertTrue(any("snippet mismatch" in item for item in errors))

    def test_canonical_payload_has_no_source_context_or_coordinates(self) -> None:
        serialized = json.dumps(self.sample)
        self.assertNotIn("sourceContext", serialized)
        self.assertNotIn("contextStartLine", serialized)
        for node in self.sample["nodes"]:
            self.assertFalse({"x", "y", "width", "height", "map"} & set(node))

    def test_viewer_avoids_inner_html_rendering(self) -> None:
        javascript = (ROOT / "assets" / "viewer.js").read_text(encoding="utf-8")
        self.assertNotIn("innerHTML", javascript)
        self.assertIn("createElement", javascript)
        reference = (ROOT.parent / "codemap-template-reference" / "app.js").read_text(encoding="utf-8")
        self.assertNotIn("innerHTML", reference)

    def test_legacy_import_rejects_non_object_roots(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "legacy.json"
            output = Path(directory) / "converted.json"
            for value in ([], "legacy", None):
                with self.subTest(value=value):
                    source.write_text(json.dumps(value), encoding="utf-8")
                    output.unlink(missing_ok=True)
                    result = subprocess.run(
                        [sys.executable, str(SCRIPTS / "import_codeatlas.py"), str(source), str(output)],
                        text=True,
                        stdout=subprocess.PIPE,
                        stderr=subprocess.PIPE,
                    )
                    self.assertEqual(result.returncode, 2)
                    self.assertIn("expected a JSON object", result.stderr)
                    self.assertNotIn("Traceback", result.stderr)
                    self.assertFalse(output.exists())

    def test_legacy_import_rejects_non_positive_section_numbers(self) -> None:
        legacy = {
            "schemaVersion": "codeatlas-codemap/v3",
            "repository": {},
            "nodes": [],
            "edges": [],
            "sections": [{"number": 1, "title": "Flow", "flow": {"steps": []}}],
        }
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "legacy.json"
            output = Path(directory) / "converted.json"
            for number in (0, -1):
                with self.subTest(number=number):
                    legacy["sections"][0]["number"] = number
                    source.write_text(json.dumps(legacy), encoding="utf-8")
                    output.unlink(missing_ok=True)
                    result = subprocess.run(
                        [sys.executable, str(SCRIPTS / "import_codeatlas.py"), str(source), str(output)],
                        text=True,
                        stdout=subprocess.PIPE,
                        stderr=subprocess.PIPE,
                    )
                    self.assertEqual(result.returncode, 2)
                    self.assertIn("invalid number", result.stderr)
                    self.assertNotIn("Traceback", result.stderr)
                    self.assertFalse(output.exists())

    def test_legacy_import_marks_edges_inferred(self) -> None:
        legacy = {
            "schemaVersion": "codeatlas-codemap/v3",
            "id": "codemap:test",
            "title": "Legacy",
            "overview": "Legacy overview",
            "generatedAt": "2026-08-21T00:00:00Z",
            "repository": {"name": "owner/repo", "revision": "abcdef0"},
            "sections": [
                {
                    "id": "section:test",
                    "number": 1,
                    "title": "Flow",
                    "summary": "Flow summary",
                    "guide": {"motivation": "Why", "details": "How"},
                    "flow": {
                        "title": "Main",
                        "steps": [
                            {
                                "id": "step:1a",
                                "label": "1a",
                                "nodeId": "node:a",
                                "title": "A",
                                "source": {"path": "a.py", "startLine": 1, "endLine": 1},
                                "snippet": "a()",
                            },
                            {
                                "id": "step:1b",
                                "label": "1b",
                                "nodeId": "node:b",
                                "title": "B",
                                "source": {"path": "b.py", "startLine": 1, "endLine": 1},
                                "snippet": "b()",
                            },
                        ],
                    },
                }
            ],
            "nodes": [
                {"id": "node:a", "label": "a", "group": "Core", "kind": "function"},
                {"id": "node:b", "label": "b", "group": "Core", "kind": "function"},
            ],
            "edges": [{"id": "edge:a-b", "source": "node:a", "target": "node:b", "type": "calls", "label": "calls"}],
        }
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "legacy.json"
            output = Path(directory) / "converted.json"
            source.write_text(json.dumps(legacy), encoding="utf-8")
            result = subprocess.run(
                [sys.executable, str(SCRIPTS / "import_codeatlas.py"), str(source), str(output)],
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            converted = load_json(output)
            self.assertTrue(converted["meta"]["template"])
            self.assertEqual(converted["edges"][0]["status"], "inferred")
            self.assertEqual(converted["edges"][0]["evidence"], [])
            self.assertIn("direct relationship evidence", converted["edges"][0]["reason"])

    def test_schema_rejects_trailing_newlines_in_paths_and_snippets(self) -> None:
        schema = load_json(self.schema)
        patterns = {
            "path": schema["$defs"]["relativePath"]["pattern"],
            "snippet": schema["$defs"]["singleLineSnippet"]["pattern"],
        }
        for field, value in (("path", "src/a.ts\n"), ("snippet", "source line\n")):
            with self.subTest(field=field):
                self.assertIsNone(re.search(patterns[field], value))

    def test_malformed_narrative_members_report_errors_without_traceback(self) -> None:
        data = self.finalized()
        data["meta"] = []
        data["sections"] = [[], {"summary": "valid", "guide": {"details": [[], {"type": "paragraph", "text": "ok"}]}}]
        try:
            errors, _ = validate_codemap(data)
        except Exception as exc:  # pragma: no cover - regression assertion
            self.fail(f"validator raised instead of reporting malformed narrative members: {exc}")
        self.assertTrue(errors)

    def test_unhashable_relation_members_report_errors_without_traceback(self) -> None:
        data = self.finalized()
        data["groups"][0]["sectionId"] = []
        data["presentation"]["map"]["groupOrder"] = [data["groups"][0]["id"], []]
        data["nodes"][0]["groupId"] = {}
        data["edges"][0]["from"] = []
        try:
            errors, _ = validate_codemap(data)
        except Exception as exc:  # pragma: no cover - regression assertion
            self.fail(f"validator raised instead of reporting unhashable members: {exc}")
        self.assertTrue(errors)

    def test_legacy_import_generates_contract_ids_beyond_z(self) -> None:
        steps = []
        legacy_nodes = []
        for index in range(28):
            legacy_id = f"node:{index}"
            legacy_nodes.append({"id": legacy_id, "label": f"Node {index}", "group": "Core", "kind": "function"})
            steps.append(
                {
                    "label": f"Step {index}",
                    "nodeId": legacy_id,
                    "title": f"Node {index}",
                    "source": {"path": "a.py", "startLine": 1, "endLine": 1},
                    "snippet": "call()",
                }
            )
        legacy = {
            "schemaVersion": "codeatlas-codemap/v3",
            "repository": {},
            "nodes": legacy_nodes,
            "edges": [],
            "sections": [{"number": 1, "title": "Flow", "summary": "Summary", "guide": {}, "flow": {"steps": steps}}],
        }
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "legacy.json"
            output = Path(directory) / "converted.json"
            source.write_text(json.dumps(legacy), encoding="utf-8")
            result = subprocess.run(
                [sys.executable, str(SCRIPTS / "import_codeatlas.py"), str(source), str(output)],
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            node_ids = [node["id"] for node in load_json(output)["nodes"]]
            self.assertEqual((node_ids[0], node_ids[25], node_ids[26], node_ids[27]), ("1a", "1z", "1aa", "1ab"))

    def test_legacy_import_uses_default_for_non_string_guide_details(self) -> None:
        legacy = {
            "schemaVersion": "codeatlas-codemap/v3",
            "repository": {},
            "nodes": [],
            "edges": [],
            "sections": [{"number": 1, "title": "Flow", "summary": "Summary", "guide": {"details": ["not prose"]}, "flow": {"steps": []}}],
        }
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "legacy.json"
            output = Path(directory) / "converted.json"
            source.write_text(json.dumps(legacy), encoding="utf-8")
            result = subprocess.run([sys.executable, str(SCRIPTS / "import_codeatlas.py"), str(source), str(output)])
            self.assertEqual(result.returncode, 0)
            detail = load_json(output)["sections"][0]["guide"]["details"][0]["text"]
            self.assertEqual(detail, "Imported details requiring review.")

    def test_legacy_import_normalizes_non_object_members(self) -> None:
        legacy = {
            "schemaVersion": "codeatlas-codemap/v3",
            "repository": [],
            "nodes": [],
            "edges": [],
            "sections": [{"number": 1, "title": "Flow", "summary": "Summary", "guide": [], "flow": []}],
        }
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "legacy.json"
            output = Path(directory) / "converted.json"
            source.write_text(json.dumps(legacy), encoding="utf-8")
            result = subprocess.run(
                [sys.executable, str(SCRIPTS / "import_codeatlas.py"), str(source), str(output)],
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(load_json(output)["meta"]["repository"]["name"], "unknown/repository")

    def test_legacy_import_normalizes_malformed_source_lines(self) -> None:
        legacy = {
            "schemaVersion": "codeatlas-codemap/v3",
            "repository": {},
            "nodes": [{"id": "node:a", "label": "A", "group": "Core", "kind": "function"}],
            "edges": [],
            "sections": [
                {
                    "number": 1,
                    "title": "Flow",
                    "summary": "Summary",
                    "guide": {},
                    "flow": {
                        "steps": [
                            {
                                "nodeId": "node:a",
                                "title": "A",
                                "source": {"path": "a.py", "startLine": [], "endLine": {}},
                                "snippet": "call()",
                            }
                        ]
                    },
                }
            ],
        }
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "legacy.json"
            output = Path(directory) / "converted.json"
            source.write_text(json.dumps(legacy), encoding="utf-8")
            result = subprocess.run(
                [sys.executable, str(SCRIPTS / "import_codeatlas.py"), str(source), str(output)],
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            converted_source = load_json(output)["nodes"][0]["source"]
            self.assertEqual(converted_source["startLine"], 1)
            self.assertEqual(converted_source["endLine"], 1)
            self.assertEqual(converted_source["targetLine"], 1)

    def test_mobile_dimensions_preserve_explicit_desktop_values(self) -> None:
        resolver = getattr(render_preview, "resolve_dimensions", None)
        self.assertIsNotNone(resolver, "render_preview.resolve_dimensions must exist")
        self.assertEqual(resolver(None, None, False), (1440, 1000))
        self.assertEqual(resolver(None, None, True), (390, 844))
        self.assertEqual(resolver(1440, 1000, True), (1440, 1000))

    def test_preview_rejects_non_positive_dimensions_and_scale(self) -> None:
        self.assertEqual(render_preview.positive_int("1"), 1)
        self.assertEqual(render_preview.positive_float("0.5"), 0.5)
        for parser, value in (
            (render_preview.positive_int, "0"),
            (render_preview.positive_int, "-1"),
            (render_preview.positive_float, "0"),
            (render_preview.positive_float, "-0.5"),
        ):
            with self.subTest(value=value), self.assertRaises(argparse.ArgumentTypeError):
                parser(value)

    def test_static_checks_ignore_network_text_inside_json_payloads(self) -> None:
        html = """<meta http-equiv="Content-Security-Policy"><div id="codemap-sections"></div><div id="map-stage"></div><div id="source-panel"></div>
<script id="codemap-data" type="application/json">{"meta":{"query":"fetch( https://private.invalid"}}</script>
<script id="codemap-sources" type="application/json">{"nodes":{}}</script>"""
        _, _, errors = smoke_test.static_checks(html)
        self.assertNotIn("built viewer contains a network dependency", errors)

    def test_smoke_targets_are_derived_from_payload_nodes(self) -> None:
        target_builder = getattr(smoke_test, "interaction_targets", None)
        self.assertIsNotNone(target_builder, "smoke_test.interaction_targets must exist")
        targets, errors = target_builder({"nodes": [{"id": "9z", "title": "First"}, {"id": "10aa", "title": "Last"}]})
        self.assertEqual(errors, [])
        self.assertEqual(targets["guide"], {"id": "9z", "title": "First"})
        self.assertEqual(targets["map"], {"id": "10aa", "title": "Last"})
        self.assertEqual(targets["mobile"], {"id": "9z", "title": "First"})

    def test_geometry_errors_name_missing_inline_positions(self) -> None:
        checker = getattr(smoke_test, "geometry_errors", None)
        self.assertIsNotNone(checker, "smoke_test.geometry_errors must exist")
        errors = checker([{"id": "1a", "left": None, "top": 1, "width": 10, "height": 10}])
        self.assertEqual(errors, ["map node 1a is missing a finite inline position"])

    def test_schema_skip_is_reported_and_can_be_required(self) -> None:
        self.assertIn("require_schema", inspect.signature(schema_errors).parameters)
        real_import = __import__

        def missing_jsonschema(name, *args, **kwargs):
            if name == "jsonschema":
                raise ImportError("missing for test")
            return real_import(name, *args, **kwargs)

        output = io.StringIO()
        with mock.patch("builtins.__import__", side_effect=missing_jsonschema), contextlib.redirect_stdout(output):
            self.assertEqual(schema_errors(self.sample, self.schema), [])
            required = schema_errors(self.sample, self.schema, require_schema=True)
        self.assertIn("WARNING", output.getvalue())
        self.assertTrue(required)

    def test_missing_implicit_schema_warns_but_explicit_schema_fails(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            codemap = Path(directory) / "codemap.json"
            missing = Path(directory) / "missing.schema.json"
            codemap.write_text(json.dumps(self.sample), encoding="utf-8")
            stdout = io.StringIO()
            stderr = io.StringIO()
            with mock.patch.object(sys, "argv", ["validate.py", str(codemap)]), contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                self.assertEqual(validate_script.main(), 0)
            self.assertIn("schema file not found", stdout.getvalue())
            with mock.patch.object(sys, "argv", ["validate.py", str(codemap), "--schema", str(missing)]), contextlib.redirect_stderr(stderr):
                self.assertEqual(validate_script.main(), 1)
            with mock.patch.object(sys, "argv", ["validate.py", str(codemap), "--require-schema"]), contextlib.redirect_stderr(stderr):
                self.assertEqual(validate_script.main(), 1)

    def test_build_reports_malformed_snapshot_without_traceback(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            snapshot = Path(directory) / "snapshot.json"
            output = Path(directory) / "index.html"
            snapshot.write_text("{", encoding="utf-8")
            result = subprocess.run(
                [
                    sys.executable,
                    str(SCRIPTS / "build.py"),
                    "--root",
                    str(ROOT),
                    "--snapshot",
                    str(snapshot),
                    "--output",
                    str(output),
                ],
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            self.assertEqual(result.returncode, 2)
            self.assertIn("cannot read source snapshot", result.stderr)
            self.assertNotIn("Traceback", result.stderr)

    def test_build_rejects_missing_explicit_snapshot(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            snapshot = Path(directory) / "missing.json"
            output = Path(directory) / "index.html"
            result = subprocess.run(
                [
                    sys.executable,
                    str(SCRIPTS / "build.py"),
                    "--root",
                    str(ROOT),
                    "--snapshot",
                    str(snapshot),
                    "--output",
                    str(output),
                ],
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            self.assertEqual(result.returncode, 2)
            self.assertIn("source snapshot not found", result.stderr)
            self.assertFalse(output.exists())

    def test_git_output_times_out_and_returns_none(self) -> None:
        seen: dict = {}

        def blocked(*args, **kwargs):
            seen.update(kwargs)
            raise subprocess.TimeoutExpired(args[0], kwargs.get("timeout"))

        with mock.patch("codemap_lib.subprocess.run", side_effect=blocked):
            try:
                result = git_output(ROOT, "status")
            except Exception as exc:  # pragma: no cover - regression assertion
                self.fail(f"git_output propagated a timeout: {exc}")
        self.assertIsNone(result)
        self.assertEqual(seen.get("timeout"), 30)

    def test_browser_launch_discovers_each_candidate_once(self) -> None:
        try:
            browser_lib = importlib.import_module("browser_lib")
        except ImportError as exc:  # pragma: no cover - regression assertion
            self.fail(f"shared browser helper is missing: {exc}")

        class Chromium:
            def __init__(self) -> None:
                self.options = None

            def launch(self, **options):
                self.options = options
                return "browser"

        chromium = Chromium()
        playwright = type("Playwright", (), {"chromium": chromium})()
        with mock.patch("browser_lib.shutil.which", side_effect=[None, "C:/chromium.exe"]) as which:
            self.assertEqual(browser_lib.browser_launch(playwright), "browser")
        self.assertEqual(which.call_count, 2)
        self.assertEqual(chromium.options["executable_path"], "C:/chromium.exe")

    def test_manifest_check_reports_missing_and_mismatched_files(self) -> None:
        checker = getattr(package_script, "manifest_errors", None)
        self.assertIsNotNone(checker, "package.manifest_errors must exist")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "present.txt").write_text("actual", encoding="utf-8")
            (root / "unlisted.txt").write_text("not covered", encoding="utf-8")
            (root / "MANIFEST.sha256").write_text("0" * 64 + "  present.txt\n" + "1" * 64 + "  missing.txt\n", encoding="utf-8")
            errors = checker(root)
        self.assertTrue(any("hash mismatch" in error for error in errors))
        self.assertTrue(any("missing file" in error for error in errors))
        self.assertTrue(any("unlisted file" in error for error in errors))

    def test_package_skips_symlinks_before_file_checks(self) -> None:
        root = mock.Mock()
        linked = mock.Mock()
        linked.is_symlink.return_value = True
        linked.is_file.return_value = False
        root.rglob.return_value = [linked]
        self.assertEqual(package_script.included_files(root), [])
        linked.is_file.assert_not_called()

    def test_package_removes_partial_archive(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "root"
            root.mkdir()
            (root / "file.txt").write_text("content", encoding="utf-8")
            output = Path(directory) / "package.zip"
            argv = ["package.py", "--root", str(root), "--output", str(output)]
            with mock.patch.object(sys, "argv", argv), mock.patch.object(package_script, "add_file", side_effect=RuntimeError("boom")):
                with self.assertRaises(RuntimeError):
                    package_script.main()
            self.assertFalse(output.with_suffix(".zip.tmp").exists())

    def test_template_replacement_is_single_pass(self) -> None:
        replace_tokens = getattr(build_script, "replace_tokens", None)
        self.assertIsNotNone(replace_tokens, "build.replace_tokens must exist")
        replacements = {token: token.lower() for token in build_script.TOKENS}
        replacements["__CODEMAP_CSS__"] = "literal __CODEMAP_JS__ in css"
        rendered = replace_tokens("|".join(build_script.TOKENS), replacements)
        self.assertTrue(rendered.startswith("__codemap_csp__|literal __CODEMAP_JS__ in css|"))
        self.assertEqual(rendered.count("literal __CODEMAP_JS__ in css"), 1)


if __name__ == "__main__":
    unittest.main()
