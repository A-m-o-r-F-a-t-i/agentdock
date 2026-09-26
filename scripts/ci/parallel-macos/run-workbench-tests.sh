#!/bin/zsh
set -euo pipefail

ROOT_DIR="${0:A:h:h:h:h}"
TMP_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/agentdock-workbench-macos.XXXXXX")"
trap 'rm -rf "$TMP_ROOT"' EXIT
export WB06_EVIDENCE_DIR="$ROOT_DIR/evidence/$(uname -m)"
mkdir -p "$WB06_EVIDENCE_DIR"
{
  print -- "source_sha=$(git -C "$ROOT_DIR" rev-parse HEAD)"
  print -- "runner_arch=$(uname -m)"
  print -- "runner_os=$(sw_vers -productVersion)"
  print -- "run_id=${GITHUB_RUN_ID:-local} attempt=${GITHUB_RUN_ATTEMPT:-1}"
  xcodebuild -version
  swiftc --version
  go version
} | tee "$WB06_EVIDENCE_DIR/toolchain.txt"

xcrun swiftc -swift-version 5 -parse-as-library \
  "$ROOT_DIR/desktop/macos/AgentDockApp/Sources/WorkbenchJSON.swift" \
  "$ROOT_DIR/desktop/macos/AgentDockApp/Sources/WorkbenchModels.swift" \
  "$ROOT_DIR/desktop/macos/AgentDockApp/Tests/WorkbenchModelTests.swift" \
  -o "$TMP_ROOT/workbench-model-tests"
"$TMP_ROOT/workbench-model-tests" | tee "$WB06_EVIDENCE_DIR/model-tests.txt"
xcrun swiftc -swift-version 5 -parse-as-library \
  "$ROOT_DIR/desktop/macos/AgentDockApp/Sources/WorkbenchJSON.swift" \
  "$ROOT_DIR/desktop/macos/AgentDockApp/Sources/WorkbenchClientError.swift" \
  "$ROOT_DIR/desktop/macos/AgentDockApp/Sources/WorkbenchSSE.swift" \
  "$ROOT_DIR/desktop/macos/AgentDockApp/Tests/WorkbenchSSETests.swift" \
  -o "$TMP_ROOT/workbench-sse-tests"
"$TMP_ROOT/workbench-sse-tests" | tee "$WB06_EVIDENCE_DIR/sse-tests.txt"

swift test --package-path "$ROOT_DIR/desktop/macos/AgentDockApp" \
  --scratch-path "$TMP_ROOT/swiftpm" \
  --xunit-output "$WB06_EVIDENCE_DIR/xctest.xml" \
  2>&1 | tee "$WB06_EVIDENCE_DIR/xctest.log"

SDK_PATH="$(xcrun --sdk macosx --show-sdk-path)"
xcrun swiftc -swift-version 5 -typecheck -target "$(uname -m)-apple-macosx13.0" \
  -sdk "$SDK_PATH" "$ROOT_DIR/desktop/macos/AgentDockApp/Sources"/*.swift \
  2>&1 | tee "$WB06_EVIDENCE_DIR/application-typecheck.log"

python3 - "$ROOT_DIR" <<'PY'
from pathlib import Path
import sys
root = Path(sys.argv[1])
sources = root / 'desktop/macos/AgentDockApp/Sources'
text = '\n'.join(p.read_text() for p in sources.glob('Workbench*.swift'))
for value in ['placeholderString = "搜索对话"', 'Last-Event-ID',
              'custom_permissions_enabled', 'receipt_type', 'limit_chars',
              'bufferingOldest(256)', 'maximumCalls = 1000']:
    if value not in text:
        raise SystemExit(f'missing Workbench contract: {value}')
for forbidden in ('WKWebView', 'Electron', 'WebView2', 'task.json', 'activity.jsonl'):
    if forbidden in text:
        raise SystemExit(f'non-native/duplicated state marker: {forbidden}')
print('Workbench source contracts passed')
PY

# Real shared Core HTTP/MCP semantics on the same SHA. These baseline domain
# tests do not represent unmerged WB01/02/03/04 integration or a live ChatGPT host.
(cd "$ROOT_DIR" && go test ./internal/httpx \
  -run 'TestExecutionHTTPRealMCPConversationApprovalAndIsolation|TestExecutionSDKMissingMetadataRemainsUnattributed|TestExecutionHTTPViewDoesNotBindAndExplicitCurrentTaskDoes' \
  -count=1 -timeout=240s -v) 2>&1 | tee "$WB06_EVIDENCE_DIR/real-core-http-mcp.log"

if [[ "${RUN_EXISTING_MACOS_TESTS:-0}" == "1" ]]; then
  "$ROOT_DIR/scripts/test/test-macos-app.sh" 2>&1 | tee "$WB06_EVIDENCE_DIR/existing-macos-regression.log"
fi
