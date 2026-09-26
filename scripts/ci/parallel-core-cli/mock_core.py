#!/usr/bin/env python3
"""Small authenticated Core fixture used only by the WB01 hosted-runner smoke test."""
from __future__ import annotations

import argparse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import signal
import sys
from typing import Any
from urllib.parse import parse_qs, urlparse

TOKEN_ENV = "WB01_MOCK_TOKEN"


def envelope(handler: BaseHTTPRequestHandler, status: int, payload: dict[str, Any]) -> None:
    body = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    handler.send_response(status)
    handler.send_header("Content-Type", "application/json; charset=utf-8")
    handler.send_header("Content-Length", str(len(body)))
    handler.send_header("Cache-Control", "no-store")
    handler.end_headers()
    handler.wfile.write(body)


class State:
    def __init__(self, token: str) -> None:
        self.token = token
        self.requests: list[dict[str, Any]] = []


class Handler(BaseHTTPRequestHandler):
    server_version = "WB01MockCore/1"
    protocol_version = "HTTP/1.1"

    @property
    def state(self) -> State:
        return self.server.state  # type: ignore[attr-defined]

    def log_message(self, format: str, *args: object) -> None:
        # Keep hosted-runner logs deterministic and never print Authorization.
        print(f"mock-core: {self.command} {self.path} -> {format % args}", file=sys.stderr)

    def authenticate(self) -> bool:
        if self.headers.get("Authorization", "") == f"Bearer {self.state.token}":
            return True
        envelope(self, 401, {"ok": False, "code": "UNAUTHORIZED", "error": "token required"})
        return False

    def read_object(self) -> dict[str, Any] | None:
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            envelope(self, 400, {"ok": False, "code": "INVALID_ARGUMENT", "error": "invalid content length"})
            return None
        if length < 2 or length > 65536:
            envelope(self, 400, {"ok": False, "code": "INVALID_ARGUMENT", "error": "body size is invalid"})
            return None
        try:
            value = json.loads(self.rfile.read(length))
        except (UnicodeDecodeError, json.JSONDecodeError):
            envelope(self, 400, {"ok": False, "code": "INVALID_ARGUMENT", "error": "invalid JSON body"})
            return None
        if not isinstance(value, dict):
            envelope(self, 400, {"ok": False, "code": "INVALID_ARGUMENT", "error": "body must be an object"})
            return None
        return value

    def record(self, body: dict[str, Any] | None = None) -> None:
        parsed = urlparse(self.path)
        self.state.requests.append(
            {
                "method": self.command,
                "path": parsed.path,
                "query": parse_qs(parsed.query),
                "body": body,
            }
        )

    def do_GET(self) -> None:  # noqa: N802 - stdlib handler contract
        if not self.authenticate():
            return
        parsed = urlparse(self.path)
        self.record()
        path = parsed.path
        query = parse_qs(parsed.query)
        if path == "/internal/runtime/status":
            envelope(
                self,
                200,
                {
                    "ok": True,
                    "source": "agentdock-api",
                    "service": "AgentDock",
                    "version": "candidate",
                    "auth_enabled": True,
                },
            )
            return
        if path == "/internal/runtime/execution/tasks":
            if query.get("limit") != ["2"]:
                envelope(self, 400, {"ok": False, "code": "INVALID_ARGUMENT", "error": "expected limit=2"})
                return
            envelope(
                self,
                200,
                {
                    "tasks": [
                        {"task_id": "tsk_alpha", "title": "Alpha", "status": "active"},
                        {"task_id": "tsk_beta", "title": "Beta", "status": "blocked"},
                    ],
                    "total": 2,
                    "has_more": False,
                    "next_offset": 2,
                },
            )
            return
        if path == "/internal/runtime/tasks/tsk_created":
            envelope(
                self,
                200,
                {
                    "ok": True,
                    "task": {
                        "task_id": "tsk_created",
                        "title": "Candidate smoke",
                        "status": "active",
                        "steps": [{"id": "S1", "title": "Verify", "status": "pending"}],
                    },
                },
            )
            return
        if path == "/internal/runtime/tasks/missing":
            envelope(self, 404, {"ok": False, "code": "TASK_NOT_FOUND", "error": "task not found"})
            return
        if path == "/internal/runtime/workspaces":
            envelope(
                self,
                200,
                {
                    "ok": True,
                    "workspaces": [
                        {
                            "workspace_id": "wsp_candidate",
                            "name": "Candidate",
                            "rules_revision": 7,
                            "runtime": "unix",
                        }
                    ],
                },
            )
            return
        if path == "/internal/runtime/workspaces/wsp_candidate":
            envelope(
                self,
                200,
                {
                    "ok": True,
                    "workspace": {
                        "workspace_id": "wsp_candidate",
                        "name": "Candidate",
                        "rules_revision": 7,
                        "runtime": "unix",
                    },
                },
            )
            return
        if path == "/internal/runtime/plugins/demo":
            envelope(self, 200, {"plugin": {"name": "demo", "enabled": True}})
            return
        if path == "/__requests":
            envelope(self, 200, {"requests": self.state.requests})
            return
        envelope(self, 404, {"ok": False, "code": "NOT_FOUND", "error": "route not found"})

    def do_POST(self) -> None:  # noqa: N802 - stdlib handler contract
        if not self.authenticate():
            return
        body = self.read_object()
        if body is None:
            return
        parsed = urlparse(self.path)
        self.record(body)
        path = parsed.path
        if path == "/internal/runtime/tasks":
            if body.get("action") != "create":
                envelope(self, 400, {"ok": False, "code": "INVALID_ARGUMENT", "error": "expected create"})
                return
            required = {
                "title": "Candidate smoke",
                "goal": "Exercise the real CLI process",
                "workspace_id": "wsp_candidate",
            }
            if any(body.get(key) != value for key, value in required.items()):
                envelope(self, 400, {"ok": False, "code": "INVALID_ARGUMENT", "error": "task body mismatch"})
                return
            if body.get("completion_conditions") != ["Smoke passes"]:
                envelope(self, 400, {"ok": False, "code": "INVALID_ARGUMENT", "error": "conditions mismatch"})
                return
            envelope(self, 200, {"ok": True, "task_id": "tsk_created", "status": "active"})
            return
        if path == "/internal/runtime/workspaces":
            if body != {
                "action": "register",
                "workspace_id": "wsp_candidate",
                "expected_revision": 7,
                "name": "Candidate renamed",
            }:
                envelope(self, 409, {"ok": False, "code": "REVISION_CONFLICT", "error": "workspace body mismatch"})
                return
            envelope(
                self,
                200,
                {
                    "ok": True,
                    "workspace": {
                        "workspace_id": "wsp_candidate",
                        "name": "Candidate renamed",
                        "rules_revision": 8,
                    },
                },
            )
            return
        envelope(self, 404, {"ok": False, "code": "NOT_FOUND", "error": "route not found"})


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--port-file", type=Path, required=True)
    args = parser.parse_args()
    token = os.environ.get(TOKEN_ENV, "")
    if not token or len(token) < 16:
        raise RuntimeError(f"{TOKEN_ENV} must contain a non-secret test token of at least 16 characters")

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    server.daemon_threads = True
    server.state = State(token)  # type: ignore[attr-defined]
    endpoint = f"http://127.0.0.1:{server.server_address[1]}"
    args.port_file.parent.mkdir(parents=True, exist_ok=True)
    args.port_file.write_text(endpoint + "\n", encoding="utf-8")

    def stop(_signum: int, _frame: object) -> None:
        # signal handlers run on the main thread; shutdown must be called elsewhere.
        server.server_close()
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    print(endpoint, flush=True)
    try:
        server.serve_forever(poll_interval=0.1)
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
