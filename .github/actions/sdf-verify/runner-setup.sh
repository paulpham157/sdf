#!/usr/bin/env bash
# Prepares the runner for sdf verify, before any pull request code is on
# disk. By default (SDF_CONFIGURE_RUNNER=auto) it changes nothing unless
# RUNNER_ENVIRONMENT is github-hosted: an ephemeral VM, discarded after the
# job, with passwordless sudo that the pull request's code could use
# anyway. SDF_CONFIGURE_RUNNER=true runs it on any runner (it changes the
# host); false skips it.
#
#  1. sdf verify runs each check in its own user, pid, mount and network
#     namespaces and refuses to run without them (ISOLATION_UNAVAILABLE).
#     Ubuntu 23.10+ (ubuntu-24.04, ubuntu-latest) blocks unprivileged user
#     namespaces through AppArmor; lift that.
#  2. Stop docker and containerd and lock their sockets: the runner user is
#     in the docker group, and a container started through them would run
#     outside the check's namespaces. (Checks already cannot see /run or
#     reach the host network; this is the second lock.) Later steps in the
#     same job cannot use docker, so run sdf verify in its own job.
set -uo pipefail

note() { echo "::notice title=sdf verify::$1"; }

case "${SDF_CONFIGURE_RUNNER:-auto}" in
  auto)
    if [ "${RUNNER_ENVIRONMENT:-}" != github-hosted ]; then
      note "not a GitHub-hosted runner: runner left as is (configure-runner: true to change it)"
      exit 0
    fi
    ;;
  true) ;;
  false)
    note "configure-runner is false: runner left as is"
    exit 0
    ;;
  *)
    echo "::error title=sdf verify::configure-runner must be auto, true or false"
    exit 1
    ;;
esac

f=/proc/sys/kernel/apparmor_restrict_unprivileged_userns
if [ -r "$f" ] && [ "$(cat "$f")" != 0 ]; then
  if sudo -n sysctl -q -w kernel.apparmor_restrict_unprivileged_userns=0; then
    note "set kernel.apparmor_restrict_unprivileged_userns=0 so checks can run in user namespaces"
  else
    echo "::warning title=sdf verify::could not allow unprivileged user namespaces; sdf verify will report ISOLATION_UNAVAILABLE"
  fi
fi

stopped=""
if command -v systemctl >/dev/null 2>&1; then
  for unit in docker.socket docker.service containerd.service; do
    if systemctl is-active --quiet "$unit" && sudo -n systemctl stop "$unit"; then
      stopped="$stopped $unit"
    fi
  done
fi
for sock in /var/run/docker.sock /run/docker.sock /run/containerd/containerd.sock; do
  if [ -S "$sock" ] && sudo -n chmod 000 "$sock"; then
    stopped="$stopped $sock(chmod 000)"
  fi
done
if [ -n "$stopped" ]; then
  note "stopped or locked:$stopped; later steps in this job cannot use docker"
fi
exit 0
