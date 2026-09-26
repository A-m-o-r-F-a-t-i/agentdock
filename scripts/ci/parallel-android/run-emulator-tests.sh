#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
api="${ANDROID_API_LEVEL:?ANDROID_API_LEVEL is required}"
export ANDROID_SYSTEM_IMAGE_API_LEVEL="${ANDROID_SYSTEM_IMAGE_API_LEVEL:-$api}"
evidence="$repo_root/evidence/emulator-api-$api"
mkdir -p "$evidence/screenshots" "$evidence/reports"
version_code=$((101070000 + GITHUB_RUN_NUMBER))

run_connected_tests() {
  local log_path="$1"
  set +e
  gradle -p "$repo_root/mobile/android" --no-daemon --stacktrace \
    connectedDebugAndroidTest \
    -PagentdockAndroidVersionCode="$version_code" \
    -PagentdockCandidateSha="$GITHUB_SHA" \
    -PagentdockCandidateRunId="$GITHUB_RUN_ID" \
    -PagentdockCandidateRunAttempt="$GITHUB_RUN_ATTEMPT" \
    2>&1 | tee "$log_path"
  local status=${PIPESTATUS[0]}
  set -e
  return "$status"
}

attempt_one_log="$evidence/connected-test-attempt1.log"
attempt_two_log="$evidence/connected-test-attempt2.log"
attempt_one_rc=0
attempt_two_rc=-1
run_connected_tests "$attempt_one_log" || attempt_one_rc=$?
# Retry API 37 once after confirming the already-booted device is responsive;
# retain both logs so a passing retry never hides the first failure.
test_rc=$attempt_one_rc
if [[ "$api" == "37" && "$attempt_one_rc" -ne 0 ]]; then
  adb wait-for-device
  adb shell getprop sys.boot_completed | tee "$evidence/retry-device-ready.txt"
  sleep 3
  attempt_two_rc=0
  run_connected_tests "$attempt_two_log" || attempt_two_rc=$?
  test_rc=$attempt_two_rc
fi
if [[ "$attempt_two_rc" -ge 0 ]]; then
  cp "$attempt_two_log" "$evidence/connected-test.log"
else
  cp "$attempt_one_log" "$evidence/connected-test.log"
fi

adb logcat -d -v threadtime >"$evidence/logcat.txt" 2>&1 || true
adb shell getprop >"$evidence/device-properties.txt" 2>&1 || true
adb shell pm list packages | sort >"$evidence/packages.txt" 2>&1 || true
adb pull "/sdcard/Android/data/dev.agentdock.workbench.candidate/files/screenshots/." "$evidence/screenshots/" >"$evidence/adb-pull.txt" 2>&1 || true

for source in \
  "$repo_root/mobile/android/app/build/reports/androidTests/connected" \
  "$repo_root/mobile/android/app/build/outputs/androidTest-results/connected"; do
  if [[ -d "$source" ]]; then
    cp -a "$source" "$evidence/reports/$(basename "$source")"
  fi
done

python3 - "$evidence/result.json" "$api" "$test_rc" "$attempt_one_rc" "$attempt_two_rc" <<'PY'
import json, os, pathlib, sys
path = pathlib.Path(sys.argv[1])
api = int(sys.argv[2])
rc = int(sys.argv[3])
attempt_one = int(sys.argv[4])
attempt_two = int(sys.argv[5])
shots = sorted(p.name for p in (path.parent / "screenshots").rglob("*.png"))
path.write_text(json.dumps({
    "schema_version": 1,
    "lane": "WB07",
    "source_sha": os.environ["GITHUB_SHA"],
    "run_id": os.environ["GITHUB_RUN_ID"],
    "run_attempt": os.environ["GITHUB_RUN_ATTEMPT"],
    "api_level": api,
    "system_image_api_level": os.environ.get("ANDROID_SYSTEM_IMAGE_API_LEVEL", str(api)),
    "architecture": "x86_64 emulator",
    "connected_test_exit_code": rc,
    "attempt_exit_codes": [attempt_one] + ([] if attempt_two < 0 else [attempt_two]),
    "screenshots": shots,
    "fixture_scope": "navigation/layout only",
    "physical_arm64_termux_scope": "not exercised"
}, ensure_ascii=False, indent=2) + "\n")
PY

if [[ "$test_rc" -ne 0 ]]; then
  python3 - "$evidence/failure-summary.txt" "$api" "$attempt_one_log" "$attempt_two_log" <<'PY'
from pathlib import Path
import os, re, sys
summary_path = Path(sys.argv[1])
api = sys.argv[2]
logs = [Path(value) for value in sys.argv[3:] if Path(value).is_file()]
pattern = re.compile(
    r"FAILURE:|FAILED|failure|error:|AssertionError|Exception|Caused by:|"
    r"WorkbenchNavigationTest|captureKeyPages|everyWorkbenchPage|No node|Semantics|"
    r"Process crashed|INSTRUMENTATION|Tests on .* failed",
    re.IGNORECASE,
)
selected = []
for log in logs:
    lines = log.read_text(errors="replace").splitlines()
    matches = [line for line in lines if pattern.search(line)]
    selected.extend([f"[{log.name}] {line}" for line in (matches[-120:] or lines[-80:])])
message = "\n".join(selected[-180:])[-24000:]
summary_path.write_text(message + "\n")
escaped = message.replace("%", "%25").replace("\r", "%0D").replace("\n", "%0A")
print(f"::error title=Android API {api} instrumentation failed::{escaped}")
step_summary = os.environ.get("GITHUB_STEP_SUMMARY")
if step_summary:
    with open(step_summary, "a", encoding="utf-8") as output:
        output.write(f"## Android API {api} failure summary\n\n```text\n{message}\n```\n")
PY
fi

exit "$test_rc"
