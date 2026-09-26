#!/usr/bin/env python3
"""Verify both hosted-runner Linux candidates came from one immutable commit."""
from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import re
from typing import Any


def digest(path: Path) -> str:
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def load_object(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise RuntimeError(f"{path} must contain a JSON object")
    return value


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()

    if not re.fullmatch(r"[a-f0-9]{40}", args.commit):
        raise RuntimeError("commit must be a full hexadecimal SHA")
    manifests = sorted(args.root.rglob("candidate-manifest-linux-*.json"))
    if len(manifests) != 2:
        raise RuntimeError(f"expected two Linux candidate manifests, found {len(manifests)}")

    candidates: list[dict[str, Any]] = []
    platforms: set[str] = set()
    for manifest_path in manifests:
        manifest = load_object(manifest_path)
        platform = manifest.get("platform")
        if platform not in {"linux/amd64", "linux/arm64"}:
            raise RuntimeError(f"unexpected candidate platform: {platform!r}")
        if platform in platforms:
            raise RuntimeError(f"duplicate candidate platform: {platform}")
        platforms.add(str(platform))
        if manifest.get("source_commit") != args.commit:
            raise RuntimeError(f"source commit mismatch in {manifest_path}")
        if manifest.get("version") != args.version:
            raise RuntimeError(f"version mismatch in {manifest_path}")
        if manifest.get("candidate_only") is not True or manifest.get("published") is not False:
            raise RuntimeError(f"candidate publication flags are invalid in {manifest_path}")
        acceptance = manifest.get("acceptance")
        if not isinstance(acceptance, dict) or set(acceptance) != {"deb", "rpm"}:
            raise RuntimeError(f"native package acceptance is incomplete in {manifest_path}")
        for package_type, result in acceptance.items():
            if result != {
                "installed": True,
                "removed": True,
                "preserved_existing_home": True,
            }:
                raise RuntimeError(f"{package_type} acceptance failed in {manifest_path}")

        assets = manifest.get("assets")
        if not isinstance(assets, dict) or len(assets) != 3:
            raise RuntimeError(f"unexpected asset manifest in {manifest_path}")
        verified_assets: dict[str, str] = {}
        for name, expected in sorted(assets.items()):
            if not isinstance(name, str) or not isinstance(expected, str):
                raise RuntimeError(f"invalid asset entry in {manifest_path}")
            asset = manifest_path.parent / name
            if not asset.is_file():
                raise RuntimeError(f"downloaded artifact is missing: {asset}")
            actual = digest(asset)
            if actual != expected:
                raise RuntimeError(f"downloaded artifact digest mismatch: {asset}")
            verified_assets[name] = actual
        candidates.append(
            {
                "platform": platform,
                "artifact_directory": manifest_path.parent.name,
                "assets": verified_assets,
                "acceptance": acceptance,
            }
        )

    if platforms != {"linux/amd64", "linux/arm64"}:
        raise RuntimeError(f"candidate platform set is incomplete: {sorted(platforms)}")

    index = {
        "schema_version": 1,
        "candidate_only": True,
        "published": False,
        "source_commit": args.commit,
        "version": args.version,
        "verified_at": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "platforms": sorted(candidates, key=lambda item: str(item["platform"])),
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(index, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(index, ensure_ascii=False, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
