#!/usr/bin/env bash
#
# with-servers.sh — start the named test servers, run a command against them,
# and take them down however the command ends.
#
#   with-servers.sh NAME [NAME...] -- COMMAND [ARG...]
#   with-servers.sh all -- COMMAND [ARG...]
#
# WHY THIS IS A SCRIPT AND NOT THREE LINES IN A TASK. A task written as
# "up; test; down" does not run `down` when the test fails — chore stops at the
# failing command, as make did, which is why every one of the Makefile's test
# targets carried its own `status=$?; make X-down; exit $status` dance. One
# trap, in one place, and the command's own status is what comes out: a run
# that is interrupted, fails, or passes leaves the same clean machine.
#
# IT ALSO EXPORTS THE ENVIRONMENT the suite needs to reach what it started, so
# the addresses are set once, next to the servers themselves, rather than
# repeated in every task that runs a tagged test.
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO" || exit 1

names=""
while [ $# -gt 0 ]; do
    case "$1" in
        --) shift; break ;;
        *)  names="$names $1"; shift ;;
    esac
done

[ -n "${names// /}" ] || { echo "with-servers.sh: no server named (use 'all' for every one)" >&2; exit 2; }
[ $# -gt 0 ] || { echo "with-servers.sh: no command (use -- COMMAND ...)" >&2; exit 2; }

case "$names" in
    *" all"*) names="" ;;   # empty means "every server" to servers.sh
esac

# shellcheck source=scripts/test-env.sh
. "$REPO/scripts/test-env.sh"

# What a tagged Go test and the C harnesses read. These names belong to the
# tests, not to the rig, which is why they are mapped here rather than being
# what test-env.sh calls them.
export SMB_HOST="$SMB_ADDR" SMB_PORT SMB_SHARE SMB_USER SMB_PASS
export S3_ENDPOINT="$S3_ADDR:$S3_PORT" S3_BUCKET S3_SECURE=false
export S3_ACCESS_KEY="$S3_KEY" S3_SECRET_KEY="$S3_SECRET"

# shellcheck disable=SC2086  # the words are the server names
cleanup() { scripts/servers.sh down $names >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM

# shellcheck disable=SC2086
scripts/servers.sh up $names || exit $?

"$@"
