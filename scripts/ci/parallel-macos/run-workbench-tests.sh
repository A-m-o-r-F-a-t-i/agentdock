#!/bin/zsh
set -euo pipefail

ROOT_DIR="${0:A:h:h:h:h}"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/agentdock-workbench-macos.XXXXXX")"
trap 'rm -rf "$TMP_ROOT"' EXIT

print -- "runner_arch=$(uname -m)"
print -- "runner_os=$(sw_vers -productVersion)"
xcodebuild -version
swiftc --version

xcrun swiftc \
  -swift-version 5 \
  -parse-as-library \
  "$ROOT_DIR/desktop/macos/AgentDockApp/Sources/WorkbenchJSON.swift" \
  "$ROOT_DIR/desktop/macos/AgentDockApp/Sources/WorkbenchModels.swift" \
  "$ROOT_DIR/desktop/macos/AgentDockApp/Tests/WorkbenchModelTests.swift" \
  -o "$TMP_ROOT/workbench-model-tests"
"$TMP_ROOT/workbench-model-tests"

xcrun swiftc \
  -swift-version 5 \
  -parse-as-library \
  "$ROOT_DIR/desktop/macos/AgentDockApp/Sources/WorkbenchJSON.swift" \
  "$ROOT_DIR/desktop/macos/AgentDockApp/Sources/WorkbenchClientError.swift" \
  "$ROOT_DIR/desktop/macos/AgentDockApp/Sources/WorkbenchSSE.swift" \
  "$ROOT_DIR/desktop/macos/AgentDockApp/Tests/WorkbenchSSETests.swift" \
  -o "$TMP_ROOT/workbench-sse-tests"
"$TMP_ROOT/workbench-sse-tests"

SDK_PATH="$(xcrun --sdk macosx --show-sdk-path)"
case "$(uname -m)" in
  arm64) SWIFT_ARCH=arm64 ;;
  x86_64) SWIFT_ARCH=x86_64 ;;
  *) print -u2 -- "unsupported macOS runner architecture"; exit 1 ;;
esac
xcrun swiftc \
  -swift-version 5 \
  -typecheck \
  -target "$SWIFT_ARCH-apple-macosx13.0" \
  -sdk "$SDK_PATH" \
  "$ROOT_DIR/desktop/macos/AgentDockApp/Sources"/*.swift

python3 - "$ROOT_DIR" <<'PY'
from pathlib import Path
import sys
root = Path(sys.argv[1])
sources = (root / "desktop/macos/AgentDockApp/Sources")
text = "\n".join(path.read_text() for path in sources.glob("Workbench*.swift"))
required = [
    'placeholderString = "搜索对话"',
    'serverNow.timeIntervalSince(lastActivityAt) < 120',
    'serverNow.timeIntervalSince(lastToolCallAt) < 180',
    'Date().addingTimeInterval(30)',
    'maximumResponseBytes = 8 * 1024 * 1024',
    'Last-Event-ID',
    '/internal/runtime/execution/sidebar',
    '/internal/runtime/permissions/effective',
    '/insertions',
]
for value in required:
    if value not in text:
        raise SystemExit(f"missing Workbench contract marker: {value}")
for forbidden in ("WKWebView", "Electron", "WebView2", "task.json", "activity.jsonl"):
    if forbidden in text:
        raise SystemExit(f"forbidden duplicated/non-native implementation marker: {forbidden}")
print("workbench source contract checks passed")
PY

if [[ "${RUN_EXISTING_MACOS_TESTS:-0}" == "1" ]]; then
  "$ROOT_DIR/scripts/test/test-macos-app.sh"
fi
