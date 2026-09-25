#!/usr/bin/env python3
"""Validate WB01 native candidate artifacts and write a non-publishing manifest."""
from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import re
from typing import Any

PRODUCT = "AgentDock Workbench"
LICENSE_ID = "Apache-2.0"


def sha256(path: Path) -> str:
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def read_json(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise RuntimeError(f"{path.name} must contain a JSON object")
    return value


def verify_checksum(path: Path) -> str:
    actual = sha256(path)
    checksum_path = path.with_name(path.name + ".sha256")
    if not checksum_path.is_file():
        raise RuntimeError(f"missing checksum: {checksum_path.name}")
    fields = checksum_path.read_text(encoding="utf-8").strip().split()
    if len(fields) != 2 or fields[0] != actual or fields[1] != path.name:
        raise RuntimeError(f"checksum mismatch: {path.name}")
    return actual


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--arch", choices=["amd64", "arm64"], required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--home-sentinel", type=Path, required=True)
    parser.add_argument("--home-sentinel-sha256", required=True)
    args = parser.parse_args()

    if not re.fullmatch(r"[a-f0-9]{40}", args.commit):
        raise RuntimeError("commit must be a full hexadecimal SHA")
    if not re.fullmatch(r"[a-f0-9]{64}", args.home_sentinel_sha256):
        raise RuntimeError("home sentinel digest must be a SHA-256 value")

    output = args.output.resolve()
    rpm_arch = "x86_64" if args.arch == "amd64" else "aarch64"
    expected_names = [
        f"agentdock_linux_{args.arch}.tar.gz",
        f"agentdock-workbench_{args.version}_{args.arch}.deb",
        f"agentdock-workbench-{args.version}-1.{rpm_arch}.rpm",
    ]

    report_path = output / f"verification-linux-{args.arch}.json"
    report = read_json(report_path)
    expected_identity = {
        "product_name": PRODUCT,
        "version": args.version,
        "commit": args.commit,
        "platform": f"linux/{args.arch}",
        "native_execution": "passed",
        "core_skill_bootstrap": "passed",
        "license": LICENSE_ID,
    }
    for key, expected in expected_identity.items():
        if report.get(key) != expected:
            raise RuntimeError(f"verification identity mismatch for {key}: {report.get(key)!r}")

    expected_installation = {
        "deb": "native_installed_verified_removed",
        "rpm": "isolated_root_installed_verified_removed",
        "services_started": False,
    }
    if report.get("package_installation") != expected_installation:
        raise RuntimeError(f"native package acceptance is incomplete: {report.get('package_installation')!r}")

    if not args.home_sentinel.is_file():
        raise RuntimeError("existing AgentDock home sentinel was removed")
    if sha256(args.home_sentinel) != args.home_sentinel_sha256:
        raise RuntimeError("existing AgentDock home sentinel was modified")
    preservation_path = output / f"home-preservation-linux-{args.arch}.json"
    preservation = read_json(preservation_path)
    if preservation.get("sha256") != args.home_sentinel_sha256 or preservation.get("preserved") is not True:
        raise RuntimeError("home preservation report does not match the verified sentinel")

    report_assets = report.get("assets")
    if not isinstance(report_assets, dict) or set(report_assets) != set(expected_names):
        raise RuntimeError(f"unexpected report asset set: {report_assets}")

    artifact_digests: dict[str, str] = {}
    for name in expected_names:
        path = output / name
        if not path.is_file() or path.stat().st_size == 0:
            raise RuntimeError(f"candidate artifact is missing or empty: {name}")
        digest = verify_checksum(path)
        if report_assets.get(name) != digest:
            raise RuntimeError(f"report digest mismatch: {name}")
        artifact_digests[name] = digest

    manifest = {
        "schema_version": 1,
        "candidate_only": True,
        "published": False,
        "product_name": PRODUCT,
        "version": args.version,
        "source_commit": args.commit,
        "platform": f"linux/{args.arch}",
        "license": LICENSE_ID,
        "created_at": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "assets": artifact_digests,
        "acceptance": {
            "deb": {
                "installed": True,
                "removed": True,
                "preserved_existing_home": True,
            },
            "rpm": {
                "installed": True,
                "removed": True,
                "preserved_existing_home": True,
            },
        },
        "services_started": False,
    }
    manifest_path = output / f"candidate-manifest-linux-{args.arch}.json"
    manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(manifest, ensure_ascii=False, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
