#!/usr/bin/env bash
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
api="${ANDROID_API_LEVEL:?ANDROID_API_LEVEL is required}"
evidence="$repo_root/evidence/emulator-api-$api"
mkdir -p "$evidence/screenshots" "$evidence/reports"
version_code=$((101070000 + GITHUB_RUN_NUMBER))

set +e
gradle -p "$repo_root/mobile/android" --no-daemon --stacktrace connectedDebugAndroidTest \
  -PagentdockAndroidVersionCode="$version_code" \
  -PagentdockCandidateSha="$GITHUB_SHA" \
  -PagentdockCandidateRunId="$GITHUB_RUN_ID" \
  -PagentdockCandidateRunAttempt="$GITHUB_RUN_ATTEMPT" 2>&1 | tee "$evidence/connected-test.log"
test_rc=${PIPESTATUS[0]}
set -e
# One deterministic run: never replay assertions or UI writes to turn a failure green.
adb logcat -d -v threadtime >"$evidence/logcat.txt" 2>&1 || true
adb shell getprop >"$evidence/device-properties.txt" 2>&1 || true
adb shell getprop ro.build.version.sdk >"$evidence/actual-api.txt" 2>&1 || true
adb shell pm list packages >"$evidence/packages.txt" 2>&1 || true
adb pull "/sdcard/Download/agentdock-wb07-screenshots/." \
  "$evidence/screenshots/" >"$evidence/adb-pull.txt" 2>&1 || true
for relative in reports/androidTests/connected outputs/androidTest-results/connected; do
  source="$repo_root/mobile/android/app/build/$relative"
  if [[ -d "$source" ]]; then
    destination="$evidence/reports/${relative//\//_}"
    cp -a "$source" "$destination"
  fi
done

python3 - "$evidence" "$api" "$test_rc" <<'PY'
import json, os, pathlib, re, sys, xml.etree.ElementTree as ET
root = pathlib.Path(sys.argv[1])
api = int(sys.argv[2]); rc = int(sys.argv[3])
required = {name + '.png' for name in (
    'home workspaces conversations tasks activity calls insert approvals permissions skills plugins '
    'connections install projects diagnostics settings tasks-empty tasks-error').split()}
shots = sorted(p.name for p in (root / 'screenshots').rglob('*.png') if p.stat().st_size > 0)
missing = sorted(required - set(shots))
cases = {}
parse_errors = []
for path in (root / 'reports').rglob('TEST-*.xml'):
    try:
        for case in ET.parse(path).iter('testcase'):
            key = (case.get('classname'), case.get('name'))
            cases[key] = 'failed' if case.find('failure') is not None or case.find('error') is not None else ('skipped' if case.find('skipped') is not None else 'passed')
    except Exception as error:
        parse_errors.append(f'{path.name}: {error}')
actual_api = (root / 'actual-api.txt').read_text(errors='replace').strip()
passed = sum(value == 'passed' for value in cases.values())
valid = rc == 0 and passed >= 7 and all(value == 'passed' for value in cases.values()) and not missing and not parse_errors and actual_api == str(api)
result = {
    'schema_version': 2, 'lane': 'WB07', 'source_sha': os.environ['GITHUB_SHA'],
    'run_id': os.environ['GITHUB_RUN_ID'], 'run_attempt': os.environ['GITHUB_RUN_ATTEMPT'],
    'api_level': api, 'actual_api_level': actual_api,
    'system_image_api_level': os.environ.get('ANDROID_SYSTEM_IMAGE_API_LEVEL', str(api)),
    'architecture': 'x86_64 emulator', 'connected_test_exit_code': rc,
    'attempt_exit_codes': [rc], 'tests': len(cases), 'passed': passed,
    'screenshots': shots, 'missing_screenshots': missing, 'report_errors': parse_errors,
    'evidence_gate': 'passed' if valid else 'failed',
    'fixture_scope': 'navigation, presentation, client lifecycle and write isolation',
    'physical_arm64_termux_scope': 'not exercised'
}
(root / 'result.json').write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
message = f'API {api}: tests={len(cases)}, passed={passed}, screenshots={len(shots)}, actual API={actual_api}, test exit={rc}'
if not valid:
    log = (root / 'connected-test.log').read_text(errors='replace').splitlines()
    pattern = re.compile(r'FAILURE|FAILED|Exception|Caused by:|AssertionError|WorkbenchNavigationTest|No node|INSTRUMENTATION', re.I)
    selected = [line for line in log if pattern.search(line)][-80:]
    message += '\nMissing screenshots: ' + ', '.join(missing) + '\n' + '\n'.join(parse_errors + selected)
(root / 'failure-summary.txt' if not valid else root / 'summary.txt').write_text(message + '\n')
escaped = message[-24000:].replace('%', '%25').replace('\r', '%0D').replace('\n', '%0A')
print(f'::{"notice" if valid else "error"} title=Android evidence gate::{escaped}')
with open(os.environ['GITHUB_STEP_SUMMARY'], 'a', encoding='utf-8') as output:
    output.write('\n## Android test evidence\n\n```text\n' + message[-24000:] + '\n```\n')
raise SystemExit(0 if valid else 1)
PY
