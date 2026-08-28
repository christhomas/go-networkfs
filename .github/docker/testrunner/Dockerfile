# The test runner.
#
# Everything the suite needs to build and run lives here: the Go toolchain, a C
# compiler for the ABI harnesses, and make. A contributor needs Docker and
# nothing else, and CI runs the same image rather than a runner image that
# happens to have the right versions on it.
FROM golang:1.25-bookworm

RUN apt-get update \
    && apt-get install -y --no-install-recommends gcc libc6-dev make netcat-openbsd \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src

# The module cache is a volume at run time, so a rebuild of the image does not
# re-download the world.
ENV GOFLAGS=-buildvcs=false
