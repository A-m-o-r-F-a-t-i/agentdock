#!/usr/bin/env python3
"""External Termux deployment manager, not a Core business-state implementation.

Mutations are serialized. Journals, consistent data snapshots and recovery limits
survive a missing Android callback. The public entry never bypasses verification.
"""
from __future__ import annotations
import contextlib
import fcntl
import hashlib
import json
import os
import platform
import re
import secrets
import shutil
import signal
import socket
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.parse
import urllib.request
from pathlib import Path, PurePosixPath
from typing import Any, Callable

MAX_JSON = 65536
MAX_ARCHIVE = 256 * 1024 * 1024
MAX_EXPANDED = 512 * 1024 * 1024
MAX_BACKUP = 1024 * 1024 * 1024
MAX_ENTRIES = 25000
MAX_JOURNALS = 128
VERSION = re.compile(r"[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9.-]+)?\Z")
IDENTIFIER = re.compile(r"[A-Za-z0-9_-]{1,96}\Z")
NONCE = re.compile(r"[A-Za-z0-9_-]{20,128}\Z")
READ_ONLY = {"probe", "status", "operation_query", "logs", "diagnostic_preview", "cleanup_preview"}
OPERATIONS = READ_ONLY | {"bootstrap", "install", "update", "resume", "cancel_operation", "adopt", "configure", "start", "stop", "restart", "repair", "guardian_check", "rollback", "export_diagnostics", "path_probe", "project_create", "cleanup"}
TERMINAL = {"succeeded", "failed", "rolled_back", "cancelled"}
SECRET_KEYS = {"token", "bearer", "password", "secret", "credential", "credentials", "authorization", "cookie"}
DEFAULT_CONFIG = {"schema_version": 1, "port": 8765, "distro": "debian", "node_name": "AgentDock Workbench", "recovery_base_seconds": 30, "recovery_max_seconds": 900, "recovery_max_failures": 3}

class BridgeError(Exception):
    def __init__(self, code: str, message: str, retryable: bool = False):
        super().__init__(message)
        self.code, self.retryable = code, retryable

def check(condition: bool, code: str, message: str) -> None:
    if not condition:
        raise BridgeError(code, message)

def redact(value: str, maximum: int = 2048) -> str:
    value = value[:max(maximum * 2, 8192)]
    value = re.sub(r"(?i)bearer\s+[^\s,;\"]+", "Bearer <redacted>", value)
    value = re.sub(r"(?i)((?:[\w-]*(?:token|secret|password)|authorization|cookie)[\"'\s:=]+)(?:\"[^\"]*\"|'[^']*'|[^\s,;]+)", r"\1<redacted>", value)
    return re.sub(r"(?<![A-Za-z0-9])[a-fA-F0-9]{64}(?![A-Za-z0-9])", "<redacted>", value)[:maximum]

def no_secrets(value: Any, depth: int = 0) -> None:
    check(depth < 20, "invalid_request", "请求嵌套过深")
    if isinstance(value, dict):
        for key, child in value.items():
            normalized = str(key).lower()
            check(not any(normalized == name or normalized.endswith("_" + name) for name in SECRET_KEYS), "invalid_request", "凭据不能经部署请求或回执传送")
            no_secrets(child, depth + 1)
    elif isinstance(value, list):
        check(len(value) <= 1000, "invalid_request", "请求数组过长")
        for child in value:
            no_secrets(child, depth + 1)

def json_bytes(value: Any) -> bytes:
    data = (json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False) + "\n").encode()
    check(len(data) <= MAX_JSON, "size_limit", "结构化记录超过 64 KiB")
    return data

def fsync_directory(path: Path) -> None:
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)

def atomic_bytes(path: Path, data: bytes) -> None:
    check(not path.is_symlink(), "unsafe_path", "私有状态文件不能是符号链接")
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix=".wb-write-", dir=path.parent)
    try:
        with os.fdopen(descriptor, "wb") as output:
            output.write(data)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
        fsync_directory(path.parent)
    finally:
        with contextlib.suppress(FileNotFoundError):
            os.unlink(temporary)

def atomic_json(path: Path, value: Any) -> None:
    atomic_bytes(path, json_bytes(value))

def read_json(path: Path, default: Any = None) -> Any:
    if not path.exists() and not path.is_symlink():
        return default
    check(not path.is_symlink() and path.is_file() and path.stat().st_size <= MAX_JSON, "unsafe_state", "状态文件类型或大小无效；保留原文件")
    try:
        return json.loads(path.read_bytes())
    except (ValueError, UnicodeError) as error:
        raise BridgeError("unsafe_state", "状态文件损坏；保留原文件") from error

def read_text(path: Path, default: str = "", maximum: int = 4096) -> str:
    if not path.exists() and not path.is_symlink():
        return default
    check(not path.is_symlink() and path.is_file() and path.stat().st_size <= maximum, "unsafe_state", "私有记录类型或大小无效")
    return path.read_text().strip()

def inside(path: Path, root: Path, *, exists: bool = False) -> Path:
    try:
        result = path.expanduser().resolve(strict=exists)
    except (OSError, RuntimeError) as error:
        raise BridgeError("unsafe_path", "路径无法安全解析") from error
    check(result != root and root in result.parents, "unsafe_path", "路径超出已授权根目录")
    return result

def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(65536), b""):
            digest.update(chunk)
    return digest.hexdigest()

def safe_extract(archive: Path, destination: Path) -> None:
    """Validate all members before writing. Links and special files are refused."""
    check(not destination.is_symlink() and destination.is_dir() and not any(destination.iterdir()), "unsafe_archive", "解包目录必须为空的私有目录")
    check(archive.stat().st_size <= MAX_ARCHIVE, "size_limit", "压缩包超过大小上限")
    with tarfile.open(archive, "r:gz") as source:
        members, seen, total = [], set(), 0
        for member in source:
            path = PurePosixPath(member.name)
            if member.name in (".", "./") and member.isdir():
                continue
            valid = (len(members) < 10000 and bool(member.name) and not path.is_absolute()
                     and ".." not in path.parts and "\\" not in member.name and ":" not in member.name
                     and all(ord(char) >= 32 for char in member.name) and bool(path.parts)
                     and len(member.name) <= 1024 and not path.parts[0].startswith(".workbench-")
                     and (member.isfile() or member.isdir()) and 0 <= member.size <= 128 * 1024 * 1024
                     and not member.issparse() and path.as_posix() not in seen)
            check(valid, "unsafe_archive", "归档包含越界、链接、重复或不支持的成员")
            total += member.size
            check(total <= MAX_EXPANDED, "size_limit", "解包总量超过上限")
            seen.add(path.as_posix())
            members.append((member, path))
        files = {path.as_posix() for member, path in members if member.isfile()}
        check(all(not any(parent.as_posix() in files for parent in path.parents) for _, path in members), "unsafe_archive", "归档文件和目录冲突")
        check(shutil.disk_usage(destination).free >= total + 64 * 1024 * 1024, "disk_full", "暂存空间不足")
        for member, path in members:
            target = destination.joinpath(*path.parts)
            if member.isdir():
                target.mkdir(mode=0o700, parents=True, exist_ok=True)
            else:
                target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
                with source.extractfile(member) as stream:
                    descriptor = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
                    with os.fdopen(descriptor, "wb") as output:
                        shutil.copyfileobj(stream, output, 65536)
                        output.flush()
                        os.fsync(output.fileno())
                target.chmod(0o755 if member.mode & 0o111 else 0o644)
        fsync_directory(destination)

