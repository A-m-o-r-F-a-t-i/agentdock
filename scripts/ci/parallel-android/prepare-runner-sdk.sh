#!/usr/bin/env bash
set -Eeuo pipefail

report_unhandled_error() {
  local status="$?" line command
  set +e
  if [[ "${ANDROID_SYSTEM_IMAGE_API_LEVEL:-}" == 37* ]]; then
    line="${BASH_LINENO[0]:-${LINENO}}"
    command="$BASH_COMMAND"
    command="${command//'%'/'%25'}"
    command="${command//$'\r'/'%0D'}"
    command="${command//$'\n'/'%0A'}"
    printf '::error title=API 37 SDK preparation failed::exit %s at line %s: %s\n' \
      "$status" "$line" "$command"
  fi
  exit "$status"
}
trap report_unhandled_error ERR

sdk_root="${ANDROID_SDK_ROOT:-${ANDROID_HOME:-}}"
if [[ -z "$sdk_root" || ! -d "$sdk_root" ]]; then
  for candidate in \
    /usr/local/lib/android/sdk \
    /opt/android-sdk-linux \
    "$HOME/Android/Sdk"; do
    if [[ -d "$candidate" ]]; then
      sdk_root="$candidate"
      break
    fi
  done
fi
[[ -n "$sdk_root" && -d "$sdk_root" ]]
export ANDROID_HOME="$sdk_root"
export ANDROID_SDK_ROOT="$sdk_root"
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
  local executable="$1" properties version
  properties="$(cd "$(dirname "$executable")/.." && pwd)/source.properties"
  if [[ -f "$properties" ]]; then
    version="$(sed -n 's/^[[:space:]]*Pkg.Revision[[:space:]]*=[[:space:]]*//p' "$properties" | head -1)"
  fi
  if [[ -z "${version:-}" ]]; then
    version="$("$executable" --version 2>/dev/null | awk '/^[0-9]+([.][0-9]+)*$/ { print; exit }' || true)"
  fi
  printf '%s\n' "$version"
}

api37_error() {
  local message="$1"
  printf '::error title=API 37 command-line tools::%s\n' "$message"
  printf 'API 37 command-line tools: %s\n' "$message" >&2
  return 1
}

remove_sdk_path() {
  local path="$1"
  rm -rf "$path" 2>/dev/null || sudo --non-interactive rm -rf "$path"
}

move_sdk_path() {
  local source="$1" destination="$2"
  mv "$source" "$destination" 2>/dev/null || sudo --non-interactive mv "$source" "$destination"
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
  local temporary_root archive extracted replacement backup actual_size
  temporary_root="$(mktemp -d "${RUNNER_TEMP:-/tmp}/wb07-cmdline-tools.XXXXXX")"
  archive="$temporary_root/commandlinetools.zip"
  extracted="$temporary_root/extracted"
  replacement="$extracted/cmdline-tools"
  backup="$sdk_root/cmdline-tools/.wb07-latest-backup-$$"

  if ! curl --fail --location --retry 3 --retry-delay 2 --proto '=https' --tlsv1.2 \
    "$archive_url" --output "$archive"; then
    remove_sdk_path "$temporary_root" || true
    api37_error 'download failed'
    return 1
  fi
  actual_size="$(wc -c < "$archive")"
  if [[ "$actual_size" -ne "$archive_size" ]]; then
    remove_sdk_path "$temporary_root" || true
    api37_error "archive size mismatch: expected $archive_size, got $actual_size"
    return 1
  fi
  if ! printf '%s  %s\n' "$archive_sha1" "$archive" | sha1sum --check --status; then
    remove_sdk_path "$temporary_root" || true
    api37_error 'archive checksum mismatch'
    return 1
  fi
  mkdir -p "$extracted"
  if ! unzip -q "$archive" -d "$extracted"; then
    remove_sdk_path "$temporary_root" || true
    api37_error 'archive extraction failed'
    return 1
  fi
  if [[ ! -x "$replacement/bin/sdkmanager" || ! -x "$replacement/bin/avdmanager" ]]; then
    remove_sdk_path "$temporary_root" || true
    api37_error 'archive does not contain executable sdkmanager and avdmanager'
    return 1
  fi

  local replacement_version replacement_major
  replacement_version="$(cmdline_tools_version "$replacement/bin/sdkmanager")"
  replacement_major="${replacement_version%%.*}"
  if [[ ! "$replacement_major" =~ ^[0-9]+$ ]] || (( replacement_major < required_major )); then
    remove_sdk_path "$temporary_root" || true
    api37_error "unexpected replacement version: ${replacement_version:-unknown}"
    return 1
  fi

  if ! remove_sdk_path "$backup"; then
    remove_sdk_path "$temporary_root" || true
    api37_error 'unable to clear stale command-line tools backup'
    return 1
  fi
  if [[ -e "$sdk_root/cmdline-tools/latest" || -L "$sdk_root/cmdline-tools/latest" ]]; then
    if ! move_sdk_path "$sdk_root/cmdline-tools/latest" "$backup"; then
      remove_sdk_path "$temporary_root" || true
      api37_error 'unable to back up existing command-line tools'
      return 1
    fi
  fi
  if ! move_sdk_path "$replacement" "$sdk_root/cmdline-tools/latest"; then
    if [[ -e "$backup" || -L "$backup" ]]; then
      move_sdk_path "$backup" "$sdk_root/cmdline-tools/latest" || true
    fi
    remove_sdk_path "$temporary_root" || true
    api37_error 'unable to install command-line tools into Android SDK'
    return 1
  fi
  if ! remove_sdk_path "$backup"; then
    api37_error 'replacement installed, but backup cleanup failed'
    return 1
  fi
  remove_sdk_path "$temporary_root" || true
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
resolved_version="$(cmdline_tools_version "$sdkmanager_path")"
[[ -n "$resolved_version" ]]
printf '%s\n' "$resolved_version"
printf 'platform=%s\nbuild_tools=%s\n' "$platform" "$sdk_root/build-tools/$ANDROID_BUILD_TOOLS"
