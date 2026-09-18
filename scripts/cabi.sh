#!/usr/bin/env bash
#
# cabi.sh — the C ABI harnesses: build them, run them, fold their coverage in.
#
#   cabi.sh drivers    the eight per-driver archives, each linked into its own
#                      C program and run
#   cabi.sh unified    libnetworkfs.a, with coverage, against a real server
#   cabi.sh all        both of the above, and no coverage profile touched
#   cabi.sh cover      both of the above, merged into the coverage profile
#
# THE SERVERS MUST ALREADY BE UP (scripts/servers.sh up). This script starts
# nothing: it is called both from a task that owns the lifecycle and from
# inside the runner container, where the servers are somebody else's.
#
# WHY A C PROGRAM AND NOT A GO TEST. The archive is what ships, and three of
# its entry points take a ByteSlice or a size_t. A Go test file may not import
# "C", so it cannot name either type — the only caller that can is a C program
# linking the built archive, which is also exactly how a consumer uses it.
#
# WHY THE PER-DRIVER ARCHIVES RUN WITHOUT COVERAGE. A c-archive has no Go main,
# so the runtime never writes its counters on the way out. The unified archive
# is built with the `coverage` tag, which adds an export the harness calls to
# flush them, and with -covermode=atomic, which is what WriteCountersDir
# requires. The per-driver archives have no such export: what they prove is
# that the archive links and the ABI behaves, which does not depend on
# measuring it. Each still gets its own coverage directory — they are separate
# programs with separate metadata, and mixing their counters in one directory
# is not something covdata is asked to untangle.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

# shellcheck source=scripts/test-env.sh
. "$REPO/scripts/test-env.sh"

GO="${GO:-go}"

die() { echo "cabi.sh: $*" >&2; exit 1; }

# The config each harness mounts with. A driver with no server here gets an
# empty config and the harness only reaches its failure paths — see issue #6,
# which is about making that visible rather than silent.
config_for() {   # $1 = driver
    case "$1" in
        smb)      echo "{\"host\":\"$SMB_ADDR\",\"port\":\"$SMB_PORT\",\"share\":\"$SMB_SHARE\",\"user\":\"$SMB_USER\",\"pass\":\"$SMB_PASS\"}" ;;
        s3)       echo "{\"endpoint\":\"$S3_ADDR:$S3_PORT\",\"bucket\":\"$S3_BUCKET\",\"access_key_id\":\"$S3_KEY\",\"secret_access_key\":\"$S3_SECRET\",\"secure\":\"false\",\"use_path_style\":\"true\"}" ;;
        ftp)      echo "{\"host\":\"$FTP_ADDR\",\"port\":\"$FTP_PORT\",\"user\":\"$FTP_USER\",\"pass\":\"$FTP_PASS\"}" ;;
        sftp)     echo "{\"host\":\"$SFTP_ADDR\",\"port\":\"$SFTP_PORT\",\"user\":\"$SFTP_USER\",\"pass\":\"$SFTP_PASS\",\"root\":\"/upload\"}" ;;
        webdav)   echo "{\"url\":\"http://$DAV_ADDR:$DAV_PORT\",\"user\":\"$DAV_USER\",\"pass\":\"$DAV_PASS\"}" ;;
        dropbox)  echo "{\"access_token\":\"mock\",\"api_base_url\":\"http://$MOCK_ADDR:$MOCK_PORT/dropbox\"}" ;;
        gdrive)   echo "{\"client_id\":\"c\",\"client_secret\":\"s\",\"refresh_token\":\"r\",\"api_base_url\":\"http://$MOCK_ADDR:$MOCK_PORT/gdrive\"}" ;;
        onedrive) echo "{\"client_id\":\"c\",\"refresh_token\":\"r\",\"api_base_url\":\"http://$MOCK_ADDR:$MOCK_PORT/onedrive\"}" ;;
        *)        echo "" ;;
    esac
}

cabi_drivers() {
    rm -rf "$CABI_DIR/drivers"
    mkdir -p "$CABI_DIR/drivers"
    for d in $DRIVERS; do
        [ -f "test/cabi/test_$d.c" ] || die "no harness test/cabi/test_$d.c for driver '$d'"
        mkdir -p "$CABI_DIR/drivers/cov-$d"
        $GO build -cover -covermode=atomic -tags coverage -buildmode=c-archive \
            -o "$CABI_DIR/drivers/lib$d.a" "./$d/cmd/$d"
        # shellcheck disable=SC2086  # CABI_LDLIBS is a word list on purpose
        $CC -DNETWORKFS_COVERAGE -o "$CABI_DIR/drivers/test_$d" "test/cabi/test_$d.c" \
            -I"$CABI_DIR/drivers" "$CABI_DIR/drivers/lib$d.a" $CABI_LDLIBS
        CABI_CONFIG="$(config_for "$d")" GOCOVERDIR="$CABI_DIR/drivers/cov-$d" \
            "$CABI_DIR/drivers/test_$d"
        $GO tool covdata textfmt -i="$CABI_DIR/drivers/cov-$d" \
            -o "$CABI_DIR/drivers/$d.out"
    done
}

cabi_unified() {
    rm -rf "$CABI_DIR/unified" "$CABI_COVER"
    mkdir -p "$CABI_DIR/unified" "$CABI_COVER"
    $GO build -cover -covermode=atomic -tags coverage \
        -buildmode=c-archive -o "$CABI_DIR/unified/libnetworkfs.a" ./cmd/networkfs
    # shellcheck disable=SC2086
    $CC -DNETWORKFS_COVERAGE -o "$CABI_DIR/unified/test_networkfs" \
        test/cabi/test_networkfs.c -I"$CABI_DIR/unified" \
        "$CABI_DIR/unified/libnetworkfs.a" $CABI_LDLIBS
    GOCOVERDIR="$CABI_COVER" \
        SMB_HOST="$SMB_ADDR" SMB_PORT="$SMB_PORT" SMB_SHARE="$SMB_SHARE" \
        SMB_USER="$SMB_USER" SMB_PASS="$SMB_PASS" \
        "$CABI_DIR/unified/test_networkfs"
}

# The C ABI is the only caller of some of this code, so leaving it out of the
# profile understates what is actually tested. merge-coverage.sh takes the
# higher count per block rather than appending, which is what stops the same
# block being listed twice and the totals being overstated.
cabi_cover() {
    [ -f "$COVERAGE" ] || die "$COVERAGE does not exist — run the Go suite first."
    cabi_drivers
    cabi_unified
    $GO tool covdata textfmt -i="$CABI_COVER" -o "$CABI_DIR/unified.out"
    test/cabi/merge-coverage.sh "$CABI_DIR/merged.out" "$COVERAGE" \
        "$CABI_DIR/unified.out" "$CABI_DIR"/drivers/*.out
    mv "$CABI_DIR/merged.out" "$COVERAGE"
}

case "${1:-}" in
    drivers) cabi_drivers ;;
    unified) cabi_unified ;;
    all)     cabi_drivers; cabi_unified ;;
    cover)   cabi_cover ;;
    ""|-h|--help)
        sed -n '3,10p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
        if [ -z "${1:-}" ]; then exit 2; fi
        ;;
    *) die "unknown command '$1' (drivers, unified, all, cover)" ;;
esac
