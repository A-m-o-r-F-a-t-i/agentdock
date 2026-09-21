#!/usr/bin/env python3
"""Exercise released Core binaries against isolated, persistent execution data.

Requires only Python's standard library. Starts loopback-only child processes;
never installs a service or reads the production home or credentials.
"""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import time
import urllib.error
import urllib.request


def require(condition: bool, message: str) -> None:
    if not condition:
        raise AssertionError(message)


class Fixture:
    def __init__(self, root: Path) -> None:
        self.root = root
        self.home = root / "home"
        self.workspace = root / "workspace 中文"
        self.home.mkdir(parents=True)
        self.workspace.mkdir()
        (self.workspace / "sentinel.txt").write_text("preserve project source", encoding="utf-8")
        with socket.socket() as probe:
            probe.bind(("127.0.0.1", 0))
            self.port = probe.getsockname()[1]
        self.origin = f"http://127.0.0.1:{self.port}"
        self.credential = secrets.token_urlsafe(32)
        self.client = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        self.process: subprocess.Popen | None = None
        self.log = None
        self.generation = 0
        self.rpc_id = 0
        self.versions: list[dict] = []

    def request(self, path: str, body: dict | None = None) -> tuple[int, dict]:
        data = None if body is None else json.dumps(body, ensure_ascii=False).encode("utf-8")
        request = urllib.request.Request(self.origin + path, data=data, headers={
            "Authorization": "Bearer " + self.credential,
            "Content-Type": "application/json", "Accept": "application/json, text/event-stream",
            "MCP-Protocol-Version": "2025-03-26",
        })
        try:
            response = self.client.open(request, timeout=22)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            raw = response.read(4 * 1024 * 1024).decode("utf-8")
            if raw.lstrip().startswith("{"):
                value = json.loads(raw)
            else:
                values = [line[5:].strip() for line in raw.splitlines() if line.startswith("data:")]
                value = json.loads(values[-1]) if values else {"error": raw}
            return response.status, value

    def local(self, path: str, body: dict | None = None) -> dict:
        status, value = self.request("/internal/runtime/" + path, body)
        require(status == 200, f"Local {path}: HTTP {status}, {value}")
        return value

    def rpc(self, method: str, params: dict) -> dict:
        self.rpc_id += 1
        status, value = self.request("/mcp", {"jsonrpc": "2.0", "id": self.rpc_id,
                                                "method": method, "params": params})
        require(status == 200 and "error" not in value, f"MCP {method}: {status} {value}")
        return value["result"]

    def tool(self, host: str | None, name: str, args: dict, error: str = "") -> dict:
        params = {"name": name, "arguments": args}
        if host is not None:
            params["_meta"] = {"openai/session": host}
        result = self.rpc("tools/call", params)
        value = result.get("structuredContent")
        if value is None:
            value = json.loads(result["content"][0]["text"])
        if error:
            require(result.get("isError") is True and value.get("code") == error,
                    f"Expected {error}, got {value}")
        else:
            require(not result.get("isError"), f"Tool {name}: {value}")
        return value

    def start(self, binary: Path, expected: str) -> None:
        require(self.process is None, "Previous fixture process is still running")
        env = {k: v for k, v in os.environ.items() if not k.upper().startswith("AGENTDOCK_")}
        env.update(AGENTDOCK_HOME=str(self.home), AGENTDOCK_DEFAULT_DIR=str(self.workspace),
                   AGENTDOCK_AUTH_TOKEN=self.credential, AGENTDOCK_AGENTS_AUTO_LOAD="false")
        self.generation += 1
        self.log = (self.root / f"core-{self.generation}.log").open("wb")
        self.process = subprocess.Popen([str(binary), "-host", "127.0.0.1", "-port", str(self.port)],
            cwd=self.workspace, env=env, stdout=self.log, stderr=subprocess.STDOUT,
            creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0))
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            require(self.process.poll() is None, "Core exited before readiness; inspect isolated log")
            try:
                status, health = self.request("/healthz")
                if status == 200:
                    require(health.get("version") == expected, f"Wrong binary version: {health}")
                    self.versions.append({"version": expected, "generation": self.generation})
                    self.rpc("initialize", {"protocolVersion": "2025-03-26", "capabilities": {},
                        "clientInfo": {"name": "execution-upgrade-fixture", "version": "1"}})
                    return
            except (urllib.error.URLError, TimeoutError):
                time.sleep(0.1)
        raise TimeoutError("Core readiness timed out")

    def stop(self) -> None:
        if self.process is not None:
            if self.process.poll() is None:
                self.process.terminate()
            self.process.wait(timeout=15)
            self.process = None
        if self.log is not None:
            self.log.close()
            self.log = None

    def terminal(self, call_id: str) -> dict:
        deadline = time.monotonic() + 12
        while time.monotonic() < deadline:
            value = self.local("calls/" + call_id)
            if value["status"] in ("succeeded", "failed", "cancelled", "unknown", "partial"):
                return value
            time.sleep(0.1)
        raise TimeoutError("Call did not become terminal")