def tree_size(root: Path) -> int:
    """Bounded non-following inventory; backup links must remain inside data."""
    total = count = 0
    if not root.exists():
        return 0
    for directory, directories, files in os.walk(root, followlinks=False):
        for name in directories + files:
            path = Path(directory) / name
            count += 1
            check(count <= MAX_ENTRIES, "size_limit", "备份文件数量超过上限")
            if path.is_symlink():
                check(not os.path.isabs(os.readlink(path)), "unsafe_path", "备份拒绝绝对符号链接")
                inside(path, root)
            elif path.is_file():
                total += path.stat().st_size
                check(total <= MAX_BACKUP, "size_limit", "数据备份超过 1 GiB；需要先导出或清理")
            else:
                check(path.is_dir(), "unsafe_path", "备份包含特殊文件")
    return total

def tree_digest(root: Path) -> str:
    tree_size(root)
    digest = hashlib.sha256()
    for directory, directories, files in os.walk(root, followlinks=False):
        directories.sort()
        for name in sorted(directories + files):
            path = Path(directory) / name
            relative = path.relative_to(root).as_posix()
            if path.is_symlink():
                item = [relative, "link", os.readlink(path)]
            elif path.is_file():
                item = [relative, "file", path.stat().st_mode & 0o777, path.stat().st_size, sha256(path)]
            else:
                item = [relative, "directory"]
            digest.update(json.dumps(item, ensure_ascii=False, separators=(",", ":")).encode())
            digest.update(b"\n")
    return digest.hexdigest()

def verify_snapshot(path: Path) -> None:
    record = read_json(path / ".snapshot-complete.json", {})
    check(record.get("schema_version") == 1 and record.get("digest") == tree_digest(path / "data"),
          "backup_corrupt", "一致性备份校验失败；未回写或自动降级")

def snapshot_tree(source: Path, target: Path) -> None:
    marker = target / ".snapshot-complete.json"
    if marker.is_file():
        verify_snapshot(target)
        return
    check(not target.is_symlink(), "unsafe_path", "备份目录无效")
    size = tree_size(source)
    check(shutil.disk_usage(source.parent).free >= size + 64 * 1024 * 1024, "disk_full", "一致性备份空间不足")
    temporary = target.with_name(target.name + ".partial")
    if temporary.exists():
        check(not temporary.is_symlink(), "unsafe_path", "备份暂存目录无效")
        shutil.rmtree(temporary)
    temporary.mkdir(mode=0o700, parents=True)
    if source.exists():
        shutil.copytree(source, temporary / "data", symlinks=True, copy_function=shutil.copy2)
    else:
        (temporary / "data").mkdir(mode=0o700)
    for directory, _, files in os.walk(temporary / "data", followlinks=False):
        for name in files:
            path = Path(directory) / name
            if not path.is_symlink():
                with path.open("rb") as stream:
                    os.fsync(stream.fileno())
        fsync_directory(Path(directory))
    atomic_json(temporary / ".snapshot-complete.json", {"schema_version": 1, "bytes": size, "digest": tree_digest(temporary / "data")})
    check(not target.exists(), "unsafe_state", "未完成备份目标已存在，需要人工核对")
    os.replace(temporary, target)
    fsync_directory(target.parent)

def https_url(value: str) -> str:
    check(isinstance(value, str) and 0 < len(value) <= 4096, "invalid_url", "下载地址无效")
    parsed = urllib.parse.urlsplit(value)
    check(parsed.scheme == "https" and bool(parsed.hostname) and not parsed.username and not parsed.password
          and not parsed.query and not parsed.fragment, "invalid_url", "清单和源资产须为无凭据、无查询参数的 HTTPS 地址")
    return value

class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise BridgeError("redirect_rejected", "节点响应重定向被拒绝")

