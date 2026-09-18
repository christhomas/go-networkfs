# The test runner.
#
# Everything the suite needs to build and run lives here: the Go toolchain, a C
# compiler for the ABI harnesses, and chore. A contributor needs Docker and
# nothing else, and CI runs the same image rather than a runner image that
# happens to have the right versions on it.
#
# CHORE, NOT MAKE. The container runs `chore test:ci` — the same task name a
# developer runs when the servers are already up — because chore is this
# repository's one entry point (issue #8) and a container that needed a
# different vocabulary would be a second one.
#
# BUILT FROM THE REPOSITORY ROOT (`docker build -f .github/docker/testrunner/
# Dockerfile .`), because it copies scripts/ci-install-chore.sh rather than
# carrying its own copy of how to install a pinned, checksummed binary.
FROM golang:1.26-bookworm

RUN apt-get update \
    && apt-get install -y --no-install-recommends gcc libc6-dev netcat-openbsd curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*

ARG CHORE_VERSION=0.11.0
COPY scripts/ci-install-chore.sh /tmp/ci-install-chore.sh
RUN CHORE_VERSION="$CHORE_VERSION" DEST=/usr/local/bin /tmp/ci-install-chore.sh \
    && rm /tmp/ci-install-chore.sh

WORKDIR /src

# The module cache is a volume at run time, so a rebuild of the image does not
# re-download the world.
ENV GOFLAGS=-buildvcs=false
