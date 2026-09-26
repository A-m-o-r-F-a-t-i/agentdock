#!/usr/bin/env bash
set -euo pipefail

sdk_root="${ANDROID_SDK_ROOT:-${ANDROID_HOME:-}}"
[[ -n "$sdk_root" && -d "$sdk_root" ]]
sdkmanager_path="$(command -v sdkmanager || true)"
if [[ -z "$sdkmanager_path" ]]; then
  for candidate in \
    "$sdk_root/cmdline-tools/latest/bin/sdkmanager" \
    "$sdk_root/cmdline-tools/bin/sdkmanager" \
    "$sdk_root/tools/bin/sdkmanager"; do
    if [[ -x "$candidate" ]]; then
      sdkmanager_path="$candidate"
      break
    fi
  done
fi
[[ -x "$sdkmanager_path" ]]

cmdline_tools_version() {
  "$1" --version 2>/dev/null | awk '/^[0-9]+([.][0-9]+)*$/ { print; exit }'
}

install_api37_cmdline_tools() {
  local required_major=22
  local current_version current_major
  current_version="$(cmdline_tools_version "$sdkmanager_path")"
  current_major="${current_version%%.*}"
  if [[ "$current_major" =~ ^[0-9]+$ ]] && (( current_major >= required_major )); then
    printf 'cmdline_tools=%s (already compatible with API 37)\n' "$current_version"
    return
  fi

  local archive_url='https://dl.google.com/android/repository/commandlinetools-linux-15859902_latest.zip'
  local archive_sha1='040d3996a65543d22ec4bf73e4c37aa37a8d4af4'
  local archive_size=181833628
  local temporary_root archive extracted replacement backup
  temporary_root="$(mktemp -d "${RUNNER_TEMP:-/tmp}/wb07-cmdline-tools.XXXXXX")"
  archive="$temporary_root/commandlinetools.zip"
  extracted="$temporary_root/extracted"
  replacement="$extracted/cmdline-tools"
  backup="$sdk_root/cmdline-tools/.wb07-latest-backup-$$"

  curl --fail --location --retry 3 --retry-delay 2 --proto '=https' --tlsv1.2 \
    "$archive_url" --output "$archive"
  [[ "$(wc -c < "$archive")" -eq "$archive_size" ]]
  printf '%s  %s\n' "$archive_sha1" "$archive" | sha1sum --check --status
  mkdir -p "$extracted"
  unzip -q "$archive" -d "$extracted"
  [[ -x "$replacement/bin/sdkmanager" && -x "$replacement/bin/avdmanager" ]]

  local replacement_version replacement_major
  replacement_version="$(cmdline_tools_version "$replacement/bin/sdkmanager")"
  replacement_major="${replacement_version%%.*}"
  [[ "$replacement_major" =~ ^[0-9]+$ ]]
  (( replacement_major >= required_major ))

  rm -rf "$backup"
  if [[ -e "$sdk_root/cmdline-tools/latest" || -L "$sdk_root/cmdline-tools/latest" ]]; then
    mv "$sdk_root/cmdline-tools/latest" "$backup"
  fi
  if ! mv "$replacement" "$sdk_root/cmdline-tools/latest"; then
    [[ ! -e "$backup" && ! -L "$backup" ]] || mv "$backup" "$sdk_root/cmdline-tools/latest"
    return 1
  fi
  rm -rf "$backup" "$temporary_root"
  sdkmanager_path="$sdk_root/cmdline-tools/latest/bin/sdkmanager"
  printf 'cmdline_tools=%s (installed for API 37 feature-drop images)\n' "$replacement_version"
}

if [[ "${ANDROID_SYSTEM_IMAGE_API_LEVEL:-}" == 37* ]]; then
  install_api37_cmdline_tools
fi

platform="$sdk_root/platforms/android-${ANDROID_COMPILE_SDK:?ANDROID_COMPILE_SDK is required}"
if [[ ! -d "$platform" ]]; then
  candidate="$sdk_root/platforms/android-${ANDROID_COMPILE_SDK}.0"
  if [[ ! -d "$candidate" ]]; then
    candidate="$(find "$sdk_root/platforms" -maxdepth 1 -type d \
      -name "android-${ANDROID_COMPILE_SDK}.*" ! -name '*beta*' | sort -V | head -1)"
  fi
  [[ -n "$candidate" && -d "$candidate" ]]
  relative="$(basename "$candidate")"
  ln -s "$relative" "$platform" 2>/dev/null || sudo ln -s "$relative" "$platform"
fi

[[ -d "$platform" ]]
[[ -d "$sdk_root/build-tools/${ANDROID_BUILD_TOOLS:?ANDROID_BUILD_TOOLS is required}" ]]
[[ -x "$sdk_root/platform-tools/adb" ]]
if [[ -n "${GITHUB_ENV:-}" ]]; then
  printf 'ANDROID_HOME=%s\nANDROID_SDK_ROOT=%s\n' "$sdk_root" "$sdk_root" >> "$GITHUB_ENV"
fi
if [[ -n "${GITHUB_PATH:-}" ]]; then
  printf '%s\n' "$(dirname "$sdkmanager_path")" >> "$GITHUB_PATH"
fi
"$sdkmanager_path" --version
printf 'platform=%s\nbuild_tools=%s\n' "$platform" "$sdk_root/build-tools/$ANDROID_BUILD_TOOLS"
