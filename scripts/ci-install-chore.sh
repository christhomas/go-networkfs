#!/usr/bin/env bash
#
# ci-install-chore.sh — install the pinned chore, on a CI runner or in an image.
#
# EVERY CI JOB RUNS CHORE TASKS (issue #8: chore is the one entry point), so
# chore has to be there before anything else happens. Pinned by CHORE_VERSION,
# which the workflow sets in one place, and checked against the release's own
# checksums — an unpinned tool is a gate whose meaning changes without a
# commit.
#
# The release tarball, not `go install`: the runner image for the containerised
# job has a Go toolchain, but the macOS and Linux runners should not spend two
# minutes compiling a task runner, and the checksum is a stronger statement
# than "whatever the proxy served".
#
#   DEST=/usr/local/bin scripts/ci-install-chore.sh   (the runner image uses this)
set -euo pipefail

: "${CHORE_VERSION:?set CHORE_VERSION, e.g. 0.11.0}"

case "$(uname -s)" in
    Linux)  os=linux ;;
    Darwin) os=darwin ;;
    *) echo "ci-install-chore: no chore build for $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
    x86_64|amd64)  arch=x86_64 ;;
    arm64|aarch64) arch=arm64 ;;
    *) echo "ci-install-chore: no chore build for $(uname -m)" >&2; exit 1 ;;
esac

tarball="chore-${CHORE_VERSION}-${os}-${arch}.tar.gz"
base="https://github.com/antimatter-studios/chore/releases/download/v${CHORE_VERSION}"
dir="${RUNNER_TEMP:-$(mktemp -d)}/chore"
mkdir -p "$dir"

curl -fsSL -o "$dir/$tarball" "$base/$tarball"
curl -fsSL -o "$dir/checksums.txt" "$base/checksums.txt"

# macOS has shasum and no sha256sum; Linux runners have both. Checking the
# checksum is not optional, so the tool that does it is chosen rather than the
# check being skipped where one is missing.
if command -v sha256sum >/dev/null 2>&1; then
    (cd "$dir" && grep " ${tarball}\$" checksums.txt | sha256sum -c -)
else
    (cd "$dir" && grep " ${tarball}\$" checksums.txt | shasum -a 256 -c -)
fi

tar -xzf "$dir/$tarball" -C "$dir"
# No -perm test: BSD find and GNU find disagree about symbolic modes, and the
# install below sets the mode anyway.
bin="$(find "$dir" -type f -name chore | head -1)"
[ -n "$bin" ] || { echo "ci-install-chore: no chore binary in $tarball" >&2; exit 1; }

DEST="${DEST:-$HOME/.local/bin}"
install -d -m 0755 "$DEST"
install -m 0755 "$bin" "$DEST/chore"
[ -z "${GITHUB_PATH:-}" ] || echo "$DEST" >> "$GITHUB_PATH"
"$DEST/chore" --version | head -1
