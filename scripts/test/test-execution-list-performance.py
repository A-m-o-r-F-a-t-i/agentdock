#!/usr/bin/env python3
"""Measure the actual Core conversation API with isolated on-disk fixtures.

The count is synthetic. HTTP handlers, registry I/O and pagination are real;
this does not measure the user's production journal or desktop rendering.
"""
from __future__ import annotations

import argparse
import copy
import importlib.util
import json
from pathlib import Path
import time

source = Path(__file__).with_name("test-execution-upgrade.py")
spec = importlib.util.spec_from_file_location("execution_upgrade_fixture", source)
assert spec is not None and spec.loader is not None
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def run(binary: Path, version: str, root: Path, count: int, title_mode: str) -> dict:
    fixture = module.Fixture(root)
    report = {"passed": False, "count": count, "title_mode": title_mode, "measurements": [],
              "scope": "Synthetic persisted conversations; real Core HTTP registry queries"}
    try:
        fixture.start(binary, version)
        created = fixture.tool("performance-template", "list_dir", {"path": "."})
        fixture.stop()
        registry = fixture.home / "execution" / "conversations.json"
        document = json.loads(registry.read_text(encoding="utf-8"))
        sample = document["items"][created["conversation_id"]]
        document["items"] = {}
        for index in range(count):
            record = copy.deepcopy(sample)
            identity = f"conv_{index + 1:032x}"
            record.update(conversation_id=identity, title=f"真实磁盘样本 {index + 1:04d}",
                          title_source="manual", source_key="")
            if title_mode == "legacy":
                record["title"] = "新对话"
                record.pop("title_source", None)
            document["items"][identity] = record
        registry.write_text(json.dumps(document, ensure_ascii=False), encoding="utf-8")
        fixture.start(binary, version)
        for label, path in (("first_page", "conversations?limit=200"),
                            ("second_page", "conversations?limit=200&offset=200"),
                            ("search", "conversations?limit=200&search=0999")):
            started = time.perf_counter()
            status, result = fixture.request("/internal/runtime/" + path)
            elapsed = round((time.perf_counter() - started) * 1000, 2)
            entry = {"query": label, "status": status, "elapsed_ms": elapsed,
                     "returned": len(result.get("conversations", [])), "total": result.get("total")}
            report["measurements"].append(entry)
            print(json.dumps(entry), flush=True)
            module.require(status == 200, f"Conversation API timed out/failed: {status} {result}")
            module.require(elapsed < 5000, f"Conversation query exceeded 5 seconds: {elapsed}ms")
        report["passed"] = True
    except Exception as error:
        report["error"] = str(error)
        raise
    finally:
        fixture.stop()
        (root / "result.json").write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
    return report


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--version", default="1.1.4")
    parser.add_argument("--root", required=True, type=Path)
    parser.add_argument("--count", type=int, default=1000)
    parser.add_argument("--title-mode", choices=("manual", "legacy"), default="manual")
    args = parser.parse_args()
    module.require(not args.root.exists(), "Provide a fresh isolated test directory")
    module.require(400 <= args.count <= 5000, "Count must be between 400 and 5000")
    print(json.dumps(run(args.binary.resolve(), args.version, args.root.resolve(), args.count, args.title_mode), indent=2))
