#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: smoke_cli.sh <agentdock-binary> <endpoint> <token-file>" >&2
  exit 2
fi

binary=$1
endpoint=$2
token_file=$3

if [[ ! -x "$binary" ]]; then
  echo "agentdock binary is not executable: $binary" >&2
  exit 1
fi
if [[ ! -s "$token_file" ]]; then
  echo "token file is missing or empty" >&2
  exit 1
fi

workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT
mkdir -p "$workdir/home"
export AGENTDOCK_HOME="$workdir/home"
export NO_COLOR=1

assert_json() {
  local path=$1
  python3 - "$path" <<'PY'
import json
from pathlib import Path
import sys
json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
PY
}

assert_jsonl() {
  local path=$1
  python3 - "$path" <<'PY'
import json
from pathlib import Path
import sys
lines = [line for line in Path(sys.argv[1]).read_text(encoding="utf-8").splitlines() if line]
if not lines:
    raise SystemExit("JSONL output is empty")
for line in lines:
    json.loads(line)
print(len(lines))
PY
}

expect_exit() {
  local expected=$1
  local stdout_path=$2
  local stderr_path=$3
  shift 3
  set +e
  "$@" >"$stdout_path" 2>"$stderr_path"
  local actual=$?
  set -e
  if [[ $actual -ne $expected ]]; then
    echo "expected exit $expected, got $actual: $*" >&2
    echo "--- stdout ---" >&2
    cat "$stdout_path" >&2 || true
    echo "--- stderr ---" >&2
    cat "$stderr_path" >&2 || true
    exit 1
  fi
}

# Legacy/root commands still execute as real subprocesses.
"$binary" version --json >"$workdir/version.json"
assert_json "$workdir/version.json"
"$binary" --help >"$workdir/help.txt"
grep -Fq "AgentDock Workbench CLI" "$workdir/help.txt"
for shell in bash zsh fish; do
  "$binary" completion "$shell" >"$workdir/completion-$shell.txt"
  grep -Fq agentdock "$workdir/completion-$shell.txt"
done

# Authenticated read path.
"$binary" status \
  --endpoint "$endpoint" \
  --token-file "$token_file" \
  --format json >"$workdir/status.json"
python3 - "$workdir/status.json" <<'PY'
import json, sys
value = json.load(open(sys.argv[1], encoding="utf-8"))
assert value["ok"] is True
assert value["service"] == "AgentDock"
assert value["auth_enabled"] is True
PY

# Bounded list output must be valid JSONL with exactly two records.
"$binary" task list \
  --endpoint "$endpoint" \
  --token-file "$token_file" \
  --limit 2 \
  --format jsonl >"$workdir/tasks.jsonl"
line_count=$(assert_jsonl "$workdir/tasks.jsonl")
[[ "$line_count" == "2" ]]
python3 - "$workdir/tasks.jsonl" <<'PY'
import json, sys
rows = [json.loads(line) for line in open(sys.argv[1], encoding="utf-8") if line.strip()]
assert [row["task_id"] for row in rows] == ["tsk_alpha", "tsk_beta"]
PY

# Real controlled writes exercise the CLI's JSON body construction.
"$binary" task create \
  --endpoint "$endpoint" \
  --token-file "$token_file" \
  --workspace wsp_candidate \
  --title "Candidate smoke" \
  --goal "Exercise the real CLI process" \
  --condition "Smoke passes" \
  --task-step S1=Verify \
  --json >"$workdir/task-create.json"
python3 - "$workdir/task-create.json" <<'PY'
import json, sys
value = json.load(open(sys.argv[1], encoding="utf-8"))
assert value["ok"] is True
assert value["task_id"] == "tsk_created"
PY

"$binary" task show tsk_created \
  --endpoint "$endpoint" \
  --token-file "$token_file" \
  --json >"$workdir/task-show.json"
assert_json "$workdir/task-show.json"

"$binary" workspace update wsp_candidate \
  --endpoint "$endpoint" \
  --token-file "$token_file" \
  --expected-revision 7 \
  --name "Candidate renamed" \
  --json >"$workdir/workspace-update.json"
python3 - "$workdir/workspace-update.json" <<'PY'
import json, sys
value = json.load(open(sys.argv[1], encoding="utf-8"))
assert value["workspace"]["rules_revision"] == 8
assert value["workspace"]["name"] == "Candidate renamed"
PY

# Stable machine-readable error classes.
printf '%s\n' 'wrong-test-token-0000000000' >"$workdir/wrong-token"
chmod 600 "$workdir/wrong-token"
expect_exit 4 "$workdir/unauthorized.json" "$workdir/unauthorized.err" \
  "$binary" status --endpoint "$endpoint" --token-file "$workdir/wrong-token" --json
python3 - "$workdir/unauthorized.json" <<'PY'
import json, sys
value = json.load(open(sys.argv[1], encoding="utf-8"))
assert value == {"code": "UNAUTHORIZED", "error": "token required", "exit_code": 4, "ok": False}
PY
[[ ! -s "$workdir/unauthorized.err" ]]

expect_exit 3 "$workdir/not-found.json" "$workdir/not-found.err" \
  "$binary" task show missing --endpoint "$endpoint" --token-file "$token_file" --json
python3 - "$workdir/not-found.json" <<'PY'
import json, sys
value = json.load(open(sys.argv[1], encoding="utf-8"))
assert value["code"] == "TASK_NOT_FOUND"
assert value["exit_code"] == 3
PY

expect_exit 8 "$workdir/unsupported.json" "$workdir/unsupported.err" \
  "$binary" plugin test demo --endpoint "$endpoint" --token-file "$token_file" --json
python3 - "$workdir/unsupported.json" <<'PY'
import json, sys
value = json.load(open(sys.argv[1], encoding="utf-8"))
assert value["code"] == "PLUGIN_TEST_UNSUPPORTED"
assert value["exit_code"] == 8
PY

expect_exit 2 "$workdir/invalid.json" "$workdir/invalid.err" \
  "$binary" permission set --scope global --mode full --expected-revision 1 --json
python3 - "$workdir/invalid.json" <<'PY'
import json, sys
value = json.load(open(sys.argv[1], encoding="utf-8"))
assert value["code"] == "INVALID_ARGUMENT"
assert value["exit_code"] == 2
PY

# Legacy plugin list with --home must not be routed to the online client.
"$binary" plugin list --home "$workdir/legacy-home" >"$workdir/legacy-plugin-list.json"
assert_json "$workdir/legacy-plugin-list.json"

# Neither the valid nor intentionally invalid test token may leak into outputs.
valid_token=$(cat "$token_file")
wrong_token=$(cat "$workdir/wrong-token")
if grep -R -F -e "$valid_token" -e "$wrong_token" "$workdir" \
  --exclude='wrong-token' --exclude='*.token' --exclude='token' >/dev/null; then
  echo "a token leaked into CLI smoke-test output" >&2
  exit 1
fi

printf '%s\n' "WB01 real CLI subprocess smoke: passed"
