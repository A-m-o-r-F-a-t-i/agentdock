#!/usr/bin/env python3
"""Validate the WB04 activity contract without network or production access."""

from __future__ import annotations

import argparse
import json
from datetime import datetime
from pathlib import Path
from typing import Any


REPO = Path(__file__).resolve().parents[3]
CONTRACT = REPO / "api" / "workbench" / "v1" / "activity"


def load_json(name: str) -> dict[str, Any]:
    with (CONTRACT / name).open("r", encoding="utf-8") as handle:
        value = json.load(handle)
    if not isinstance(value, dict):
        raise AssertionError(f"{name} must contain a JSON object")
    return value


def timestamp(value: str) -> datetime:
    parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    if parsed.tzinfo is None:
        raise AssertionError(f"timestamp lacks a timezone: {value}")
    return parsed


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()

    assertions = 0

    def check(condition: bool, message: str) -> None:
        nonlocal assertions
        assertions += 1
        if not condition:
            raise AssertionError(message)

    schema = load_json("conversation-status.schema.json")
    fixture = load_json("conversation-status.fixture.json")
    boundaries = load_json("activity-boundaries.fixture.json")

    windows = {
        "recent_interaction_window_ms": 120_000,
        "insertion_eligibility_window_ms": 180_000,
        "unclaimed_insertion_expiry_ms": 300_000,
        "receipt_wait_ms": 30_000,
    }
    for field, expected in windows.items():
        check(fixture.get(field) == expected, f"runtime fixture changed {field}")
        check(schema["properties"][field]["const"] == expected, f"schema changed {field}")

    check(fixture.get("schema_version") == 1, "runtime fixture schema version changed")
    check(boundaries.get("schema_version") == 1, "boundary fixture schema version changed")
    check(boundaries.get("interval_convention") == "half-open", "time intervals must remain half-open")

    formulas = {
        "recent_interaction": lambda elapsed: 0 <= elapsed < windows["recent_interaction_window_ms"],
        "insertion_eligibility": lambda elapsed: 0 <= elapsed < windows["insertion_eligibility_window_ms"],
        "unclaimed_insertion_expired": lambda elapsed: elapsed >= windows["unclaimed_insertion_expiry_ms"],
        "receipt_wait_elapsed": lambda elapsed: elapsed >= windows["receipt_wait_ms"],
    }
    for name, formula in formulas.items():
        cases = boundaries["cases"].get(name)
        check(isinstance(cases, list) and len(cases) >= 3, f"missing boundary vector: {name}")
        for case in cases:
            elapsed = case["elapsed_ms"]
            check(case["expected"] is formula(elapsed), f"wrong {name} expectation at {elapsed} ms")

    server_now = timestamp(fixture["server_now"])
    group_ids: set[str] = set()
    navigation_ids: set[str] = set()
    unattributed_rows = 0
    rows_by_id: dict[str, dict[str, Any]] = {}

    for group in fixture["groups"]:
        group_id = group["workspace_id"]
        check(bool(group_id) and not group_id.startswith("footer:"), f"invalid workspace identity: {group_id!r}")
        check(group_id not in group_ids, f"duplicate workspace identity: {group_id}")
        group_ids.add(group_id)
        recent_count = 0
        execution_count = 0
        for row in group["conversations"]:
            conversation_id = row["conversation_id"]
            is_unattributed = row["is_unattributed"]
            if is_unattributed:
                unattributed_rows += 1
                check(group_id == "unattributed", "unattributed row escaped its typed group")
                check(conversation_id == "", "unattributed row acquired a fabricated ID")
                check(not row["recently_active"] and not row["in_flight"], "unattributed row became active navigation")
                navigation_key = "unattributed"
            else:
                check(bool(conversation_id), "ordinary conversation has an empty ID")
                check(conversation_id != "unattributed" and not conversation_id.startswith("footer:"), "reserved navigation key collision")
                navigation_key = conversation_id
                rows_by_id[conversation_id] = row
            check(navigation_key not in navigation_ids, f"duplicate navigation identity: {navigation_key}")
            navigation_ids.add(navigation_key)

            if row["recently_active"]:
                recent_count += 1
            if row["in_flight"]:
                execution_count += 1

            last_raw = row.get("last_interaction_at")
            expiry_raw = row.get("interaction_expires_at")
            if last_raw is None:
                check(not row["recently_active"], "row without interaction evidence is marked recent")
                check(expiry_raw is None, "interaction expiry exists without an interaction timestamp")
            else:
                check(expiry_raw is not None, "interaction timestamp lacks an explicit expiry")
                last = timestamp(last_raw)
                expiry = timestamp(expiry_raw)
                elapsed_ms = int((expiry - last).total_seconds() * 1000)
                check(elapsed_ms == windows["recent_interaction_window_ms"], "interaction expiry is not exactly 120 seconds")
                expected_recent = not is_unattributed and last <= server_now < expiry
                check(row["recently_active"] is expected_recent, f"recent activity mismatch for {conversation_id}")
                stats_interaction = row["statistics"].get("last_interaction_at")
                if stats_interaction is not None:
                    check(stats_interaction == last_raw, f"top-level/statistics interaction mismatch for {conversation_id}")

        check(group["recent_count"] == recent_count, f"recent_count mismatch for {group_id}")
        check(group["execution_count"] == execution_count, f"execution_count mismatch for {group_id}")

    check(unattributed_rows == 1, "fixture must contain exactly one typed unattributed row")

    daemon = rows_by_id["conv_daemon"]
    check(daemon["in_flight"] and not daemon["recently_active"], "long-running process was folded into recent interaction")
    check(timestamp(daemon["last_activity_at"]) > timestamp(daemon["interaction_expires_at"]), "daemon output did not outlive the RPC interaction window")

    approval = rows_by_id["conv_approval"]
    check(approval["in_flight"] and approval["statistics"]["pending"] == 1, "pending approval is not separately live")
    check(not approval["recently_active"], "expired approval incorrectly extends recent interaction")

    long_rpc = rows_by_id["conv_long_rpc"]
    request_at = timestamp(long_rpc["statistics"]["last_tool_call_at"])
    interaction_at = timestamp(long_rpc["last_interaction_at"])
    check((interaction_at - request_at).total_seconds() == 300, "long synchronous RPC fixture lost its completion boundary")
    check(long_rpc["recently_active"] and not long_rpc["in_flight"], "completed long RPC is not represented independently")

    report = {
        "status": "passed",
        "assertions": assertions,
        "contract": "workbench.activity.v1",
        "fixtures": [
            "conversation-status.fixture.json",
            "activity-boundaries.fixture.json",
        ],
        "production_modified": False,
    }
    rendered = json.dumps(report, ensure_ascii=False, indent=2) + "\n"
    if args.output is not None:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(rendered, encoding="utf-8")
    print(rendered, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
