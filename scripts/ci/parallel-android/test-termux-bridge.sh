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
  local op="$1" oid="$2" rid="$3" nonce="$4" extra="${5:-{}}"
  jq -cn --arg op "$op" --arg oid "$oid" --arg rid "$rid" --arg nonce "$nonce" --argjson extra "$extra" \
    '$extra + {schema_version:1,operation:$op,operation_id:$oid,request_id:$rid,nonce:$nonce}' |
    sh "$bridge" "$op" "$oid" "$rid" "$nonce" -
}

nonce=abcdefghijklmnopqrstuvwxyzABCDE123456
out="$(request probe op_probe req_probe "$nonce")"
jq -e '.operation=="probe" and .operation_id=="op_probe" and .request_id=="req_probe" and (.status|type=="string")' <<<"$out" >/dev/null

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

echo 'termux bridge contract tests passed'
