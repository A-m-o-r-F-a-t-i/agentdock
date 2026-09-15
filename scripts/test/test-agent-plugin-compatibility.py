#!/usr/bin/env python3
"""Validate public schemas and optionally install/read the fixture in isolated Codex.

Requires jsonschema. No model, agent thread, login, or user Codex configuration is used.
"""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import queue
import shutil
import subprocess
import tempfile
import threading

import jsonschema

REPOSITORY = Path(__file__).resolve().parents[2]


def codex_command(executable: str, *arguments: str) -> list[str]:
    command = [executable, *arguments]
    if os.name == "nt" and executable.lower().endswith((".cmd", ".bat")):
        return ["cmd.exe", "/d", "/c", *command]
    return command


def check_codex(fixture: Path) -> dict:
    executable = shutil.which("codex.cmd" if os.name == "nt" else "codex")
    if not executable:
        raise RuntimeError("Codex CLI is required for --codex")
    with tempfile.TemporaryDirectory(prefix="agentdock-plugin-compat-") as temporary:
        root = Path(temporary)
        marketplace = root / "marketplace"
        installed_source = marketplace / "plugins" / "portable-demo"
        shutil.copytree(fixture, installed_source)
        descriptor = marketplace / ".agents" / "plugins" / "marketplace.json"
        descriptor.parent.mkdir(parents=True)
        descriptor.write_text(json.dumps({"name": "agentdock-compat", "plugins": [{
            "name": "portable-demo", "source": {"source": "local", "path": "./plugins/portable-demo"},
            "policy": {"installation": "AVAILABLE", "authentication": "ON_INSTALL"}, "category": "Productivity",
        }]}), encoding="utf-8")
        home = root / "codex-home"
        home.mkdir()
        environment = dict(os.environ, CODEX_HOME=str(home))

        def invoke(*arguments: str) -> str:
            result = subprocess.run(codex_command(executable, *arguments), env=environment,
                                    capture_output=True, text=True, encoding="utf-8", timeout=60, check=False)
            if result.returncode:
                raise RuntimeError(f"Codex {' '.join(arguments[:3])}: {result.stderr.strip()}")
            return result.stdout

        version = invoke("--version").strip()
        invoke("plugin", "marketplace", "add", str(marketplace), "--json")
        install = json.loads(invoke("plugin", "add", "portable-demo@agentdock-compat", "--json"))
        listing = json.loads(invoke("plugin", "list", "--marketplace", "agentdock-compat", "--json"))
        if not any(item.get("enabled") for item in listing["installed"]):
            raise RuntimeError("Codex did not enable the installed fixture")
        cache = Path(install["installedPath"])
        for name in ("plugin.json", "mcp.json", "skills/portable-check/SKILL.md"):
            if (cache / name).read_bytes() != (fixture / name).read_bytes():
                raise RuntimeError(f"Codex changed portable package content: {name}")
        process = subprocess.Popen(codex_command(executable, "app-server"), env=environment,
                                   stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
                                   text=True, encoding="utf-8", bufsize=1)
        replies: queue.Queue[dict] = queue.Queue()

        def read() -> None:
            assert process.stdout is not None
            for line in process.stdout:
                try:
                    replies.put(json.loads(line))
                except json.JSONDecodeError:
                    pass

        reader = threading.Thread(target=read, daemon=True)
        reader.start()

        def send(value: dict) -> None:
            assert process.stdin is not None
            process.stdin.write(json.dumps(value) + "\n")
            process.stdin.flush()

        def request(identifier: int, method: str, params: dict) -> dict:
            send({"id": identifier, "method": method, "params": params})
            for _ in range(100):
                reply = replies.get(timeout=20)
                if reply.get("id") == identifier:
                    if "error" in reply:
                        raise RuntimeError(f"Codex {method}: {reply['error']}")
                    return reply["result"]
            raise RuntimeError("Codex returned too many unrelated notifications")

        try:
            request(1, "initialize", {"clientInfo": {"name": "plugin-compat-test", "version": "1.0.0"},
                                       "capabilities": {"experimentalApi": True}})
            send({"method": "initialized"})
            detail = request(2, "plugin/read", {"pluginName": "portable-demo", "marketplacePath": str(descriptor)})
            encoded = json.dumps(detail)
            if "portable-check" not in encoded or "portable-echo" not in encoded:
                raise RuntimeError("Codex plugin/read did not discover both the Skill and MCP server")
            return {"version": version, "installed": True, "enabled": True,
                    "package_unchanged": True, "skill_discovered": "portable-check", "mcp_discovered": "portable-echo"}
        finally:
            if process.stdin is not None:
                process.stdin.close()
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                if os.name == "nt":
                    subprocess.run(["taskkill", "/PID", str(process.pid), "/T", "/F"], capture_output=True, timeout=15)
                else:
                    process.terminate()
                process.wait(timeout=10)
            reader.join(timeout=2)
            if process.stdout is not None:
                process.stdout.close()


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--codex", action="store_true")
    parser.add_argument("--plugin", type=Path, default=REPOSITORY / "internal/plugin/testdata/portable-demo")
    args = parser.parse_args()
    fixture = args.plugin.resolve(strict=True)
    checked = []
    for name in ("plugin", "mcp"):
        path = fixture / f"{name}.json"
        if name == "mcp" and not path.exists():
            continue
        schema = json.loads((REPOSITORY / f"internal/plugin/schemas/{name}.schema.json").read_text(encoding="utf-8"))
        jsonschema.Draft202012Validator(schema).validate(json.loads(path.read_text(encoding="utf-8-sig")))
        checked.append(path.name)
    report = {"schema": "Agent Plugins 1.0.0", "validated": checked}
    if args.codex:
        report["codex"] = check_codex(fixture)
    print(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