def execute(old: Path, new: Path, root: Path, old_version: str, new_version: str) -> dict:
    fixture = Fixture(root)
    report: dict = {"passed": False, "cases": [], "production_touched": False}
    def passed(name: str) -> None:
        report["cases"].append({"case": name, "passed": True})
        print("PASS " + name, flush=True)
    try:
        fixture.start(old, old_version)
        task = fixture.tool("upgrade-a", "task_manage", {"action": "create", "title": "升级前真实任务",
            "goal": "Verify persistence", "completion_conditions": ["preserved"],
            "steps": [{"id": "verify", "title": "Verify persistent state"}]})
        cid, tid = task["conversation_id"], task["task_id"]
        read = fixture.tool("upgrade-a", "read_file", {"path": "sentinel.txt"})
        pending = fixture.tool("upgrade-a", "file_edit", {"action": "add", "path": "never-written.txt", "content": "not approved"})
        require(pending.get("status") == "pending_approval", "Fixture write was dispatched without approval")
        other = fixture.tool("upgrade-b", "list_dir", {"path": "."})["conversation_id"]
        fixture.local("conversations/batch", {"ids": [other], "action": "rename", "title": "用户保留名称"})
        passed("released_old_binary_created_real_task_calls_manual_title_and_pending_request")
        fixture.stop()
        fixture.start(new, new_version)
        require(fixture.local("calls/" + read["call_id"])["status"] == "succeeded", "Old result was rewritten")
        require(fixture.local("tasks/" + tid)["task"]["title"] == "升级前真实任务", "Old task identity or title changed")
        require(fixture.local("conversations/" + other)["conversation"]["title"] == "用户保留名称", "Manual title changed")
        require(fixture.terminal(pending["call_id"])["status"] != "succeeded", "Old approval was automatically replayed")
        require(not (fixture.workspace / "never-written.txt").exists(), "Unapproved file exists")
        later = fixture.tool("upgrade-a", "list_dir", {"path": "."})
        require(later["conversation_id"] == cid and later["task_id"] == tid, "Upgrade split continuation identity")
        passed("upgrade_preserved_ids_history_manual_title_and_did_not_replay_old_approval")
        orphan = fixture.tool(None, "list_dir", {"path": "."})
        require(not orphan.get("conversation_id"), "Unattributed call received a fabricated identity")
        orphan_id = orphan["call_id"]
        for action in ("isolate", "unisolate", "archive", "unarchive", "trash", "restore", "trash", "delete"):
            result = fixture.local("calls/batch", {"ids": [orphan_id], "action": action, "confirm_permanent": action == "delete"})
            require(result["succeeded"] == 1, f"Call management failed: {action} {result}")
        passed("unattributed_call_stable_handle_supports_management_and_deletion")
        policy = fixture.local("permissions/effective")["policy"]
        fixture.local("permissions", {"scope": "global", "mode": "full", "confirm_full": True,
                                       "expected_revision": policy["revision"]})
        running = fixture.tool("upgrade-a", "exec_command", {"cmd": "Write-Output 'isolated-running'; Start-Sleep -Seconds 40",
                                                              "execution_mode": "async"})
        stop = fixture.local("conversations/" + cid + "/terminate", {"confirm": True})
        require(stop["terminated"] is True, "Termination gate was not saved")
        require(fixture.terminal(running["call_id"])["status"] in ("cancelled", "failed"), "Running process did not stop")
        blocked = fixture.tool("upgrade-a", "list_dir", {"path": "."}, "CONVERSATION_TERMINATED")
        require(blocked["error"] == "用户已终止此对话，请停止执行。", "Termination text changed")
        require(fixture.tool("upgrade-b", "list_dir", {"path": "."})["conversation_id"] == other, "Other conversation affected")
        require("calls" in fixture.local("calls/export?conversation_id=" + cid), "Terminated history could not be exported")
        passed("termination_stopped_actual_async_command_blocked_new_calls_and_preserved_other_conversation")
        fixture.stop()
        fixture.start(new, new_version)
        fixture.tool("upgrade-a", "list_dir", {"path": "."}, "CONVERSATION_TERMINATED")
        require(fixture.request("/internal/runtime/calls/" + orphan_id)[0] == 404, "Deleted call reappeared after restart")
        fixture.local("conversations/" + cid + "/resume", {"confirm": True})
        require(fixture.tool("upgrade-a", "list_dir", {"path": "."})["task_id"] == tid, "Local resume lost task identity")
        require(not (fixture.workspace / "never-written.txt").exists(), "Resume replayed old write")
        for action in ("trash", "delete"):
            fixture.local("conversations/batch", {"ids": [other], "action": action, "confirm_permanent": action == "delete"})
        fixture.stop()
        fixture.start(new, new_version)
        require(fixture.request("/internal/runtime/conversations/" + other)[0] == 410, "Deleted source did not return 410")
        fixture.tool("upgrade-b", "list_dir", {"path": "."}, "CONVERSATION_TERMINATED")
        require((fixture.workspace / "sentinel.txt").read_text(encoding="utf-8") == "preserve project source", "Project source changed")
        passed("restart_preserved_termination_call_tombstone_conversation_tombstone_and_source_files")
        report["passed"] = True
    except Exception as error:
        report["error"] = str(error).replace(fixture.credential, "[REDACTED]")
        raise
    finally:
        fixture.stop()
        report["binary_generations"] = fixture.versions
        (root / "result.json").write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
    return report


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", type=Path, required=True)
    parser.add_argument("--target", type=Path, required=True)
    parser.add_argument("--root", type=Path, required=True)
    parser.add_argument("--baseline-version", default="1.1.2")
    parser.add_argument("--target-version", default="1.1.3")
    args = parser.parse_args()
    require(not args.root.exists(), "Provide a fresh isolated test directory")
    for path in (args.baseline, args.target):
        require(path.is_file(), f"Core binary not found: {path}")
    print(json.dumps(execute(args.baseline.resolve(), args.target.resolve(), args.root.resolve(),
                              args.baseline_version, args.target_version), ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
