#!/usr/bin/env bash
#
# servers.sh — the test servers, as containers, with one lifecycle.
#
#   servers.sh up   [NAME...]   start them (default: all), wait until each is
#                               answering, and print one line per server
#   servers.sh down [NAME...]   remove them (default: all, and the network)
#   servers.sh status           what is running
#   servers.sh env              the environment a suite needs to reach them
#
#   NAME is one of: samba s3 ftp sftp webdav mockapi
#
# WHY EVERY DRIVER THAT CAN HAVE A SERVER GETS ONE. Without a server a driver's
# integration tests are skipped and its C harness only ever reaches the failure
# paths, so the coverage number reports the unit tests alone and a broken mount
# path looks exactly like a working one. Nothing here is installed on the
# machine: it is Docker or it is nothing.
#
# WHY THE ADDRESSES ARE VARIABLES. On a developer machine the servers are
# reached on localhost through published ports. Inside the containerised runner
# they are reached by container name on a shared Docker network, on the
# INTERNAL port. Same servers, two addresses, so every address is a variable
# with a host-shaped default and the runner overrides it (scripts/docker-suite.sh).
#
# QUIET. `docker build` is the loud part, so it is built with -q and its output
# kept for --verbose (FLTH_VERBOSE=1). Container ids go nowhere: a line naming
# the server and where it is listening is the useful verdict.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

ALL_SERVERS="samba s3 ftp sftp webdav mockapi"

# Every address, port, credential and image: scripts/test-env.sh, which the
# suite and the C harness read too, so a port cannot be 4445 here and 445
# there.
# shellcheck source=scripts/test-env.sh
. "$REPO/scripts/test-env.sh"

VERBOSE="${FLTH_VERBOSE:-0}"

die() { echo "servers.sh: $*" >&2; exit 1; }

need_docker() {
    command -v docker >/dev/null 2>&1 \
        || die "docker is not installed — the test servers are containers, there is no host fallback."
    docker info >/dev/null 2>&1 \
        || die "docker is installed but not running."
}

# Built quietly. -q prints the image id and nothing else; --verbose builds the
# ordinary loud way, because a failing image build is exactly when the layers
# matter.
build_image() {   # $1 = tag, rest = docker build arguments
    local tag="$1"; shift
    if [ "$VERBOSE" = 1 ]; then
        docker build -t "$tag" "$@"
    else
        docker build -q -t "$tag" "$@" >/dev/null
    fi
}

# Wait for a container's published port, or dump the container's logs and fail.
# A server that is up but not yet listening fails the first test rather than
# the whole suite, which reads as a driver bug; this is what stops that.
wait_for_port() {   # $1 = container, $2 = published port
    local container="$1" port="$2"
    for _ in $(seq 1 40); do
        if nc -z 127.0.0.1 "$port" 2>/dev/null; then return 0; fi
        sleep 1
    done
    echo "servers.sh: $container did not answer on port $port within 40s" >&2
    docker logs "$container" >&2 || true
    return 1
}

rm_container() { docker rm -f "$1" >/dev/null 2>&1 || true; }

network_up() {
    docker network inspect "$TEST_NETWORK" >/dev/null 2>&1 \
        || docker network create "$TEST_NETWORK" >/dev/null
}

up_samba() {
    build_image "$SMB_IMAGE" .github/docker/samba
    rm_container "$SMB_CONTAINER"
    docker run -d --network "$TEST_NETWORK" --network-alias samba \
        --name "$SMB_CONTAINER" -p "$SMB_PORT:445" "$SMB_IMAGE" >/dev/null
    wait_for_port "$SMB_CONTAINER" "$SMB_PORT"
    echo "  samba    127.0.0.1:$SMB_PORT (share tmp, user smbuser)"
}

up_s3() {
    rm_container "$S3_CONTAINER"
    docker run -d --network "$TEST_NETWORK" --network-alias minio \
        --name "$S3_CONTAINER" -p "$S3_PORT:9000" \
        -e MINIO_ROOT_USER="$S3_KEY" -e MINIO_ROOT_PASSWORD="$S3_SECRET" \
        "$S3_IMAGE" server /data >/dev/null
    wait_for_port "$S3_CONTAINER" "$S3_PORT"
    # MinIO's filesystem backend stores a bucket as a directory, so this is
    # enough to create one without pulling in the mc client. The Go tests make
    # their own bucket through the API; the C harness has no way to.
    docker exec "$S3_CONTAINER" mkdir -p "/data/$S3_BUCKET"
    echo "  s3       127.0.0.1:$S3_PORT (bucket $S3_BUCKET)"
}

up_ftp() {
    rm_container "$FTP_CONTAINER"
    # Passive mode hands the client a second port to connect back on, so the
    # range has to be published as well as the control port.
    docker run -d --network "$TEST_NETWORK" --network-alias ftp \
        --name "$FTP_CONTAINER" \
        -p "$FTP_PORT:21" -p "$FTP_PASV_LO-$FTP_PASV_HI:$FTP_PASV_LO-$FTP_PASV_HI" \
        -e FTP_USER="$FTP_USER" -e FTP_PASS="$FTP_PASS" \
        "$FTP_IMAGE" >/dev/null
    wait_for_port "$FTP_CONTAINER" "$FTP_PORT"
    echo "  ftp      127.0.0.1:$FTP_PORT (user $FTP_USER)"
}

