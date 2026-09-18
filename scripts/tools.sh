#!/usr/bin/env bash
#
# tools.sh — install and verify what the HOST needs to run the gates.
#
#   tools.sh            install anything missing, then verify
#   tools.sh --check    verify only; fail naming what to run, install nothing
#
# WHAT IT INSTALLS, and where. golangci-lint and govulncheck, both PINNED, into
# this repository's own tmp/bin. Not into $GOPATH/bin and not into /usr/local:
# a checkout should not change what the rest of the machine sees, and two
# repositories wanting different versions of a linter is normal. Tasks that
# need them put tmp/bin on PATH themselves (scripts/lint.sh, scripts/vulncheck.sh).
#
# WHY PINNED. `go install ...@latest` in a workflow is a check whose meaning
# changes without a commit: a linter release adds a rule and a pull request
# that touched nothing goes red. The version lives here, one line, and moving
# it is a commit someone reviewed.
#
# WHY `--check` EXISTS. A gate that silently does nothing when its tool is
# missing reports green having checked nothing — the family's "never skip"
# rule. `--check` is what the gates call first so a missing tool fails once,
# naming `chore tools`, rather than being quietly skipped.
#
# NOT HERE: the Go toolchain, a C compiler and Docker. Those are the machine's,
# not a checkout's, and each is checked where it is needed with a message
# saying how to get it.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

# The pins. Keep GOLANGCI_VERSION in step with .golangci.yml's `version: "2"`
# schema — a v1 binary cannot read a v2 config, and the error it gives does not
# say so.
GOLANGCI_VERSION="${GOLANGCI_VERSION:-2.11.4}"
GOVULNCHECK_VERSION="${GOVULNCHECK_VERSION:-v1.8.0}"

BIN="$REPO/tmp/bin"
CHECK_ONLY=0
[ "${1:-}" = "--check" ] && CHECK_ONLY=1

have() { [ -x "$BIN/$1" ]; }

version_of_golangci() {
    "$BIN/golangci-lint" version 2>/dev/null | grep -o "$GOLANGCI_VERSION" | head -1
}

install_golangci() {
    local os arch url tmp
    case "$(uname -s)" in
        Darwin) os=darwin ;;
        Linux)  os=linux ;;
        *)      echo "tools.sh: no golangci-lint build for $(uname -s)" >&2; return 1 ;;
    esac
    case "$(uname -m)" in
        x86_64|amd64)  arch=amd64 ;;
        arm64|aarch64) arch=arm64 ;;
        *) echo "tools.sh: no golangci-lint build for $(uname -m)" >&2; return 1 ;;
    esac
    url="https://github.com/golangci/golangci-lint/releases/download/v$GOLANGCI_VERSION/golangci-lint-$GOLANGCI_VERSION-$os-$arch.tar.gz"
    tmp="$(mktemp -d)"
    trap 'rm -rf "$tmp"' RETURN
    echo "tools.sh: fetching golangci-lint $GOLANGCI_VERSION ($os-$arch)"
    # The release tarball, not `go install`: the same binary the upstream
    # action uses, in seconds rather than the minutes compiling it costs.
    curl -sSfL "$url" | tar -xz -C "$tmp"
    mkdir -p "$BIN"
    mv "$tmp/golangci-lint-$GOLANGCI_VERSION-$os-$arch/golangci-lint" "$BIN/golangci-lint"
    chmod +x "$BIN/golangci-lint"
}

install_govulncheck() {
    echo "tools.sh: installing govulncheck $GOVULNCHECK_VERSION"
    mkdir -p "$BIN"
    GOBIN="$BIN" go install "golang.org/x/vuln/cmd/govulncheck@$GOVULNCHECK_VERSION"
}

missing=""

if ! have golangci-lint || [ -z "$(version_of_golangci)" ]; then
    if [ "$CHECK_ONLY" = 1 ]; then
        missing="$missing golangci-lint@$GOLANGCI_VERSION"
    else
        install_golangci
    fi
fi

if ! have govulncheck; then
    if [ "$CHECK_ONLY" = 1 ]; then
        missing="$missing govulncheck@$GOVULNCHECK_VERSION"
    else
        install_govulncheck
    fi
fi

if [ -n "$missing" ]; then
    echo "tools.sh: missing:$missing" >&2
    echo "          run 'chore tools' to install them." >&2
    exit 1
fi

echo "tools: $("$BIN/golangci-lint" version 2>/dev/null | head -1), govulncheck $GOVULNCHECK_VERSION — in tmp/bin"