class NodeBackend:
    """Actual Termux/PRoot I/O. CI uses an injected, temporary-filesystem backend."""
    def __init__(self, root: Path, config: dict[str, Any]):
        self.root, self.config = root, config

    def environment(self) -> dict[str, Any]:
        missing = [name for name in ("proot-distro", "curl", "openssl", "python3") if not shutil.which(name)]
        architecture = platform.machine().lower()
        return {"architecture": architecture, "arm64": architecture in ("aarch64", "arm64"), "missing": missing,
                "distro": self.config["distro"], "port": self.config["port"]}

    def validate_environment(self) -> None:
        value = self.environment()
        check(not value["missing"], "requires_user_action", "缺少 Termux 依赖，请执行导出的 bootstrap")
        check(value["arm64"], "unsupported_architecture", "Core 部署要求 ARM64 Termux；界面模拟器不代表该运行架构")
        result = subprocess.run(["proot-distro", "login", self.config["distro"], "--", "/bin/sh", "-c", "command -v curl >/dev/null && command -v tar >/dev/null && command -v jq >/dev/null"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=20, close_fds=True)
        check(result.returncode == 0, "requires_user_action", "Linux 环境未就绪，请继续 bootstrap")

    @staticmethod
    def process_stamp(pid: int) -> str:
        check(1 < pid <= 2147483647, "unknown_process", "进程标识无效")
        fields = Path(f"/proc/{pid}/stat").read_text().rsplit(") ", 1)[1].split()
        boot = Path("/proc/sys/kernel/random/boot_id").read_text().strip()
        return f"{pid}:{fields[2]}:{fields[3]}:{fields[19]}:{boot}"

    def owned_pid(self) -> int | None:
        pid_text = read_text(self.root / "core.pid")
        identity = read_text(self.root / "core.identity")
        if not pid_text:
            return None
        check(pid_text.isdecimal() and 1 < int(pid_text) <= 2147483647, "unknown_process", "已有 PID 无效；未发送进程信号")
        pid = int(pid_text)
        if not Path(f"/proc/{pid}").exists():
            return None
        try:
            actual = self.process_stamp(pid)
        except (OSError, IndexError) as error:
            raise BridgeError("unknown_process", "无法读取已有进程身份；未发送信号") from error
        check(actual == identity and actual.startswith(f"{pid}:{pid}:{pid}:"), "unknown_process", "已有进程身份与受管记录不同；未接管或发送信号")
        return pid

    def bearer(self) -> str:
        value = read_text(self.root / "auth-token", maximum=128)
        check(re.fullmatch(r"[a-fA-F0-9]{64}", value) is not None, "requires_user_action", "节点身份材料无效；未生成替代凭据")
        return value

    def request(self, path: str) -> dict[str, Any]:
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
        request = urllib.request.Request(f"http://127.0.0.1:{self.config['port']}{path}", headers={"Authorization": "Bearer " + self.bearer(), "Accept": "application/json"})
        with opener.open(request, timeout=3) as response:
            data = response.read(MAX_JSON + 1)
            check(response.status == 200 and len(data) <= MAX_JSON, "health_failed", "节点管理响应无效")
            value = json.loads(data)
            check(isinstance(value, dict), "health_failed", "节点未返回结构化响应")
            return value

    def health(self, expected_version: str = "") -> bool:
        try:
            if self.owned_pid() is None:
                return False
            result = self.request("/internal/runtime/status")
            permission = self.request("/internal/runtime/permissions/effective")
            effective = permission.get("effective")
            revision = permission.get("revision") if not isinstance(effective, dict) else effective.get("revision", permission.get("revision"))
            return (result.get("ok") is True and result.get("source") == "agentdock-api"
                    and result.get("agentdock_home") == "/opt/agentdock-workbench/data"
                    and (not expected_version or result.get("version") == expected_version)
                    and type(revision) is int and revision > 0)
        except (BridgeError, OSError, ValueError, TypeError):
            return False

    def binary_version(self, directory: Path) -> str:
        relative = directory.relative_to(self.root).as_posix()
        result = subprocess.run(["proot-distro", "login", self.config["distro"], "--bind", f"{self.root}:/opt/agentdock-workbench", "--", f"/opt/agentdock-workbench/{relative}/bin/agentdock", "version", "--json"], capture_output=True, timeout=20, close_fds=True)
        check(result.returncode == 0 and len(result.stdout) <= 65536, "verification_failed", "Core 版本探针失败")
        try:
            return json.loads(result.stdout)["version"]
        except (KeyError, ValueError, TypeError) as error:
            raise BridgeError("verification_failed", "Core 版本探针格式无效") from error

    def stop(self) -> None:
        pid = self.owned_pid()
        if pid is None:
            for name in ("core.pid", "core.identity"):
                (self.root / name).unlink(missing_ok=True)
            return
        os.killpg(pid, signal.SIGTERM)
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            try:
                os.killpg(pid, 0)
            except ProcessLookupError:
                break
            time.sleep(0.1)
        else:
            check(self.owned_pid() == pid, "unknown_process", "停止期间进程身份改变；未升级信号")
            os.killpg(pid, signal.SIGKILL)
            time.sleep(0.1)
            try:
                os.killpg(pid, 0)
            except ProcessLookupError:
                pass
            else:
                raise BridgeError("stop_incomplete", "进程组仍存在，停止结果待核对")
        for name in ("core.pid", "core.identity"):
            (self.root / name).unlink(missing_ok=True)

    def start(self, expected_version: str) -> None:
        self.bearer()
        if self.health(expected_version):
            return
        self.stop()
        check(read_text(self.root / "desired-state", "stopped") == "running", "user_stopped", "停止意图已生效，未启动 Core")
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
            try:
                probe.bind(("127.0.0.1", self.config["port"]))
            except OSError as error:
                raise BridgeError("port_in_use", "端口已被占用；未终止其他服务") from error
        log = self.root / "logs/core.log"
        check(not log.is_symlink(), "unsafe_path", "受管日志链接无效")
        log.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        if log.exists() and log.stat().st_size > 8 * 1024 * 1024:
            os.replace(log, log.with_suffix(".previous.log"))
        with log.open("ab") as output:
            # The child records its PID/session before exec. Killing this caller
            # cannot turn the successful launch into an unrecorded managed process.
            child = subprocess.Popen([sys.executable, str(Path(__file__).resolve()), "--launch", str(self.root)], stdin=subprocess.DEVNULL, stdout=output, stderr=subprocess.STDOUT, start_new_session=True, close_fds=True)
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline and child.poll() is None:
            if self.health(expected_version):
                marker = self.root / "workspace" / (".wb-health-" + secrets.token_hex(12))
                descriptor = os.open(marker, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
                try:
                    with os.fdopen(descriptor, "wb") as output:
                        output.write(b"workspace-probe")
                    check(marker.read_bytes() == b"workspace-probe", "health_failed", "工作目录读写探针失败")
                finally:
                    marker.unlink(missing_ok=True)
                return
            if read_text(self.root / "desired-state", "stopped") != "running":
                break
            time.sleep(0.25)
        child.poll()
        self.stop()
        raise BridgeError("health_failed", "Core 身份、版本、管理鉴权或健康检查失败")

    def fetch(self, url: str, target: Path, maximum: int, resume: bool = False) -> None:
        https_url(url)
        check(not target.is_symlink(), "unsafe_path", "下载暂存文件无效")
        args = ["curl", "--noproxy", "*", "-fsSL", "--retry", "2", "--max-time", "180", "--max-filesize", str(maximum), "--proto", "=https", "--proto-redir", "=https", "--tlsv1.2"]
        if resume and target.exists() and target.stat().st_size <= maximum:
            args += ["--continue-at", "-"]
        result = subprocess.run(args + [url, "-o", str(target)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=200, close_fds=True)
        if result.returncode in (22, 33, 36) and resume:
            target.unlink(missing_ok=True)
            return self.fetch(url, target, maximum, False)
        check(result.returncode == 0 and target.is_file() and target.stat().st_size <= maximum, "download_failed", "下载未完成，可按原操作继续；未使用残包")

    def verify_signature(self, key: Path, manifest: Path, signature: Path) -> None:
        result = subprocess.run(["openssl", "pkeyutl", "-verify", "-pubin", "-inkey", str(key), "-rawin", "-in", str(manifest), "-sigfile", str(signature)], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=10, close_fds=True)
        check(result.returncode == 0, "trust_failed", "发布清单签名验证失败")

def launch_owned(root: Path) -> None:
    root = root.resolve(strict=True)
    config = Deployment.validate_config(read_json(root / "node.json", DEFAULT_CONFIG))
    check(read_text(root / "desired-state", "stopped") == "running", "user_stopped", "停止意图已生效")
    backend = NodeBackend(root, config)
    backend.bearer()
    pid = os.getpid()
    stamp = backend.process_stamp(pid)
    check(stamp.startswith(f"{pid}:{pid}:{pid}:"), "unknown_process", "无法建立独立受管进程组")
    atomic_bytes(root / "core.pid", str(pid).encode())
    atomic_bytes(root / "core.identity", stamp.encode())
    command = 'export AGENTDOCK_HOME=/opt/agentdock-workbench/data; export AGENTDOCK_DEFAULT_DIR=/opt/agentdock-workbench/workspace; export AGENTDOCK_HOST=127.0.0.1; export AGENTDOCK_PORT="$(cat /opt/agentdock-workbench/port)"; export AGENTDOCK_AUTH_TOKEN="$(cat /opt/agentdock-workbench/auth-token)"; export AGENTDOCK_MCP_APPS_ENABLED=false; exec /opt/agentdock-workbench/current/bin/agentdock'
    os.execvp("proot-distro", ["proot-distro", "login", config["distro"], "--bind", f"{root}:/opt/agentdock-workbench", "--", "/bin/sh", "-lc", command])

class Deployment:
    def __init__(self, home: Path, backend_factory: Callable = NodeBackend, clock: Callable[[], float] = time.time, fault: Callable[[str], None] = lambda _: None):
        self.home = home.resolve(strict=True)
        self.state = self.home / ".agentdock-workbench"
        check(not self.state.is_symlink(), "unsafe_path", "产品私有目录不能是符号链接")
        adopted = read_text(self.state / "adopted-root")
        self.root = inside(Path(adopted) if adopted else self.state / "node", self.home)
        self.config = self.validate_config(read_json(self.root / "node.json", DEFAULT_CONFIG))
        self.backend_factory, self.clock, self.fault = backend_factory, clock, fault
        self.backend = backend_factory(self.root, self.config)
        for name in ("versions", "data", "workspace", "logs", "transactions", "backups", "quarantine"):
            check(not (self.root / name).is_symlink(), "unsafe_path", "受管目录链接需要先明确迁移")

    @staticmethod
    def validate_config(value: dict) -> dict:
        check(isinstance(value, dict) and not set(value) - set(DEFAULT_CONFIG), "invalid_request", "节点配置字段无效")
        result = dict(DEFAULT_CONFIG, **value)
        check(result["schema_version"] == 1, "invalid_request", "节点配置版本无效")
        check(type(result["port"]) is int and 1024 <= result["port"] <= 65535, "invalid_request", "端口范围为 1024–65535")
        check(isinstance(result["distro"], str) and re.fullmatch(r"[a-z][a-z0-9_-]{0,31}", result["distro"]) is not None, "invalid_request", "PRoot 发行版标识无效")
        check(isinstance(result["node_name"], str) and 0 < len(result["node_name"]) <= 80, "invalid_request", "节点名称无效")
        check(type(result["recovery_base_seconds"]) is int and 15 <= result["recovery_base_seconds"] <= 300, "invalid_request", "恢复初始退避须为 15–300 秒")
        check(type(result["recovery_max_seconds"]) is int and result["recovery_base_seconds"] <= result["recovery_max_seconds"] <= 3600, "invalid_request", "恢复退避上限无效")
        check(type(result["recovery_max_failures"]) is int and 1 <= result["recovery_max_failures"] <= 10, "invalid_request", "连续失败上限须为 1–10")
        return result

    def initialize(self) -> None:
        for name in ("versions", "data", "workspace", "logs", "transactions", "backups", "quarantine"):
            (self.root / name).mkdir(mode=0o700, parents=True, exist_ok=True)
        if not (self.root / "node.json").exists():
            atomic_json(self.root / "node.json", self.config)
        if not (self.root / "port").exists():
            atomic_bytes(self.root / "port", str(self.config["port"]).encode())

    @contextlib.contextmanager
    def lock(self):
        directory = self.state / "locks"
        check(not directory.is_symlink(), "unsafe_path", "管理锁目录无效")
        directory.mkdir(mode=0o700, parents=True, exist_ok=True)
        descriptor = os.open(directory / "bridge.lock", os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
        try:
            deadline = time.monotonic() + 10
            while True:
                try:
                    fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    break
                except BlockingIOError:
                    check(time.monotonic() < deadline, "busy", "另一个节点操作仍在执行；未重复提交")
                    time.sleep(0.05)
            yield
        finally:
            os.close(descriptor)

    def pointer(self, name: str) -> str:
        path = self.root / name
        if not path.exists() and not path.is_symlink():
            return ""
        check(path.is_symlink(), "unsafe_state", "版本指针不是受管符号链接")
        target = inside(path, self.root / "versions", exists=True)
        check(target.parent == self.root / "versions", "unsafe_path", "版本指针不在版本根目录")
        return target.name

    def set_pointer(self, name: str, version: str) -> None:
        current = self.root / name
        check(not current.exists() or current.is_symlink(), "unsafe_path", "拒绝覆盖非版本指针")
        if not version:
            current.unlink(missing_ok=True)
            fsync_directory(self.root)
            return
        check(Path(version).name == version and not version.startswith("."), "unsafe_path", "版本目录名无效")
        target = self.root / "versions" / version
        check(not target.is_symlink() and target.is_dir(), "unsafe_path", "受管版本目录无效")
        inside(target, self.root / "versions", exists=True)
        temporary = self.root / (".pointer-" + secrets.token_hex(12))
        temporary.symlink_to("versions/" + version)
        try:
            os.replace(temporary, current)
            fsync_directory(self.root)
        finally:
            temporary.unlink(missing_ok=True)

    def metadata(self, name: str) -> dict:
        if not name:
            return {}
        check(Path(name).name == name and not name.startswith("."), "unsafe_path", "版本目录标识无效")
        path = inside(self.root / "versions" / name, self.root / "versions", exists=True)
        value = read_json(path / ".workbench-release.json", {})
        check(isinstance(value, dict), "unsafe_state", "版本描述格式无效")
        return value

    def desired(self) -> str:
        state = read_text(self.root / "desired-state", "stopped")
        check(state in ("running", "stopped"), "unsafe_state", "节点运行意图无效")
        return state

    def set_desired(self, value: str) -> None:
        atomic_bytes(self.root / "desired-state", value.encode())

    def current_version(self) -> str:
        return self.metadata(self.pointer("current")).get("version", "")

    def status(self) -> dict:
        version = self.current_version()
        owned, error = False, ""
        try:
            owned = self.backend.owned_pid() is not None
        except BridgeError as failure:
            error = failure.code
        healthy = owned and self.backend.health(version)
        active = read_text(self.root / "active-operation")
        return {"status": "running" if healthy else ("requires_user_action" if error else "degraded" if owned else "stopped"),
                "message": "节点身份和管理鉴权已验证" if healthy else "节点状态待核对" if error else "没有健康的受管 Core",
                "data": {"version": version, "desired_state": self.desired(), "healthy": healthy, "process_identity": "verified" if owned else "unknown" if error else "absent", "active_operation_id": active, "node": self.config,
                         "environment": self.backend.environment(), "recovery": read_json(self.root / "recovery.json", {}),
                         "manifest_trust_ready": (self.state / "trust/release-ed25519-public.pem").is_file(), "observed_at": self.clock()}}

    def journal_path(self, identity: str) -> Path:
        check(isinstance(identity, str) and IDENTIFIER.fullmatch(identity) is not None, "invalid_request", "操作标识无效")
        parent = self.root / "transactions" / identity
        check(not parent.is_symlink(), "unsafe_path", "操作目录不能是符号链接")
        return parent / "journal.json"

    def journal_records(self) -> list[tuple[Path, dict]]:
        paths = list((self.root / "transactions").glob("*/journal.json"))
        check(len(paths) <= 256, "capacity", "操作记录数量异常；停止自动清理")
        records = []
        for path in paths:
            check(not path.parent.is_symlink(), "unsafe_path", "操作记录目录无效")
            record = read_json(path)
            check(isinstance(record, dict), "unsafe_state", "操作记录格式无效")
            records.append((path, record))
        return records

    def reserve_journal(self) -> None:
        records = self.journal_records()
        if len(records) < MAX_JOURNALS:
            return
        terminal = [(path, value) for path, value in records if value.get("status") in TERMINAL]
        for path, _ in sorted(terminal, key=lambda pair: pair[1].get("updated_at", 0))[:len(records) - MAX_JOURNALS + 1]:
            for name in ("journal.json", "manifest.json", "manifest.sig", "core.tar.gz.partial"):
                (path.parent / name).unlink(missing_ok=True)
        check(len(self.journal_records()) < MAX_JOURNALS, "capacity", "未决操作达到上限；请先查询和核对原操作")

    def save(self, journal: dict, phase: str | None = None, **changes) -> None:
        if phase is not None:
            journal["phase"] = phase
        journal.update(changes, updated_at=self.clock())
        atomic_json(self.journal_path(journal["operation_id"]), journal)
        self.fault(journal["phase"])

    def query(self, identity: str) -> dict:
        record = read_json(self.journal_path(identity))
        check(record is not None, "not_found", "操作记录不存在，未自动重放请求")
        result = {key: record.get(key) for key in ("operation_id", "operation", "phase", "status", "source_version", "target_version", "created_at", "updated_at", "retryable", "message", "backup", "failure_code")}
        if isinstance(record.get("result"), dict):
            result["result"] = record["result"]
        return result

    def ensure_no_active(self, identity: str = "") -> None:
        # The scan also covers a crash between journaling and publishing the active ID.
        for _, record in self.journal_records():
            if record.get("kind") == "deployment" and record.get("operation_id") != identity:
                check(record.get("status") in TERMINAL, "transaction_pending", "存在未完成部署事务；请按原 operation ID 继续或回退")

    def finish(self, journal: dict, status: str, message: str) -> dict:
        self.save(journal, "complete", status=status, retryable=False, message=message)
        if read_text(self.root / "active-operation") == journal["operation_id"]:
            (self.root / "active-operation").unlink(missing_ok=True)
            fsync_directory(self.root)
        directory = self.journal_path(journal["operation_id"]).parent
        for name in ("core.tar.gz.partial", "manifest.sig"):
            (directory / name).unlink(missing_ok=True)
        outward = "ok" if status == "cancelled" else status
        if status == "succeeded":
            outward = {"install": "installed", "update": "updated", "rollback": "rolled_back"}.get(journal["operation"], "ok")
        return {"status": outward, "message": message, "data": self.query(journal["operation_id"])}

    def make_journal(self, operation: str, identity: str, payload: dict) -> dict:
        fingerprint = hashlib.sha256(json_bytes(payload)).hexdigest()
        existing = read_json(self.journal_path(identity))
        if existing is not None:
            check(existing.get("operation") == operation and existing.get("fingerprint") == fingerprint, "operation_conflict", "同一 operation ID 的请求内容不同；未重放")
            return existing
        self.ensure_no_active(identity)
        self.reserve_journal()
        source = self.pointer("current")
        journal = {"schema_version": 1, "kind": "deployment", "operation_id": identity, "operation": operation, "fingerprint": fingerprint,
                   "payload": payload, "phase": "created", "status": "running", "created_at": self.clock(), "updated_at": self.clock(),
                   "source": source, "source_version": self.metadata(source).get("version", ""), "target": "", "target_version": "", "backup": identity,
                   "retryable": True, "message": "部署事务已建立", "desired_before": self.desired()}
        self.save(journal)
        atomic_bytes(self.root / "active-operation", identity.encode())
        return journal

    def prepare_install(self, journal: dict) -> None:
        payload = journal["payload"]
        key = self.state / "trust/release-ed25519-public.pem"
        check(key.is_file() and not key.is_symlink() and key.stat().st_size < 8192, "pending_manifest", "缺少受信发布公钥；未下载或安装 Core")
        self.backend.validate_environment()
        version = payload.get("apk_version", "")
        check(isinstance(version, str) and len(version) <= 80 and VERSION.fullmatch(version) is not None, "version_mismatch", "必须提供当前 APK 的准确产品版本")
        directory = self.journal_path(journal["operation_id"]).parent
        manifest, signature = directory / "manifest.json", directory / "manifest.sig"
        self.save(journal, "manifest")
        self.backend.fetch(https_url(payload.get("manifest_url", "")), manifest, MAX_JSON)
        self.backend.fetch(https_url(payload.get("manifest_signature_url", "")), signature, 4096)
        self.backend.verify_signature(key, manifest, signature)
        manifest_digest = sha256(manifest)
        check(not journal.get("manifest_digest") or journal["manifest_digest"] == manifest_digest, "manifest_changed", "原操作的签名清单内容已改变，请取消后重新确认；未替换目标")
        self.save(journal, manifest_digest=manifest_digest)
        release = read_json(manifest)
        check(isinstance(release, dict) and release.get("schema_version") == 1 and release.get("platform") == "linux"
              and release.get("arch") == "arm64" and release.get("version") == version
              and re.fullmatch(r"[a-f0-9]{64}", str(release.get("sha256", ""))) is not None, "version_mismatch", "签名清单的平台、架构、摘要或 APK/Core 版本不匹配")
        asset_url = https_url(release.get("asset_url", ""))
        archive = directory / "core.tar.gz.partial"
        self.save(journal, "download", target_version=version)
        if not archive.is_file() or sha256(archive) != release["sha256"]:
            self.backend.fetch(asset_url, archive, MAX_ARCHIVE, resume=True)
        check(sha256(archive) == release["sha256"], "verification_failed", "Core 包 SHA-256 不匹配；未进入维护阶段")
        target_name = version + "-" + release["sha256"][:12]
        target = self.root / "versions" / target_name
        stage = self.root / "versions" / (".stage-" + journal["operation_id"])
        self.save(journal, "verify", target=target_name)
        if target.exists():
            metadata = self.metadata(target_name)
            check(metadata.get("sha256") == release["sha256"] and metadata.get("binary_sha256") == sha256(target / "bin/agentdock"), "version_conflict", "已有版本目录内容不同；未覆盖")
        else:
            if stage.exists():
                check(not stage.is_symlink(), "unsafe_path", "版本暂存目录无效")
                shutil.rmtree(stage)
            stage.mkdir(mode=0o700)
            safe_extract(archive, stage)
            check((stage / "bin/agentdock").is_file() and os.access(stage / "bin/agentdock", os.X_OK), "verification_failed", "缺少可执行 Core")
            check(self.backend.binary_version(stage) == version, "version_mismatch", "Core 自报版本与签名清单不同")
            metadata = {key: release[key] for key in ("version", "platform", "arch", "sha256")}
            metadata["binary_sha256"] = sha256(stage / "bin/agentdock")
            metadata["data_schema"] = str(release.get("data_schema", "unknown"))[:128]
            atomic_json(stage / ".workbench-release.json", metadata)
            os.replace(stage, target)
            fsync_directory(target.parent)
        self.save(journal, "prepared", target=target_name, target_version=version)

    def source_healthy(self, journal: dict) -> bool:
        source = journal["source"]
        if not source:
            return False
        if self.backend.health(journal["source_version"]):
            return True
        proof = read_json(self.root / "health-proof.json", {})
        binary = self.root / "versions" / source / "bin/agentdock"
        return proof.get("version_directory") == source and binary.is_file() and proof.get("binary_sha256") == sha256(binary)

    def backup_path(self, identity: str) -> Path:
        check(isinstance(identity, str) and IDENTIFIER.fullmatch(identity) is not None, "unsafe_state", "备份标识无效")
        path = self.root / "backups" / identity
        check(not path.is_symlink(), "unsafe_path", "备份目录不能是符号链接")
        return path

    def restore_data(self, journal: dict, backup: str, label: str) -> None:
        source = self.backup_path(backup)
        check(read_json(source / ".snapshot-complete.json", {}).get("schema_version") == 1, "backup_missing", "缺少完整一致性备份；未自动降级")
        verify_snapshot(source)
        destination = self.root / "data"
        quarantine = self.root / "quarantine" / (journal["operation_id"] + "-" + label)
        restored = self.root / (".restore-" + journal["operation_id"] + "-" + label)
        marker = self.journal_path(journal["operation_id"]).parent / (label + "-restored.json")
        if marker.exists():
            (destination / ".restore-ready").unlink(missing_ok=True)
            return
        # The copy-complete marker survives the rename. Re-entry after either rename
        # cannot overwrite the only snapshot or silently mix schemas.
        if not (restored / ".restore-ready").exists():
            if restored.exists():
                check(not restored.is_symlink(), "unsafe_path", "恢复暂存目录无效")
                shutil.rmtree(restored)
            size = tree_size(source / "data")
            check(shutil.disk_usage(self.root).free > size + 64 * 1024 * 1024, "disk_full", "恢复空间不足；备份保持完整")
            shutil.copytree(source / "data", restored, symlinks=True)
            atomic_bytes(restored / ".restore-ready", b"1")
        if not quarantine.exists() and destination.exists():
            os.replace(destination, quarantine)
            fsync_directory(self.root)
            self.fault(label + "-quarantined")
        if not destination.exists():
            os.replace(restored, destination)
            fsync_directory(self.root)
            self.fault(label + "-published")
        check((destination / ".restore-ready").exists(), "restore_conflict", "恢复目标已有未知数据；未覆盖")
        atomic_json(marker, {"schema_version": 1, "backup": backup})
        (destination / ".restore-ready").unlink(missing_ok=True)
        if restored.exists():
            shutil.rmtree(restored)

    def recover_previous(self, journal: dict) -> dict:
        self.save(journal, "rollback", status="recovering")
        self.backend.stop()
        if (self.backup_path(journal["backup"]) / ".snapshot-complete.json").exists():
            self.restore_data(journal, journal["backup"], "failed-new-data")
        self.set_pointer("current", journal["source"])
        if journal["source"] and self.desired() == "running":
            self.backend.start(journal["source_version"])
        return self.finish(journal, "rolled_back", "部署未提交，已恢复旧版本与一致性数据；失败数据仍在私有隔离目录")

    def execute_transaction(self, operation: str, identity: str, payload: dict) -> dict:
        new = read_json(self.journal_path(identity)) is None
        journal = self.make_journal(operation, identity, payload)
        if journal["status"] in TERMINAL:
            if read_text(self.root / "active-operation") == identity:
                (self.root / "active-operation").unlink(missing_ok=True)
                fsync_directory(self.root)
            success = journal["status"] == "succeeded"
            if success:
                try:
                    self.cleanup(False)
                except (BridgeError, OSError):
                    pass  # Committed health is not invalidated by deferred cleanup.
            return {"status": "ok" if success else "failed", "message": "返回原操作终态，未重复执行", "data": self.query(identity)}
        if new and payload.get("start_after_install") is True:
            self.set_desired("running")
        try:
            if journal["phase"] == "rollback":
                return self.recover_previous(journal)
            if journal["phase"] in {"created", "manifest", "download", "verify"}:
                if operation == "rollback":
                    fallback = read_json(self.root / "fallback.json", {})
                    target = fallback.get("version_directory", "")
                    check(target and fallback.get("backup"), "backup_missing", "没有可验证的完整回退点")
                    metadata = self.metadata(target)
                    check(metadata.get("binary_sha256") == sha256(self.root / "versions" / target / "bin/agentdock"), "verification_failed", "回退二进制改变；未自动降级")
                    self.backup_path(fallback["backup"])
                    self.save(journal, "prepared", target=target, target_version=metadata["version"], restore_backup=fallback["backup"])
                else:
                    self.prepare_install(journal)
            if journal["phase"] == "prepared":
                check(self.metadata(journal["target"]).get("binary_sha256") == sha256(self.root / "versions" / journal["target"] / "bin/agentdock"), "verification_failed", "暂存二进制已改变，未切换")
                self.save(journal, "stop", source_was_healthy=self.source_healthy(journal))
            if journal["phase"] == "stop":
                self.backend.stop()
                self.save(journal, "backup")
            if journal["phase"] == "backup":
                snapshot_tree(self.root / "data", self.backup_path(journal["backup"]))
                self.save(journal, "switch")
            if journal["phase"] == "switch":
                if operation == "rollback":
                    self.restore_data(journal, journal["restore_backup"], "pre-rollback-data")
                self.set_pointer("current", journal["target"])
                self.fault("pointer-published")
                self.save(journal, "start")
            if journal["phase"] == "start":
                check(self.desired() == "running", "user_stopped", "节点停止意图已保留；请明确恢复后继续验证")
                self.backend.start(journal["target_version"])
                self.save(journal, "health")
            if journal["phase"] == "health":
                check(self.backend.health(journal["target_version"]), "health_failed", "新版本未通过真实节点身份与管理鉴权检查")
                self.save(journal, "commit")
            if journal["phase"] == "commit":
                atomic_json(self.root / "health-proof.json", {"version_directory": journal["target"], "binary_sha256": self.metadata(journal["target"])["binary_sha256"], "verified_at": self.clock()})
                if journal["source"] and journal.get("source_was_healthy"):
                    atomic_json(self.root / "fallback.json", {"version_directory": journal["source"], "backup": journal["backup"], "product_version": journal["source_version"]})
                    self.set_pointer("previous", journal["source"])
                result = self.finish(journal, "succeeded", "版本、管理鉴权和健康验证通过，部署事务已提交")
                try:
                    result["data"]["retention"] = self.cleanup(False)
                except (BridgeError, OSError):
                    result["data"]["retention"] = {"warning": "安装已提交；旧文件清理需要另行核对"}
                return result
            raise BridgeError("unsafe_state", "未知部署阶段；保留原事务")
        except BridgeError as error:
            safe = redact(str(error))
            self.save(journal, failure_code=error.code, message=safe, retryable=True, status="requires_user_action")
            if journal["phase"] in {"switch", "start", "health", "commit", "rollback"} and error.code != "user_stopped":
                try:
                    result = self.recover_previous(journal)
                    result["status"] = "failed"
                    result["message"] = safe + "；旧版本及数据已回退"
                    return result
                except (BridgeError, OSError, subprocess.SubprocessError) as rollback_error:
                    self.save(journal, "rollback", status="requires_user_action", message="回退待继续核对，所有备份与版本引用已保留：" + redact(str(rollback_error)), retryable=True)
            return {"status": "pending_manifest" if error.code == "pending_manifest" else "requires_user_action", "message": safe, "data": self.query(identity)}
        except (OSError, ValueError, subprocess.SubprocessError, tarfile.TarError) as error:
            self.save(journal, status="requires_user_action", failure_code="io_failed", message="部署 I/O 中断，可查询原 operation ID 后继续", retryable=True)
            raise BridgeError("io_failed", "部署 I/O 中断；未重复执行，原事务与备份保留", True) from error

    def recovery(self, manual_reset: bool = False) -> dict:
        if self.desired() != "running":
            return {"status": "stopped", "message": "用户已停止节点，未自动恢复"}
        self.ensure_no_active()
        version = self.current_version()
        record = read_json(self.root / "recovery.json", {"failures": 0, "next_attempt_at": 0, "circuit_open": False})
        check(isinstance(record, dict) and type(record.get("failures", 0)) is int and 0 <= record.get("failures", 0) <= 10, "unsafe_state", "恢复状态损坏，未自动重置")
        if self.backend.health(version):
            atomic_json(self.root / "recovery.json", {"failures": 0, "next_attempt_at": 0, "circuit_open": False, "last_success": self.clock()})
            return {"status": "healthy", "message": "节点健康，无需恢复"}
        if manual_reset:
            record = {"failures": 0, "next_attempt_at": 0, "circuit_open": False}
        check(not record.get("circuit_open"), "recovery_circuit_open", "自动恢复已熔断，需要用户核对并明确恢复")
        check(self.clock() >= record.get("next_attempt_at", 0), "recovery_backoff", "恢复退避尚未结束，未重复启动")
        record.update(failures=record.get("failures", 0) + 1, last_attempt_at=self.clock())
        record["next_attempt_at"] = self.clock() + min(self.config["recovery_max_seconds"], self.config["recovery_base_seconds"] * (2 ** (record["failures"] - 1)))
        record["circuit_open"] = record["failures"] >= self.config["recovery_max_failures"]
        atomic_json(self.root / "recovery.json", record)
        try:
            self.backend.start(version)
            check(self.backend.health(version), "health_failed", "恢复后的管理鉴权未通过")
        except (BridgeError, OSError, subprocess.SubprocessError) as error:
            record["last_error"] = redact(str(error))
            atomic_json(self.root / "recovery.json", record)
            raise BridgeError("health_failed", "恢复未通过；失败次数、退避时间和熔断状态已持久化") from error
        atomic_json(self.root / "recovery.json", {"failures": 0, "next_attempt_at": 0, "circuit_open": False, "last_success": self.clock()})
        current = self.pointer("current")
        if current and self.metadata(current).get("binary_sha256"):
            atomic_json(self.root / "health-proof.json", {"version_directory": current, "binary_sha256": self.metadata(current)["binary_sha256"], "verified_at": self.clock()})
        return {"status": "healthy", "message": "当前版本已恢复，未安装或更新版本"}

    def cleanup(self, preview: bool = True) -> dict:
        protected = {self.pointer("current"), self.pointer("previous")}
        fallback = read_json(self.root / "fallback.json", {})
        protected.add(fallback.get("version_directory", ""))
        backups = {fallback.get("backup", "")}
        for _, record in self.journal_records():
            if record.get("status") not in TERMINAL:
                protected.update((record.get("source", ""), record.get("target", "")))
                backups.update((record.get("backup", ""), record.get("restore_backup", "")))
        versions_dir, backups_dir = self.root / "versions", self.root / "backups"
        eligible = []
        for path in list(versions_dir.iterdir()) if versions_dir.is_dir() else []:
            if path.is_dir() and not path.is_symlink() and not path.name.startswith(".") and path.name not in protected and (path / ".workbench-release.json").is_file():
                eligible.append(path)
        old_backups = [path for path in (list(backups_dir.iterdir()) if backups_dir.is_dir() else []) if path.is_dir() and not path.is_symlink() and path.name not in backups and (path / ".snapshot-complete.json").is_file()]
        check(len(eligible) + len(old_backups) <= 256, "capacity", "清理对象超过上限；未执行")
        result = {"versions": [path.name for path in eligible], "backups": [path.name for path in old_backups], "preview": preview,
                  "preserved": ["current", "verified fallback", "unfinished transactions", "quarantined data", "user projects", "identity"]}
        if not preview:
            for path in eligible + old_backups:
                shutil.rmtree(path)
        return result

    def logs(self, payload: dict) -> dict:
        name = payload.get("log", "core")
        check(name in ("core", "previous"), "invalid_request", "日志类型无效")
        path = self.root / "logs" / ("core.log" if name == "core" else "core.previous.log")
        offset = payload.get("offset", 0)
        check(type(offset) is int and 0 <= offset <= 2**53, "invalid_request", "日志游标无效")
        query = str(payload.get("search", ""))[:256]
        if not path.exists():
            return {"status": "ok", "message": "尚无该类日志", "data": {"text": "", "offset": 0, "next_offset": 0, "has_more": False}}
        check(not path.is_symlink() and path.is_file(), "unsafe_path", "日志文件类型无效")
        size, output, consumed = path.stat().st_size, [], 0
        with path.open("rb") as stream:
            start = min(offset, size)
            if start:
                stream.seek(start - 1)
                if stream.read(1) != b"\n":
                    # Never display a partial secret when a caller supplies a mid-line cursor.
                    while stream.tell() < size and consumed < 65536:
                        chunk = stream.readline(8193)
                        consumed += len(chunk)
                        if chunk.endswith(b"\n"):
                            break
            else:
                stream.seek(0)
            while stream.tell() < size and consumed < 65536 and len(output) < 200:
                line = stream.readline(8193)
                consumed += len(line)
                if len(line) > 8192:
                    while line and not line.endswith(b"\n") and consumed < 65536:
                        line = stream.readline(8193)
                        consumed += len(line)
                    output.append("[超长日志行已省略]")
                    continue
                safe = redact(line.decode("utf-8", "replace"), 8192)
                if not query or query.casefold() in safe.casefold():
                    output.append(safe.rstrip("\r\n"))
                if sum(len(item.encode()) for item in output) > 14000:
                    break
            next_offset = stream.tell()
        text = "\n".join(output)
        return {"status": "ok", "message": "仅返回完整行的有界脱敏日志", "data": {"text": text, "offset": start, "next_offset": next_offset, "has_more": next_offset < size, "original_bytes": size}}

    def validate_payload(self, operation: str, payload: dict) -> None:
        allowed = {
            "probe": set(), "status": set(), "bootstrap": set(), "start": set(), "stop": set(), "restart": set(),
            "guardian_check": set(), "diagnostic_preview": set(), "cleanup_preview": set(), "export_diagnostics": set(),
            "logs": {"log", "offset", "search"}, "operation_query": {"target_operation_id"},
            "configure": {"node"}, "adopt": {"existing_root", "confirm_adopt"},
            "install": {"apk_version", "manifest_url", "manifest_signature_url", "start_after_install"},
            "update": {"apk_version", "manifest_url", "manifest_signature_url", "start_after_install"},
            "resume": {"target_operation_id", "confirm_start"},
            "cancel_operation": {"target_operation_id", "confirm_cancel"},
            "repair": {"confirm_reset"}, "rollback": {"confirm_data_restore", "start_after_install"},
            "cleanup": {"confirm_cleanup", "preview_digest"},
            "path_probe": {"termux_path", "confirm_path"},
            "project_create": {"termux_path", "confirm_path", "name"},
        }
        check(operation in allowed and isinstance(payload, dict), "invalid_request", "不支持该部署操作")
        check(not set(payload) - (allowed[operation] | {"source"}), "invalid_request", "请求包含未声明字段")
        no_secrets(payload)

    def dispatch(self, operation: str, identity: str, payload: dict) -> dict:
        self.validate_payload(operation, payload)
        if operation in READ_ONLY:
            if operation == "operation_query":
                return {"status": "ok", "message": "只读取原操作记录，未重放", "data": self.query(payload.get("target_operation_id", ""))}
            if operation == "logs":
                return self.logs(payload)
            if operation == "cleanup_preview":
                preview = self.cleanup(True)
                preview["preview_digest"] = hashlib.sha256(json_bytes(preview)).hexdigest()
                return {"status": "ok", "message": "仅预览可安全清理的受管对象", "data": preview}
            if operation == "diagnostic_preview":
                return {"status": "ok", "message": "诊断只包含节点状态和有界脱敏日志，不含身份或工程", "data": {"categories": ["version", "desired state", "environment", "redacted Core log"], "excludes": ["credentials", "OAuth state", "project files"]}}
            return self.status()
        if operation == "stop":
            self.root.mkdir(mode=0o700, parents=True, exist_ok=True)
            self.set_desired("stopped")
        with self.lock():
            self.initialize()
            if operation in ("install", "update", "rollback"):
                if operation == "rollback":
                    check(payload.get("confirm_data_restore") is True, "confirmation_required", "回退会恢复一致性数据快照，必须明确确认")
                return self.execute_transaction(operation, identity, payload)
            if operation == "resume":
                target = payload.get("target_operation_id", "")
                original = read_json(self.journal_path(target))
                check(original is not None and original.get("kind") == "deployment", "not_found", "原部署操作不存在；未重放运行命令")
                if payload.get("confirm_start") is True:
                    self.set_desired("running")
                return self.execute_transaction(original["operation"], target, original["payload"])
            if operation == "cancel_operation":
                check(payload.get("confirm_cancel") is True, "confirmation_required", "取消事务需要明确确认")
                original = read_json(self.journal_path(payload.get("target_operation_id", "")))
                check(original is not None and original.get("kind") == "deployment", "not_found", "没有对应部署事务")
                if original["status"] in TERMINAL:
                    return {"status": "ok", "message": "原操作已有终态，未撤销已提交安装", "data": self.query(original["operation_id"])}
                if original["phase"] in {"created", "manifest", "download", "verify", "prepared"}:
                    return self.finish(original, "cancelled", "部署已取消；未切换版本或回写数据")
                return self.recover_previous(original)
            # Runtime commands also have a persistent receipt. A lost callback never
            # authorizes replay of a restart, configuration update or file write.
            fingerprint = hashlib.sha256(json_bytes(payload)).hexdigest()
            existing = read_json(self.journal_path(identity))
            if existing:
                check(existing.get("operation") == operation and existing.get("fingerprint") == fingerprint, "operation_conflict", "原操作标识已被不同请求使用")
                if existing.get("status") in TERMINAL and isinstance(existing.get("result"), dict):
                    return existing["result"]
                return {"status": "requires_user_action", "message": "原运行操作没有终态；请读取节点状态，不自动重放", "data": self.query(identity)}
            self.reserve_journal()
            record = {"schema_version": 1, "kind": "runtime", "operation_id": identity, "operation": operation,
                      "fingerprint": fingerprint, "phase": "running", "status": "running", "created_at": self.clock(), "retryable": False}
            self.save(record)
            try:
                result = self.runtime_operation(operation, identity, payload)
                terminal_status = "succeeded" if result["status"] in {"ok", "healthy", "running", "stopped", "adopted"} else "failed"
                self.save(record, "complete", status=terminal_status, message=result.get("message", ""), result=result)
                return result
            except (BridgeError, OSError, subprocess.SubprocessError) as error:
                code = error.code if isinstance(error, BridgeError) else "io_failed"
                result = {"status": "requires_user_action" if code in {"unknown_process", "recovery_circuit_open", "recovery_backoff", "transaction_pending"} else "failed", "error_code": code, "message": redact(str(error)) if isinstance(error, BridgeError) else "运行 I/O 失败，未重放请求"}
                self.save(record, "complete", status="failed", message=result["message"], result=result)
                return result

    def runtime_operation(self, operation: str, identity: str, payload: dict) -> dict:
        if operation == "stop":
            self.backend.stop()
            return {"status": "stopped", "message": "停止意图已落盘，受管进程已停止；未修改守护开关"}
        self.ensure_no_active()
        if operation == "bootstrap":
            value = self.backend.environment()
            if value["missing"]:
                return {"status": "requires_user_action", "message": "请在 Termux 执行导出的 bootstrap", "data": value}
            self.backend.validate_environment()
            token = self.root / "auth-token"
            if token.exists() or token.is_symlink():
                self.backend.bearer()
            else:
                atomic_bytes(token, secrets.token_hex(32).encode())
            return {"status": "ok", "message": "私有节点目录与环境已准备；Core 仍需签名清单安装"}
        if operation == "configure":
            check(self.backend.owned_pid() is None, "node_running", "节点运行时不修改运行配置，请先停止")
            self.config = self.validate_config(payload.get("node", {}))
            atomic_json(self.root / "node.json", self.config)
            atomic_bytes(self.root / "port", str(self.config["port"]).encode())
            self.backend = self.backend_factory(self.root, self.config)
            return {"status": "ok", "message": "节点配置已保存；未启动 Core", "data": self.config}
        if operation in ("start", "restart"):
            self.set_desired("running")
            if operation == "restart":
                self.backend.stop()
            return self.recovery(manual_reset=True)
        if operation in ("repair", "guardian_check"):
            return self.recovery(manual_reset=operation == "repair" and payload.get("confirm_reset") is True)
        if operation == "adopt":
            check(payload.get("confirm_adopt") is True, "confirmation_required", "已有节点接管须明确确认")
            raw = payload.get("existing_root", "")
            check(isinstance(raw, str) and raw.startswith("/"), "invalid_request", "已有节点目录无效")
            candidate = inside(Path(raw), self.home, exists=True)
            check((candidate / "node.json").is_file(), "incompatible_layout", "该节点没有受管部署描述；仅提供只读诊断，不直接迁移")
            config = self.validate_config(read_json(candidate / "node.json"))
            backend = self.backend_factory(candidate, config)
            check(backend.owned_pid() is None or backend.health(), "unknown_process", "已有节点身份未验证；未接管")
            atomic_bytes(self.state / "adopted-root", str(candidate).encode())
            return {"status": "adopted", "message": "只建立受管目录引用，未替换程序、配置或身份"}
        if operation == "cleanup":
            check(payload.get("confirm_cleanup") is True, "confirmation_required", "清理前必须预览并确认")
            preview = self.cleanup(True)
            expected = hashlib.sha256(json_bytes(preview)).hexdigest()
            check(secrets.compare_digest(expected, str(payload.get("preview_digest", ""))), "preview_changed", "清理预览已变化，请重新确认；未执行删除")
            return {"status": "ok", "message": "仅清理无恢复引用的受管旧版本和备份", "data": self.cleanup(False)}
        if operation == "export_diagnostics":
            out = self.root / "logs" / ("diagnostics-" + identity + ".json")
            value = {"schema_version": 1, "status": self.status(), "logs": self.logs({})["data"], "excludes": ["credentials", "OAuth", "projects"]}
            atomic_json(out, value)
            return {"status": "ok", "message": "诊断已保留在 Termux 私有目录", "data": {"relative_path": "logs/" + out.name, "bytes": out.stat().st_size}}
        if operation in ("path_probe", "project_create"):
            check(payload.get("confirm_path") is True, "confirmation_required", "Termux 路径须由用户明确授权")
            raw = payload.get("termux_path", "")
            check(isinstance(raw, str) and raw.startswith("/") and len(raw) <= 2048, "invalid_request", "路径无效")
            root = Path(raw).resolve(strict=True)
            allowed = root == self.home or self.home in root.parents or str(root).startswith("/storage/emulated/0/")
            check(allowed and root.is_dir(), "unsafe_path", "只允许明确授权的 Termux home 或共享存储目录")
            if operation == "project_create":
                name = payload.get("name", "")
                check(isinstance(name, str) and 0 < len(name) <= 128 and name not in (".", "..") and not any(c in name for c in '/\\\x00\n\r'), "invalid_request", "工程名称无效")
                root = inside(root / name, root)
                root.mkdir(mode=0o700, exist_ok=True)
                rules = root / "AGENTS.md"
                if not rules.exists():
                    descriptor = os.open(rules, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
                    with os.fdopen(descriptor, "w") as output:
                        output.write("# Project rules\n\nPreserve user files and verify changes before delivery.\n")
            marker = root / (".wb-path-" + identity)
            descriptor = os.open(marker, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
            try:
                with os.fdopen(descriptor, "wb") as output:
                    output.write(b"path-probe")
                check(marker.read_bytes() == b"path-probe", "path_unavailable", "路径回读失败")
            finally:
                marker.unlink(missing_ok=True)
            return {"status": "ok", "message": "Termux 路径读写已验证；Android SAF 权限仍独立验证", "data": {"termux_path": str(root), "readable": True, "writable": True, "android_uri_mapped": False}}
        raise BridgeError("invalid_request", "不支持该部署操作")


def main(argv: list[str]) -> int:
    if len(argv) == 2 and argv[0] == "--launch":
        launch_owned(Path(argv[1]))
        return 1
    envelope = {"schema_version": 1}
    try:
        check(len(argv) == 5, "invalid_request", "部署入口需要五个固定参数")
        operation, operation_id, request_id, nonce, payload_path = argv
        check(operation in OPERATIONS and IDENTIFIER.fullmatch(operation_id) is not None and IDENTIFIER.fullmatch(request_id) is not None and NONCE.fullmatch(nonce) is not None and payload_path == "-", "invalid_request", "部署请求标识或操作无效")
        envelope.update(operation=operation, operation_id=operation_id, request_id=request_id, nonce=nonce)
        def expired(_signal, _frame):
            raise BridgeError("invalid_request", "部署请求输入超时")
        signal.signal(signal.SIGALRM, expired)
        signal.alarm(5)
        try:
            data = sys.stdin.buffer.read(MAX_JSON + 1)
        finally:
            signal.alarm(0)
        check(len(data) <= MAX_JSON, "invalid_request", "部署请求超过 64 KiB")
        payload = json.loads(data, parse_constant=lambda _value: (_ for _ in ()).throw(ValueError("non-finite JSON")))
        check(isinstance(payload, dict) and type(payload.get("schema_version")) is int and all(payload.get(key) == value for key, value in envelope.items()), "invalid_request", "请求正文与部署标识不一致")
        for key in envelope:
            payload.pop(key)
        no_secrets(payload)
        home = Path(os.environ.get("AGENTDOCK_WORKBENCH_TEST_HOME", "")) if os.environ.get("AGENTDOCK_WORKBENCH_TEST_MODE") == "1" else Path.home()
        manager = Deployment(home)
        result = manager.dispatch(operation, operation_id, payload)
        success = result["status"] in {"ok", "healthy", "running", "stopped", "installed", "updated", "rolled_back", "adopted"}
        blocked = result["status"] in {"pending_manifest", "requires_user_action"}
        envelope.update(result, phase="complete" if success else "blocked" if blocked else "failed")
        sys.stdout.buffer.write(json_bytes(envelope))
        return 0 if (success or blocked) and not result.get("error_code") else 1
    except BridgeError as error:
        code = "requires_user_action" if error.code in {"unknown_process", "recovery_backoff", "recovery_circuit_open", "transaction_pending", "user_stopped"} else error.code
        envelope.update(status=code, phase="failed", message=redact(str(error)), error_code=error.code, retryable=error.retryable)
    except (ValueError, TypeError, UnicodeError, RecursionError):
        envelope.update(status="invalid_request", phase="failed", message="部署请求格式无效")
    except (OSError, subprocess.SubprocessError, tarfile.TarError):
        envelope.update(status="failed", phase="failed", message="部署 I/O 失败；请按原 operation ID 查询，未自动重放")
    sys.stdout.buffer.write(json_bytes(envelope))
    return 1

if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
