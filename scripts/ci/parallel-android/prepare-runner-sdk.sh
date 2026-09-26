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
