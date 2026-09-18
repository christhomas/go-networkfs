#!/usr/bin/env bash
#
# lint.sh — the two checks the `lint` job makes: gofmt -s, then golangci-lint.
#
# BOTH, ALWAYS, AND NEITHER SKIPS. gofmt is the one a pre-commit hook catches
# and golangci-lint is the one that needs a pinned binary, so they used to live
# in two different places — a shell step in the workflow and an action. One
# script means `chore lint` locally and the CI job check the same two things,
# and a difference between them is a bug in one file rather than a discovery
# at merge time.
#
# The linter comes from tmp/bin, where `chore tools` pins it. A missing binary
# FAILS here naming that task; it does not quietly lint nothing.
#
# IF IT PANICS WITH "file requires newer Go version": golangci-lint is compiled
# against one Go release and type-checks with that release's go/types, so a
# host toolchain NEWER than the binary's makes it bail out on the standard
# library's own export data. CI pins Go from go.mod, so the two agree there.
# Locally, match go.mod (`mise use go@$(go mod edit -json | ...)`), or move
# GOLANGCI_VERSION in scripts/tools.sh forward.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

scripts/tools.sh --check >/dev/null

PATH="$REPO/tmp/bin:$PATH"
export PATH

# gofmt -l lists what it would change and says nothing when there is nothing,
# so the failure has to be built from its output rather than its status.
out="$(gofmt -l -s . 2>&1)"
if [ -n "$out" ]; then
    echo "gofmt -s would reformat:" >&2
    echo "$out" >&2
    exit 1
fi

golangci-lint run

echo "lint: gofmt -s clean, golangci-lint clean"
