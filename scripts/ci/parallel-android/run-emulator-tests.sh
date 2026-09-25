#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
api="${ANDROID_API_LEVEL:?ANDROID_API_LEVEL is required}"
evidence="$repo_root/evidence/emulator-api-$api"
mkdir -p "$evidence/screenshots" "$evidence/reports"

version_code=$((101070000 + GITHUB_RUN_NUMBER))
set +e
gradle -p "$repo_root/mobile/android" --no-daemon --stacktrace \
  connectedDebugAndroidTest \
  -PagentdockAndroidVersionCode="$version_code" \
  -PagentdockCandidateSha="$GITHUB_SHA" \
  -PagentdockCandidateRunId="$GITHUB_RUN_ID" \
  -PagentdockCandidateRunAttempt="$GITHUB_RUN_ATTEMPT" \
  2>&1 | tee "$evidence/connected-test.log"
test_rc=${PIPESTATUS[0]}
set -e

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

python3 - "$evidence/result.json" "$api" "$test_rc" <<'PY'
import json, os, pathlib, sys
path = pathlib.Path(sys.argv[1])
api = int(sys.argv[2])
rc = int(sys.argv[3])
shots = sorted(p.name for p in (path.parent / "screenshots").rglob("*.png"))
path.write_text(json.dumps({
    "schema_version": 1,
    "lane": "WB07",
    "source_sha": os.environ["GITHUB_SHA"],
    "run_id": os.environ["GITHUB_RUN_ID"],
    "run_attempt": os.environ["GITHUB_RUN_ATTEMPT"],
    "api_level": api,
    "architecture": "x86_64 emulator",
    "connected_test_exit_code": rc,
    "screenshots": shots,
    "fixture_scope": "navigation/layout only",
    "physical_arm64_termux_scope": "not exercised"
}, ensure_ascii=False, indent=2) + "\n")
PY

exit "$test_rc"
