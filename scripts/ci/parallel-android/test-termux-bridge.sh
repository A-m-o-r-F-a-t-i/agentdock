#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
bridge="$root/mobile/android/termux/agentdock-workbench"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
export AGENTDOCK_WORKBENCH_TEST_MODE=1
export AGENTDOCK_WORKBENCH_TEST_HOME="$tmp/home"
mkdir -p "$AGENTDOCK_WORKBENCH_TEST_HOME"

request() {
  local op="$1" oid="$2" rid="$3" nonce="$4" extra="${5-}"
  [[ -n "$extra" ]] || extra='{}'
  jq -cn --arg op "$op" --arg oid "$oid" --arg rid "$rid" --arg nonce "$nonce" --argjson extra "$extra" \
    '$extra + {schema_version:1,operation:$op,operation_id:$oid,request_id:$rid,nonce:$nonce}' |
    sh "$bridge" "$op" "$oid" "$rid" "$nonce" -
}

nonce=abcdefghijklmnopqrstuvwxyzABCDE123456
out="$(request probe op_probe req_probe "$nonce")"
jq -e '.operation=="probe" and .operation_id=="op_probe" and .request_id=="req_probe" and (.status|type=="string") and (has("credential")|not)' <<<"$out" >/dev/null
[ ! -e "$AGENTDOCK_WORKBENCH_TEST_HOME/.agentdock-workbench" ]
request status op_status req_status "$nonce" >/dev/null
[ ! -e "$AGENTDOCK_WORKBENCH_TEST_HOME/.agentdock-workbench" ]

out="$(request bootstrap op_boot req_boot "$nonce")"
jq -e '.status=="pending_manifest" or .status=="requires_user_action" or .status=="ok"' <<<"$out" >/dev/null

out="$(request install op_install req_install "$nonce")"
jq -e '.status=="pending_manifest"' <<<"$out" >/dev/null
[ ! -e "$AGENTDOCK_WORKBENCH_TEST_HOME/.agentdock-workbench/node/current" ]

set +e
bad="$(printf '{}' | sh "$bridge" start 'bad/id' req_bad "$nonce" - 2>/dev/null)"
rc=$?
set -e
[ "$rc" -ne 0 ]
jq -e '.status=="invalid_request"' <<<"$bad" >/dev/null

printf '%70000s' x >"$tmp/oversize"
set +e
oversize="$(sh "$bridge" status op_large req_large "$nonce" - <"$tmp/oversize" 2>/dev/null)"
rc=$?
set -e
[ "$rc" -ne 0 ]
jq -e '.status=="invalid_request"' <<<"$oversize" >/dev/null

# Unknown process ownership must never signal the current CI shell or a PID group.
node="$AGENTDOCK_WORKBENCH_TEST_HOME/.agentdock-workbench/node"
printf '%s\n' "$$" >"$node/core.pid"
set +e
out="$(request stop op_stop req_stop "$nonce")"
rc=$?
set -e
[ "$rc" -ne 0 ]
jq -e '.status=="requires_user_action"' <<<"$out" >/dev/null
kill -0 "$$"
[ "$(cat "$node/desired-state")" = stopped ]
[ "$(cat "$node/core.pid")" = "$$" ]
# The supervised child must not inherit the bridge's mutation lock descriptor.
rm -f "$node/core.pid" "$node/core.identity"
mkdir -p "$tmp/mock-bin" "$node/versions/fixture/bin"
printf '#!/bin/sh\nexit 1\n' >"$node/versions/fixture/bin/agentdock"
chmod 700 "$node/versions/fixture/bin/agentdock"
ln -s versions/fixture "$node/current"
printf '%064d\n' 0 >"$node/auth-token"
cat >"$tmp/mock-bin/proot-distro" <<'MOCK_PROOT'
#!/bin/sh
if [ -e "/proc/$$/fd/9" ]; then
  printf inherited >"$AGENTDOCK_WORKBENCH_TEST_HOME/child-lock"
else
  printf closed >"$AGENTDOCK_WORKBENCH_TEST_HOME/child-lock"
fi
exit 1
MOCK_PROOT
chmod 700 "$tmp/mock-bin/proot-distro"
set +e
out="$(PATH="$tmp/mock-bin:$PATH" request start op_lock req_lock "$nonce")"
rc=$?
set -e
[ "$rc" -ne 0 ]
[ "$(cat "$AGENTDOCK_WORKBENCH_TEST_HOME/child-lock")" = closed ]

# Exact code used by the bridge extractor is tested without bypassing signature gates in production.
python3 - "$bridge" "$tmp" <<'PYTEST'
import io, os, pathlib, subprocess, sys, tarfile
bridge, root = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
text = bridge.read_text()
code = text.split("<<'ARCHIVE_GUARD' || return 83\n", 1)[1].split("\nARCHIVE_GUARD", 1)[0]
cases = [
    ("good", [("bin/agentdock", tarfile.REGTYPE, b"fixture")], True),
    ("traversal", [("../escape", tarfile.REGTYPE, b"fixture")], False),
    ("absolute", [("/tmp/escape", tarfile.REGTYPE, b"fixture")], False),
    ("symlink", [("bin/link", tarfile.SYMTYPE, b"")], False),
    ("hardlink", [("bin/link", tarfile.LNKTYPE, b"")], False),
    ("duplicate", [("bin/app", tarfile.REGTYPE, b"a"), ("bin/app", tarfile.REGTYPE, b"b")], False),
    ("conflict", [("bin", tarfile.REGTYPE, b"a"), ("bin/app", tarfile.REGTYPE, b"b")], False),
    ("backslash", [("..\\escape", tarfile.REGTYPE, b"a")], False),
    ("reserved", [(".workbench-release.json", tarfile.REGTYPE, b"{}")], False),
]
for name, entries, success in cases:
    archive = root / (name + ".tar.gz"); stage = root / (name + "-stage"); stage.mkdir()
    with tarfile.open(archive, "w:gz") as output:
        for filename, kind, payload in entries:
            member = tarfile.TarInfo(filename); member.type = kind; member.mode = 0o7777
            member.size = len(payload) if kind == tarfile.REGTYPE else 0
            if kind in (tarfile.SYMTYPE, tarfile.LNKTYPE): member.linkname = "/tmp/escape"
            output.addfile(member, io.BytesIO(payload) if member.isfile() else None)
    result = subprocess.run([sys.executable, "-c", code, str(archive), str(stage)], capture_output=True, text=True)
    assert (result.returncode == 0) == success, (name, result.stderr)
    if success:
        assert (stage / "bin/agentdock").read_bytes() == b"fixture"
        assert (stage / "bin/agentdock").stat().st_mode & 0o7777 == 0o755
    else:
        assert not any(stage.iterdir()), (name, "partial extraction before validation")
print("9 archive cases passed")
PYTEST

echo 'termux bridge contract tests passed'
