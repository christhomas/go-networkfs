#!/usr/bin/env bash
#
# test-env.sh — every address, port, credential and image the test rig uses,
# in ONE place. Sourced, never executed.
#
# WHY IT IS ONE FILE. These values were the Makefile's variable block, and they
# are read by four things that must agree: the script that starts the servers,
# the script that runs the suite against them, the script that builds and runs
# the C harnesses, and the runner container's environment. Four copies of
# "the SMB port is 4445" is three chances to be wrong, and the failure is a
# connection refused in a driver test — which reads as a driver bug.
#
# EVERY VALUE TAKES THE ENVIRONMENT FIRST. On a developer machine the servers
# are published on localhost, which is what these defaults say. Inside the
# containerised runner they are reached by container NAME on the shared Docker
# network and on the INTERNAL port, so scripts/docker-suite.sh passes
# SMB_ADDR=samba SMB_PORT=445 and so on, and nothing here has to know.
#
# THE NAMES ARE THE MAKEFILE'S, unchanged. They are what .github/workflows,
# the runner container and anyone's shell history already say.

# Where the servers are reachable.
SMB_ADDR="${SMB_ADDR:-127.0.0.1}"
S3_ADDR="${S3_ADDR:-127.0.0.1}"
FTP_ADDR="${FTP_ADDR:-127.0.0.1}"
SFTP_ADDR="${SFTP_ADDR:-127.0.0.1}"
DAV_ADDR="${DAV_ADDR:-127.0.0.1}"
MOCK_ADDR="${MOCK_ADDR:-127.0.0.1}"

# The servers join this network so the runner container can reach them by name.
TEST_NETWORK="${TEST_NETWORK:-go-networkfs-test}"
RUNNER_IMAGE="${RUNNER_IMAGE:-go-networkfs-test:latest}"

# The SMB driver cannot be tested without a real server: there is no in-process
# Go SMB server to stand in for one.
SMB_PORT="${SMB_PORT:-4445}"
SMB_IMAGE="${SMB_IMAGE:-go-networkfs-samba:test}"
SMB_CONTAINER="${SMB_CONTAINER:-go-networkfs-samba}"
SMB_SHARE="${SMB_SHARE:-tmp}"
SMB_USER="${SMB_USER:-smbuser}"
SMB_PASS="${SMB_PASS:-Smbpasswd12345}"

# MinIO speaks the S3 protocol, so the S3 driver is tested against the real
# thing rather than a hand-written stub of it.
S3_PORT="${S3_PORT:-9000}"
S3_IMAGE="${S3_IMAGE:-minio/minio:latest}"
S3_CONTAINER="${S3_CONTAINER:-go-networkfs-minio}"
S3_BUCKET="${S3_BUCKET:-testbucket}"
S3_KEY="${S3_KEY:-minioadmin}"
S3_SECRET="${S3_SECRET:-minioadmin}"

FTP_PORT="${FTP_PORT:-2121}"
FTP_PASV_LO="${FTP_PASV_LO:-40000}"
FTP_PASV_HI="${FTP_PASV_HI:-40009}"
FTP_IMAGE="${FTP_IMAGE:-garethflowers/ftp-server:latest}"
FTP_CONTAINER="${FTP_CONTAINER:-go-networkfs-ftp}"
FTP_USER="${FTP_USER:-testuser}"
FTP_PASS="${FTP_PASS:-Ftppasswd12345}"

SFTP_PORT="${SFTP_PORT:-2222}"
SFTP_IMAGE="${SFTP_IMAGE:-atmoz/sftp:latest}"
SFTP_CONTAINER="${SFTP_CONTAINER:-go-networkfs-sftp}"
SFTP_USER="${SFTP_USER:-testuser}"
SFTP_PASS="${SFTP_PASS:-testpass}"

DAV_PORT="${DAV_PORT:-8080}"
DAV_IMAGE="${DAV_IMAGE:-bytemark/webdav:latest}"
DAV_CONTAINER="${DAV_CONTAINER:-go-networkfs-webdav}"
DAV_USER="${DAV_USER:-testuser}"
DAV_PASS="${DAV_PASS:-testpass}"

# Dropbox, Google Drive and OneDrive cannot be run locally the way Samba can.
# One mock API stands in for all three, built from test/mockapi in this repo.
MOCK_PORT="${MOCK_PORT:-8081}"
MOCK_IMAGE="${MOCK_IMAGE:-go-networkfs-mockapi:test}"
MOCK_CONTAINER="${MOCK_CONTAINER:-go-networkfs-mockapi}"

# Every driver whose integration tests are behind a build tag and need a
# server. Without these tags most of each driver is never executed and the
# default coverage number reports it as untested.
INTEGRATION_TAGS="${INTEGRATION_TAGS:-smb_integration,s3_integration}"

# The drivers that build as a c-archive. THIS LIST AND chores.yml's DRIVERS
# MUST AGREE — scripts/cabi.sh builds one harness per name here, and the
# archives task checks that every name in its own list produced an archive.
DRIVERS="${DRIVERS:-ftp sftp smb dropbox webdav gdrive s3 onedrive}"

COVERAGE="${COVERAGE:-coverage.out}"

# The C ABI harness. It links the shipped archive and calls the exported
# functions the way a consumer does, which is the only way to reach the three
# entry points taking a ByteSlice or a size_t: a Go test file may not import
# "C", so it cannot name either type.
CABI_DIR="${CABI_DIR:-build/cabi}"
CABI_COVER="${CABI_COVER:-build/cabi-cover}"
CC="${CC:-cc}"
if [ -z "${CABI_LDLIBS:-}" ]; then
    case "$(uname -s)" in
        Darwin) CABI_LDLIBS="-framework CoreFoundation -framework Security" ;;
        *)      CABI_LDLIBS="-lpthread -ldl -lresolv" ;;
    esac
fi
