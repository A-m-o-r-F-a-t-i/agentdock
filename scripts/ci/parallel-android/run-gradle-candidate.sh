#!/usr/bin/env bash
set -euo pipefail

version_code=$((101070000 + GITHUB_RUN_NUMBER))
mkdir -p evidence/build
set +e
gradle -p mobile/android --no-daemon --stacktrace --warning-mode all \
  lintDebug testDebugUnitTest assembleDebug assembleRelease assembleDebugAndroidTest \
  -PagentdockAndroidVersionCode="$version_code" \
  -PagentdockCandidateSha="$GITHUB_SHA" \
  -PagentdockCandidateRunId="$GITHUB_RUN_ID" \
  -PagentdockCandidateRunAttempt="$GITHUB_RUN_ATTEMPT" \
  2>&1 | tee evidence/build/gradle.log
gradle_status=${PIPESTATUS[0]}
set -e

if (( gradle_status != 0 )); then
  python3 - <<'PY'
from pathlib import Path
import os
import re

path = Path("evidence/build/gradle.log")
lines = path.read_text(errors="replace").splitlines()
pattern = re.compile(
    r"(^e: |^error: |\.kt:[0-9]+:[0-9]+|FAILURE:|What went wrong|Execution failed|"
    r"Could not (?:resolve|find|determine)|Unresolved reference|Compilation error|Caused by:)",
    re.IGNORECASE,
)
selected = [line for line in lines if pattern.search(line)]
if not selected:
    selected = lines[-120:]
message = "\n".join(selected[-160:])[-24000:]
escaped = message.replace("%", "%25").replace("\r", "%0D").replace("\n", "%0A")
print(f"::error title=Gradle candidate build failed::{escaped}")
summary = os.environ.get("GITHUB_STEP_SUMMARY")
if summary:
    with open(summary, "a", encoding="utf-8") as output:
        output.write("## Gradle failure summary\n\n```text\n" + message + "\n```\n")
PY
fi
exit "$gradle_status"
