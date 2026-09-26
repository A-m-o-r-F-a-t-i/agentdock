#!/data/data/com.termux/files/usr/bin/sh
set -eu
umask 077

# Run manually inside the official external Termux app after exporting the complete
# bridge bundle from AgentDock Workbench. It never installs a Core release by itself.
SELF_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
BRIDGE="$SELF_DIR/agentdock-workbench"
MODULE="$SELF_DIR/agentdock_workbench.py"
[ -f "$MODULE" ] || { printf 'Missing companion module\n' >&2; exit 1; }
[ -f "$BRIDGE" ] || { printf 'Missing companion file: %s\n' "$BRIDGE" >&2; exit 1; }

pkg update -y
pkg install -y proot-distro curl jq coreutils util-linux procps openssl-tool tar python
mkdir -p "$HOME/.termux/tasker" "$HOME/.agentdock-workbench/trust"
install -m 0600 "$MODULE" "$HOME/.termux/tasker/agentdock_workbench.py"
install -m 0700 "$BRIDGE" "$HOME/.termux/tasker/agentdock-workbench"
properties="$HOME/.termux/termux.properties"
touch "$properties"
if grep -q '^[[:space:]]*allow-external-apps[[:space:]]*=' "$properties"; then
  sed -i 's/^[[:space:]]*allow-external-apps[[:space:]]*=.*/allow-external-apps=true/' "$properties"
else
  printf '\nallow-external-apps=true\n' >>"$properties"
fi
termux-reload-settings 2>/dev/null || true
if ! proot-distro list 2>/dev/null | grep -Eq 'debian.*\(installed\)|debian.*installed'; then
  proot-distro install debian
fi
proot-distro login debian -- /bin/sh -lc 'apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl tar jq procps openssl && apt-get clean'
printf '%s\n' 'Termux bridge and Debian PRoot are ready.'
printf '%s\n' 'Return to AgentDock Workbench, grant RUN_COMMAND, then run Probe/Bootstrap.'
printf '%s\n' 'Core installation remains blocked until a trusted signed ARM64 release manifest/key is available.'
