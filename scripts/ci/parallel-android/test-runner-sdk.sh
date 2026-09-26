#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
script="$repo_root/scripts/ci/parallel-android/prepare-runner-sdk.sh"
temporary_root="$(mktemp -d)"
trap 'rm -rf "$temporary_root"' EXIT

make_sdk() {
  local sdk="$1" version="$2"
  mkdir -p \
    "$sdk/cmdline-tools/latest/bin" \
    "$sdk/platforms/android-37" \
    "$sdk/build-tools/37.0.0" \
    "$sdk/platform-tools"
  cat > "$sdk/cmdline-tools/latest/bin/sdkmanager" <<EOF
#!/usr/bin/env bash
printf '%s\\n' '$version'
EOF
  printf 'Pkg.Revision=%s\n' "$version" > "$sdk/cmdline-tools/latest/source.properties"
  cat > "$sdk/platform-tools/adb" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
  chmod +x "$sdk/cmdline-tools/latest/bin/sdkmanager" "$sdk/platform-tools/adb"
}

make_base_tools() {
  local directory="$1"
  mkdir -p "$directory"
  cat > "$directory/curl" <<'EOF'
#!/usr/bin/env bash
printf 'curl must not be called for this case\n' >&2
exit 99
EOF
  chmod +x "$directory/curl"
}

run_prepare() {
  local sdk="$1" image_api="$2" tools="$3" output="$4"
  ANDROID_SDK_ROOT="$sdk" \
  ANDROID_COMPILE_SDK=37 \
  ANDROID_BUILD_TOOLS=37.0.0 \
  ANDROID_SYSTEM_IMAGE_API_LEVEL="$image_api" \
  RUNNER_TEMP="$temporary_root/runner-temp" \
  PATH="$tools:/usr/bin:/bin" \
  bash "$script" > "$output" 2>&1
}

mkdir -p "$temporary_root/runner-temp"

sdk35="$temporary_root/sdk35"
tools35="$temporary_root/tools35"
make_sdk "$sdk35" 20.0
make_base_tools "$tools35"
run_prepare "$sdk35" 35 "$tools35" "$temporary_root/api35.txt"
grep -Fx '20.0' "$temporary_root/api35.txt" >/dev/null

sdk37_compatible="$temporary_root/sdk37-compatible"
tools37_compatible="$temporary_root/tools37-compatible"
make_sdk "$sdk37_compatible" 22.0
make_base_tools "$tools37_compatible"
run_prepare "$sdk37_compatible" 37.0 "$tools37_compatible" "$temporary_root/api37-compatible.txt"
grep -F 'cmdline_tools=22.0 (already compatible with API 37)' "$temporary_root/api37-compatible.txt" >/dev/null

make_install_tools() {
  local directory="$1" checksum_result="$2"
  mkdir -p "$directory"
  cat > "$directory/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
output=''
while (($#)); do
  if [[ "$1" == '--output' ]]; then
    output="$2"
    shift 2
  else
    shift
  fi
done
[[ -n "$output" ]]
truncate -s 181833628 "$output"
EOF
  cat > "$directory/sha1sum" <<EOF
#!/usr/bin/env bash
exit $checksum_result
EOF
  cat > "$directory/unzip" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
destination=''
while (($#)); do
  if [[ "$1" == '-d' ]]; then
    destination="$2"
    shift 2
  else
    shift
  fi
done
mkdir -p "$destination/cmdline-tools/bin"
cat > "$destination/cmdline-tools/bin/sdkmanager" <<'INNER'
#!/usr/bin/env bash
printf '22.0\n'
INNER
cat > "$destination/cmdline-tools/bin/avdmanager" <<'INNER'
#!/usr/bin/env bash
exit 0
INNER
printf 'Pkg.Revision=22.0\n' > "$destination/cmdline-tools/source.properties"
chmod +x "$destination/cmdline-tools/bin/sdkmanager" "$destination/cmdline-tools/bin/avdmanager"
EOF
  cat > "$directory/mv" <<'EOF'
#!/usr/bin/env bash
exit 13
EOF
  cat > "$directory/sudo" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
[[ "${1:-}" != '--non-interactive' ]] || shift
command="$1"
shift
case "$command" in
  mv) exec /bin/mv "$@" ;;
  rm) exec /bin/rm "$@" ;;
  *) exit 64 ;;
esac
EOF
  chmod +x "$directory"/*
}

sdk37_upgrade="$temporary_root/sdk37-upgrade"
tools37_upgrade="$temporary_root/tools37-upgrade"
make_sdk "$sdk37_upgrade" 20.0
make_install_tools "$tools37_upgrade" 0
run_prepare "$sdk37_upgrade" 37.0 "$tools37_upgrade" "$temporary_root/api37-upgrade.txt"
test "$("$sdk37_upgrade/cmdline-tools/latest/bin/sdkmanager" --version)" = '22.0'
test -z "$(find "$sdk37_upgrade/cmdline-tools" -maxdepth 1 -name '.wb07-latest-backup-*' -print -quit)"
grep -F 'cmdline_tools=22.0 (installed for API 37 feature-drop images)' "$temporary_root/api37-upgrade.txt" >/dev/null

sdk37_bad_checksum="$temporary_root/sdk37-bad-checksum"
tools37_bad_checksum="$temporary_root/tools37-bad-checksum"
make_sdk "$sdk37_bad_checksum" 20.0
make_install_tools "$tools37_bad_checksum" 1
set +e
run_prepare "$sdk37_bad_checksum" 37.0 "$tools37_bad_checksum" "$temporary_root/api37-bad-checksum.txt"
status=$?
set -e
test "$status" -ne 0
test "$("$sdk37_bad_checksum/cmdline-tools/latest/bin/sdkmanager" --version)" = '20.0'
grep -F '::error title=API 37 command-line tools::archive checksum mismatch' "$temporary_root/api37-bad-checksum.txt" >/dev/null

echo 'runner SDK preparation contract tests passed'
