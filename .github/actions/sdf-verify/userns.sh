#!/usr/bin/env bash
# sdf verify runs each check in its own user and pid namespace, and refuses
# to run without one (ISOLATION_UNAVAILABLE). Ubuntu 23.10 and later, which
# includes the ubuntu-24.04 / ubuntu-latest runners, block unprivileged user
# namespaces through AppArmor; hosted runners have passwordless sudo, so lift
# that here, before any pull request code is on disk. If this fails, the
# verify step fails with ISOLATION_UNAVAILABLE rather than running unisolated.
set -uo pipefail
f=/proc/sys/kernel/apparmor_restrict_unprivileged_userns
if [ -r "$f" ] && [ "$(cat "$f")" != 0 ]; then
  sudo -n sysctl -q -w kernel.apparmor_restrict_unprivileged_userns=0 ||
    echo "::warning title=sdf verify::could not allow unprivileged user namespaces; sdf verify will report ISOLATION_UNAVAILABLE"
fi
exit 0