up_sftp() {
    rm_container "$SFTP_CONTAINER"
    docker run -d --network "$TEST_NETWORK" --network-alias sftp \
        --name "$SFTP_CONTAINER" -p "$SFTP_PORT:22" \
        "$SFTP_IMAGE" "$SFTP_USER:$SFTP_PASS:::upload" >/dev/null
    wait_for_port "$SFTP_CONTAINER" "$SFTP_PORT"
    echo "  sftp     127.0.0.1:$SFTP_PORT (user $SFTP_USER, root /upload)"
}

up_webdav() {
    rm_container "$DAV_CONTAINER"
    docker run -d --network "$TEST_NETWORK" --network-alias webdav \
        --name "$DAV_CONTAINER" -p "$DAV_PORT:80" \
        -e USERNAME="$DAV_USER" -e PASSWORD="$DAV_PASS" "$DAV_IMAGE" >/dev/null
    wait_for_port "$DAV_CONTAINER" "$DAV_PORT"
    echo "  webdav   127.0.0.1:$DAV_PORT (user $DAV_USER)"
}

# The stand-in for Dropbox, Google Drive and OneDrive. Built from source in
# this repository rather than pulled, so it cannot drift from the drivers.
up_mockapi() {
    build_image "$MOCK_IMAGE" -f .github/docker/mockapi/Dockerfile .
    rm_container "$MOCK_CONTAINER"
    docker run -d --network "$TEST_NETWORK" --network-alias mockapi \
        --name "$MOCK_CONTAINER" -p "$MOCK_PORT:8081" "$MOCK_IMAGE" >/dev/null
    wait_for_port "$MOCK_CONTAINER" "$MOCK_PORT"
    echo "  mockapi  127.0.0.1:$MOCK_PORT (dropbox, gdrive, onedrive)"
}

down_samba()   { rm_container "$SMB_CONTAINER"; }
down_s3()      { rm_container "$S3_CONTAINER"; }
down_ftp()     { rm_container "$FTP_CONTAINER"; }
down_sftp()    { rm_container "$SFTP_CONTAINER"; }
down_webdav()  { rm_container "$DAV_CONTAINER"; }
down_mockapi() { rm_container "$MOCK_CONTAINER"; }

container_of() {
    case "$1" in
        samba)   echo "$SMB_CONTAINER" ;;
        s3)      echo "$S3_CONTAINER" ;;
        ftp)     echo "$FTP_CONTAINER" ;;
        sftp)    echo "$SFTP_CONTAINER" ;;
        webdav)  echo "$DAV_CONTAINER" ;;
        mockapi) echo "$MOCK_CONTAINER" ;;
        *)       die "unknown server '$1' (known: $ALL_SERVERS)" ;;
    esac
}

check_names() {
    local name known
    for name in "$@"; do
        known=0
        for k in $ALL_SERVERS; do [ "$name" = "$k" ] && known=1; done
        [ "$known" = 1 ] || die "unknown server '$name' (known: $ALL_SERVERS)"
    done
}

cmd="${1:-}"
[ $# -gt 0 ] && shift

case "$cmd" in
    up)
        need_docker
        names="${*:-$ALL_SERVERS}"
        # shellcheck disable=SC2086  # the words are the point
        check_names $names
        network_up
        for name in $names; do "up_$name"; done
        ;;

    down)
        # NOT need_docker: `down` is what a trap runs when something went
        # wrong, and a teardown that fails because Docker went away would
        # replace the real error with its own.
        command -v docker >/dev/null 2>&1 || exit 0
        names="${*:-$ALL_SERVERS}"
        # shellcheck disable=SC2086
        check_names $names
        for name in $names; do "down_$name"; done
        # The network belongs to the whole set, so it only goes when they all do.
        if [ $# -eq 0 ]; then
            docker network rm "$TEST_NETWORK" >/dev/null 2>&1 || true
        fi
        ;;

    status)
        need_docker
        for name in $ALL_SERVERS; do
            c="$(container_of "$name")"
            state="$(docker inspect -f '{{.State.Status}}' "$c" 2>/dev/null || echo "-")"
            printf '  %-8s %-10s %s\n' "$name" "$state" "$c"
        done
        ;;

    env)
        # What a suite needs in its environment to reach the servers this
        # script started. One definition, read by scripts/suite.sh and by
        # anyone running a single test by hand.
        cat <<ENV
SMB_HOST=${SMB_ADDR:-127.0.0.1}
SMB_PORT=$SMB_PORT
SMB_SHARE=tmp
SMB_USER=smbuser
SMB_PASS=Smbpasswd12345
S3_ENDPOINT=${S3_ADDR:-127.0.0.1}:$S3_PORT
S3_BUCKET=$S3_BUCKET
S3_ACCESS_KEY=$S3_KEY
S3_SECRET_KEY=$S3_SECRET
S3_SECURE=false
ENV
        ;;

    ""|-h|--help)
        sed -n '3,11p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
        # No command at all is a usage error; --help was asked for.
        if [ -z "$cmd" ]; then exit 2; fi
        ;;

    *)
        die "unknown command '$cmd' (up, down, status, env)"
        ;;
esac
