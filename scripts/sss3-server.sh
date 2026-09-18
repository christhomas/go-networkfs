#!/usr/bin/env bash
#
# sss3-server.sh — start and stop a real S3 server without Docker.
#
# stupid-simple-s3 is a single static Go binary, so the S3 driver's tests
# do not need a container the way the SMB ones need Samba. This fetches
# the pinned release build for the host platform, checks it against the
# published SHA-256, and runs it from build/. A contributor on Linux or
# macOS gets a real S3 server from `chore test:s3` with nothing installed
# by hand and no Docker daemon.
#
# The containerised path still exists (`chore servers:up -- s3`) and is what
# CI's integration job uses, because there every server is a container on a
# shared network and the runner reaches them by name.
#
# Usage: sss3-server.sh up|down|status
set -euo pipefail

# Pinned. Nothing here resolves a floating tag: a test server that can
# change under the suite is a test server that can fail for reasons the
# diff does not explain.
SSS3_VERSION="${SSS3_VERSION:-1.0.7}"

# SHA-256 of each published binary for v1.0.7, from the release's
# checksums.txt. Verified before the binary is ever executed.
sss3_sha() {
	case "$1" in
	linux-amd64)  echo 669f0996a6adb2fda0206ee246b2d0550e7150b6554422881eeb4c4c084f0191 ;;
	linux-arm64)  echo 242b41cb5ac884a1663299c061b63dc2394a2b47dea3135cede81d00df4a93b8 ;;
	darwin-amd64) echo e0cad571f946bb491b40ce08a07326444c59c4335c6fd127cc79b8844b17a6c4 ;;
	darwin-arm64) echo e36a96f99f5363925c75adc57586d6dfa7837b9421afbc6dcb411448fee86f31 ;;
	*) return 1 ;;
	esac
}

PORT="${S3_PORT:-9000}"
BUCKET="${S3_BUCKET:-testbucket}"
KEY="${S3_KEY:-sss3admin}"
SECRET="${S3_SECRET:-sss3admin123}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIR="$ROOT/build/sss3"
BIN="$DIR/stupid-simple-s3-$SSS3_VERSION"
PIDFILE="$DIR/sss3.pid"
LOG="$DIR/sss3.log"

# Quiet by default: a verdict, not a transcript. Everything the server
# says goes to $LOG, which is named on failure.
say() { printf '%s\n' "$*" >&2; }
die() { say "$*"; [ -f "$LOG" ] && tail -20 "$LOG" >&2; exit 1; }

platform() {
	local os arch
	case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) die "sss3: unsupported OS $(uname -s); use 'chore servers:up -- s3' for the container" ;;
	esac
	case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) die "sss3: unsupported arch $(uname -m); use 'chore servers:up -- s3' for the container" ;;
	esac
	echo "$os-$arch"
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		# macOS has no sha256sum in the base install.
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}

fetch() {
	local plat want url tmp got
	plat="$(platform)"
	want="$(sss3_sha "$plat")" || die "sss3: no pinned checksum for $plat"

	if [ -x "$BIN" ] && [ "$(sha256 "$BIN")" = "$want" ]; then
		return 0
	fi

	url="https://github.com/espebra/stupid-simple-s3/releases/download/v$SSS3_VERSION/stupid-simple-s3-$plat"
	mkdir -p "$DIR"
	tmp="$BIN.download"
	say "sss3: fetching v$SSS3_VERSION ($plat)"
	curl -fsSL --retry 3 -o "$tmp" "$url" || die "sss3: download failed: $url"

	got="$(sha256 "$tmp")"
	if [ "$got" != "$want" ]; then
		rm -f "$tmp"
		die "sss3: checksum mismatch for $plat: got $got, want $want"
	fi
	chmod +x "$tmp"
	mv "$tmp" "$BIN"
}

up() {
	down
	fetch
	mkdir -p "$DIR/data" "$DIR/tmp"
	# A fresh store each run. Objects surviving into the next run is how
	# a suite starts passing for the wrong reason.
	rm -rf "$DIR/data" "$DIR/tmp"
	mkdir -p "$DIR/data" "$DIR/tmp"

	# STUPID_BUCKET_NAME creates the bucket at startup. The Go tests can
	# make their own through the API; the C harness has no way to, which
	# is why the bucket has to exist before anything connects.
	STUPID_PORT="$PORT" \
		STUPID_STORAGE_PATH="$DIR/data" \
		STUPID_MULTIPART_PATH="$DIR/tmp" \
		STUPID_RW_ACCESS_KEY="$KEY" \
		STUPID_RW_SECRET_KEY="$SECRET" \
		STUPID_BUCKET_NAME="$BUCKET" \
		STUPID_LOG_LEVEL=warn \
		"$BIN" >"$LOG" 2>&1 &
	echo $! >"$PIDFILE"

	for _ in $(seq 1 40); do
		if curl -fsS -o /dev/null "http://127.0.0.1:$PORT/healthz" 2>/dev/null; then
			say "sss3: ready on 127.0.0.1:$PORT (bucket $BUCKET)"
			return 0
		fi
		sleep 0.5
	done
	die "sss3: not ready on port $PORT after 20s; see $LOG"
}

down() {
	if [ -f "$PIDFILE" ]; then
		kill "$(cat "$PIDFILE")" 2>/dev/null || true
		rm -f "$PIDFILE"
	fi
}

# Exit 0 when a server this script started is alive and answering, 1 otherwise.
# Both halves matter: a pid that is gone and a pid that is there but wedged are
# the same answer to "can I run the S3 tests".
status() {
	[ -f "$PIDFILE" ] || return 1
	kill -0 "$(cat "$PIDFILE")" 2>/dev/null || return 1
	curl -fsS -o /dev/null "http://127.0.0.1:$PORT/healthz" 2>/dev/null
}

case "${1:-}" in
up) up ;;
down) down ;;
status) status ;;
*)
	echo "usage: $0 up|down|status" >&2
	exit 2
	;;
esac
