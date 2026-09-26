#!/usr/bin/env python3
"""Validate the WB01 control contract, fixtures, docs and candidate workflow."""
from __future__ import annotations

import json
from pathlib import Path
import re
import sys

ROOT = Path(__file__).resolve().parents[3]
API = ROOT / "api/workbench/v1"
WORKFLOW = ROOT / ".github/workflows/parallel-core-cli.yml"
MATRIX = ROOT / "docs/cli/command-matrix.md"
GUIDE = ROOT / "docs/cli/workbench.md"


def fail(message: str) -> None:
    raise RuntimeError(message)


def load_json(path: Path) -> object:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        fail(f"invalid JSON {path.relative_to(ROOT)}: {error}")


def validate_contract() -> None:
    contract = load_json(API / "contract.json")
    if not isinstance(contract, dict):
        fail("contract root must be an object")
    if contract.get("schema_version") != 1 or contract.get("contract_revision") != 3:
        fail("unexpected control contract version")

    expected_exit_codes = {
        "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "130"
    }
    actual_exit_codes = set(contract.get("errors", {}).get("exit_codes", {}))
    if actual_exit_codes != expected_exit_codes:
        fail(f"exit-code contract mismatch: {sorted(actual_exit_codes)}")

    endpoint_commands: set[str] = set()
    endpoints = contract.get("endpoints")
    if not isinstance(endpoints, list) or len(endpoints) < 35:
        fail("endpoint inventory is unexpectedly incomplete")
    for endpoint in endpoints:
        if not isinstance(endpoint, dict):
            fail("endpoint entry must be an object")
        method = endpoint.get("method")
        path = endpoint.get("path")
        commands = endpoint.get("commands")
        if method not in {"GET", "POST", "DELETE"}:
            fail(f"invalid endpoint method: {method!r}")
        if not isinstance(path, str) or not path.startswith("/"):
            fail(f"invalid endpoint path: {path!r}")
        if not isinstance(commands, list) or not commands:
            fail(f"endpoint {method} {path} lacks command mappings")
        endpoint_commands.update(str(command) for command in commands)
        if endpoint.get("local_write") and method == "GET":
            fail(f"read endpoint marked as local write: {path}")

    required_commands = {
        "status",
        "workspace update",
        "task checkpoint",
        "task follow",
        "conversation attach",
        "conversation follow",
        "call logs",
        "call wait",
        "activity watch",
        "insertion send",
        "insertion wait",
        "approval wait",
        "permission set",
    }
    missing = required_commands - endpoint_commands
    if missing:
        fail(f"contract lacks required commands: {sorted(missing)}")

    events = contract.get("events", {})
    if events.get("content_type") != "text/event-stream":
        fail("SSE content type is not frozen")
    backoff = events.get("backoff", {})
    if backoff != {
        "minimum_ms": 250,
        "maximum_ms": 5000,
        "maximum_consecutive_cold_failures": 8,
    }:
        fail(f"unexpected SSE retry contract: {backoff}")

    payloads = contract.get("payloads", {})
    if payloads.get("cursor_unit") != "UTF-8 byte offset":
        fail("payload cursor must remain a UTF-8 byte offset")
    if payloads.get("limit_unit") != "Unicode scalar values":
        fail("payload limit must remain a Unicode scalar budget")

    unavailable = "\n".join(str(item) for item in contract.get("explicitly_unavailable", []))
    for required in [
        "call retry",
        "permission profile CRUD",
        "plugin active test",
        "insertion-specific follow",
    ]:
        if required not in unavailable:
            fail(f"unavailable capability is not explicit: {required}")


    domains = contract.get("domain_contracts", {})
    for name, source, fields in [
        ("permissions", "internal/permission/policy.go", "effective_fields"),
        ("insertions", "internal/insertion/view.go", "public_fields"),
    ]:
        definition = domains.get(name, {})
        text = (ROOT / source).read_text(encoding="utf-8")
        if name == "insertions":
            text += (ROOT / "internal/insertion/store.go").read_text(encoding="utf-8")
        if not definition.get(fields):
            fail(f"missing integrated domain fields: {name}")
        for field in definition[fields]:
            if f'json:"{field}' not in text:
                fail(f"domain field is not implemented by its authority: {name}.{field}")
    activity = domains.get("activity", {})
    for key in ["schema", "fixture", "boundaries_fixture"]:
        if not activity.get(key) or not (API / activity[key]).is_file():
            fail(f"missing integrated activity contract: {key}")


