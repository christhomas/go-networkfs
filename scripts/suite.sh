#!/usr/bin/env bash
#
# suite.sh — the whole suite against servers that are ALREADY UP, with one
# coverage profile over the lot.
#
# It starts nothing and tears nothing down. That is the point: the same script
# runs inside the containerised runner (where the servers belong to the host)
# and from `chore test:integration` (where the task owns them), so there is one
# definition of "the full run" rather than one per entry point.
#
# THIS IS THE NUMBER THAT MATTERS. Without the integration build tags most of
# each driver is never executed, and a plain `go test ./...` reports those
# paths as untested — so the coverage figure from this run is the only honest
# one this repository produces.
#
# test/... is excluded from the measurement. The mock API server lives there
# and is scaffolding, not product: counting it would mean the coverage figure
# falls every time the test harness grows, which is the wrong incentive. It is
# excluded twice over — from -coverpkg, so its own statements are never
# instrumented, and from the profile afterwards, because the C harness's
# profiles are merged in and carry blocks -coverpkg never saw.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

# shellcheck source=scripts/test-env.sh
. "$REPO/scripts/test-env.sh"

GO="${GO:-go}"

pkgs="$($GO list ./... | grep -v '/test/' | paste -sd, -)"

$GO test -count=1 -tags="$INTEGRATION_TAGS" \
    -coverpkg="$pkgs" \
    -covermode=atomic -coverprofile="$COVERAGE" ./...

# The C harnesses need the servers still up: the SMB and S3 archives mount
# against them, which is the only way to reach the success paths.
scripts/cabi.sh cover

# Drop the harness's own packages from the merged profile.
grep -v '/test/' "$COVERAGE" > "$COVERAGE.tmp" && mv "$COVERAGE.tmp" "$COVERAGE"

$GO tool cover -func="$COVERAGE" | tail -1