def validate_fixtures() -> None:
    fixtures = API / "fixtures"
    if not fixtures.is_dir():
        fail("fixture directory is missing")
    parsed = 0
    for path in sorted(fixtures.iterdir()):
        if path.suffix == ".json":
            load_json(path)
            parsed += 1
        elif path.suffix == ".jsonl":
            lines = [line for line in path.read_text(encoding="utf-8").splitlines() if line]
            if not lines:
                fail(f"empty JSONL fixture: {path.name}")
            for index, line in enumerate(lines, start=1):
                try:
                    json.loads(line)
                except json.JSONDecodeError as error:
                    fail(f"invalid JSONL {path.name}:{index}: {error}")
            parsed += 1
        elif path.suffix == ".sse":
            text = path.read_text(encoding="utf-8")
            if "id:" not in text or "event:" not in text or "data:" not in text:
                fail(f"invalid SSE fixture: {path.name}")
            parsed += 1
    if parsed < 8:
        fail(f"too few representative fixtures: {parsed}")

    fixture_text = "\n".join(
        path.read_text(encoding="utf-8", errors="replace")
        for path in sorted(fixtures.iterdir())
        if path.is_file()
    )
    secret_patterns = [
        r"gh[pousr]_[A-Za-z0-9]{20,}",
        r"github_pat_[A-Za-z0-9_]{20,}",
        r"Bearer\s+[A-Za-z0-9._~-]{20,}",
        r"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----",
    ]
    for pattern in secret_patterns:
        if re.search(pattern, fixture_text):
            fail(f"fixture appears to contain a credential: {pattern}")


def validate_docs() -> None:
    matrix = MATRIX.read_text(encoding="utf-8")
    guide = GUIDE.read_text(encoding="utf-8")
    for marker in ["已实现", "派生实现", "兼容保留", "部分实现", "未提供"]:
        if marker not in matrix:
            fail(f"command matrix lacks status marker: {marker}")
    for command in [
        "workspace update",
        "task checkpoint",
        "conversation attach",
        "call wait",
        "insertion wait",
        "plugin test",
    ]:
        if command not in matrix:
            fail(f"command matrix lacks {command}")
    guide_plain = guide.replace("**", "")
    for phrase in [
        "UTF-8 字节偏移",
        "Unicode 标量",
        "Last-Event-ID",
        "--confirm-permanent",
        "delivery_unknown",
    ]:
        if phrase not in guide_plain:
            fail(f"operator guide lacks contract phrase: {phrase}")

    source = "\n".join(
        path.read_text(encoding="utf-8")
        for path in sorted((ROOT / "cmd/agentdock").glob("command_control*.go"))
    )
    for fake_success in ["read-contract-passed", '"test": "passed"', "not implemented but successful"]:
        if fake_success in source:
            fail(f"fake-success marker remains in CLI source: {fake_success}")


def validate_workflow() -> None:
    text = WORKFLOW.read_text(encoding="utf-8")
    required = [
        "name: WB01 Core CLI Candidate",
        "parallel/20260925/core-cli",
        "permissions:\n  contents: read",
        "actions/checkout@v5",
        "actions/setup-go@v6",
        "actions/upload-artifact@v7",
        "ubuntu-24.04",
        "ubuntu-24.04-arm",
        "go test -p 2 ./...",
        "go test -race",
        "go vet ./...",
        "packaging/build-unix-release.py",
        "test-linux-native-packages.py",
        "verify_contract.py",
        "smoke_cli.sh",
        "git rev-parse HEAD",
        "GITHUB_SHA",
    ]
    for marker in required:
        if marker not in text:
            fail(f"candidate workflow lacks required marker: {marker}")

    forbidden = [
        "self-hosted",
        "contents: write",
        "packages: write",
        "id-token: write",
        "gh release create",
        "gh release upload",
        "softprops/action-gh-release",
        "ncipollo/release-action",
        "git tag ",
        "git push origin main",
        "pull_request:",
        "push:\n    tags:",
    ]
    for marker in forbidden:
        if marker in text:
            fail(f"candidate workflow contains forbidden publication or runner marker: {marker}")

    runners = re.findall(r"runs-on:\s*([^\n]+)", text)
    allowed = {
        "ubuntu-24.04",
        "${{ matrix.runner }}",
    }
    unexpected = [runner.strip() for runner in runners if runner.strip() not in allowed]
    if unexpected:
        fail(f"candidate workflow uses unexpected runners: {unexpected}")


def main() -> int:
    validate_contract()
    validate_fixtures()
    validate_docs()
    validate_workflow()
    print("WB01 contract, fixtures, docs and candidate workflow: valid")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except RuntimeError as error:
        print(f"WB01 validation failed: {error}", file=sys.stderr)
        raise SystemExit(1)
