# Makefile — common invocations for go-networkfs.
#
# Targets:
#   test              go test -race with coverage
#   test-short        skip integration tests that start embedded servers
#   test-smb          SMB driver integration tests against a Samba container
#   test-s3           S3 driver integration tests against a MinIO container
#   test-integration  every driver's integration tests, with coverage
#   test-cabi         the C ABI, exercised from C against a real server
#   bench             run benchmarks against the FTP driver
#   coverage-html     open an HTML coverage report in the browser
#   archives          build all driver c-archives (lib<name>.a) plus the
#                     unified libnetworkfs.a dispatcher archive
#   tui               build the bubbletea TUI binary
#   vet               go vet
#   tidy              go mod tidy + verify
#   clean             remove build artifacts

GO         ?= go
OUT        ?= build
COVERAGE   ?= coverage.out
ARCHIVES   := ftp sftp smb dropbox webdav gdrive s3 onedrive

# The SMB driver needs a real server. There is no in-process Go SMB server to
# stand in for one, so the integration tests run against a container.
# Where the servers are reachable. On the host they are published on
# localhost; inside the containerised runner they are reached by container name
# on a shared network, so every target takes these from variables.
SMB_ADDR      ?= 127.0.0.1
S3_ADDR       ?= 127.0.0.1
FTP_ADDR      ?= 127.0.0.1
SFTP_ADDR     ?= 127.0.0.1
DAV_ADDR      ?= 127.0.0.1

# Servers join this network so the runner container can reach them by name.
TEST_NETWORK  ?= go-networkfs-test
RUNNER_IMAGE  ?= go-networkfs-test:latest

SMB_PORT      ?= 4445
SMB_IMAGE     ?= go-networkfs-samba:test
SMB_CONTAINER ?= go-networkfs-samba

# MinIO speaks the S3 protocol, so the S3 driver can be tested against the
# real thing rather than a hand-written stub of it.
S3_PORT       ?= 9000
S3_IMAGE      ?= minio/minio:latest
S3_CONTAINER  ?= go-networkfs-minio
S3_BUCKET     ?= testbucket
S3_KEY        ?= minioadmin
S3_SECRET     ?= minioadmin

# The remaining servers. Each driver that can be given one gets one, so its
# C harness can mount and reach the success paths rather than only the
# failure paths.
FTP_PORT      ?= 2121
FTP_PASV_LO   ?= 40000
FTP_PASV_HI   ?= 40009
FTP_IMAGE     ?= garethflowers/ftp-server:latest
FTP_CONTAINER ?= go-networkfs-ftp
FTP_USER      ?= testuser
FTP_PASS      ?= Ftppasswd12345

SFTP_PORT      ?= 2222
SFTP_IMAGE     ?= atmoz/sftp:latest
SFTP_CONTAINER ?= go-networkfs-sftp
SFTP_USER      ?= testuser
SFTP_PASS      ?= testpass

MOCK_ADDR      ?= 127.0.0.1
MOCK_PORT      ?= 8081
MOCK_IMAGE     ?= go-networkfs-mockapi:test
MOCK_CONTAINER ?= go-networkfs-mockapi

DAV_PORT      ?= 8080
DAV_IMAGE     ?= bytemark/webdav:latest
DAV_CONTAINER ?= go-networkfs-webdav
DAV_USER      ?= testuser
DAV_PASS      ?= testpass

# Tags for every driver whose integration tests need a server.
INTEGRATION_TAGS ?= smb_integration,s3_integration

# The C ABI harness. It links the shipped archive and calls the exported
# functions the way a consumer does, which is the only way to reach the three
# entry points taking a ByteSlice or a size_t: a Go test file may not import
# "C", so it cannot name either type.
CABI_DIR   ?= build/cabi
CABI_COVER ?= build/cabi-cover
UNAME_S    := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
CABI_LDLIBS ?= -framework CoreFoundation -framework Security
else
CABI_LDLIBS ?= -lpthread -ldl -lresolv
endif

.PHONY: all
all: test archives tui

.PHONY: test
test:
	$(GO) test -race -covermode=atomic -coverprofile=$(COVERAGE) ./...
	$(GO) tool cover -func=$(COVERAGE) | tail -1

.PHONY: test-short
test-short:
	$(GO) test -race -short ./...

# Start a throwaway Samba server, run the SMB integration tests against it,
# and take it down however the tests turn out.
.PHONY: test-smb
test-smb: samba-up
	@SMB_HOST=$(SMB_ADDR) SMB_PORT=$(SMB_PORT) SMB_SHARE=tmp \
		SMB_USER=smbuser SMB_PASS=Smbpasswd12345 \
		$(GO) test -race -count=1 -tags=smb_integration -run Integration ./smb/... ; \
		status=$$? ; $(MAKE) samba-down ; exit $$status

# Wait for a container port, or dump the container's logs and fail.
define wait_for_port
	@printf 'waiting for $(1) on port $(2)'
	@for i in $$(seq 1 40); do \
		if nc -z 127.0.0.1 $(2) 2>/dev/null; then echo " ready"; exit 0; fi; \
		printf '.'; sleep 1; \
	done; \
	echo " timed out"; docker logs $(1); exit 1
endef

.PHONY: minio-up
minio-up:
	@docker rm -f $(S3_CONTAINER) >/dev/null 2>&1 || true
	docker run -d --network $(TEST_NETWORK) --network-alias minio --name $(S3_CONTAINER) -p $(S3_PORT):9000 \
		-e MINIO_ROOT_USER=$(S3_KEY) -e MINIO_ROOT_PASSWORD=$(S3_SECRET) \
		$(S3_IMAGE) server /data
	$(call wait_for_port,$(S3_CONTAINER),$(S3_PORT))
	@# MinIO's filesystem backend stores a bucket as a directory, so this is
	@# enough to create one without pulling in the mc client. The Go tests make
	@# their own bucket through the API; the C harness has no way to.
	@docker exec $(S3_CONTAINER) mkdir -p /data/$(S3_BUCKET)

.PHONY: ftp-up
ftp-up:
	@docker rm -f $(FTP_CONTAINER) >/dev/null 2>&1 || true
	@# Passive mode hands the client a second port to connect back on, so the
	@# range has to be published and the server has to advertise an address the
	@# client can actually reach.
	docker run -d --network $(TEST_NETWORK) --network-alias ftp --name $(FTP_CONTAINER) \
		-p $(FTP_PORT):21 -p $(FTP_PASV_LO)-$(FTP_PASV_HI):$(FTP_PASV_LO)-$(FTP_PASV_HI) \
		-e FTP_USER=$(FTP_USER) -e FTP_PASS=$(FTP_PASS) \
		$(FTP_IMAGE)
	$(call wait_for_port,$(FTP_CONTAINER),$(FTP_PORT))

.PHONY: ftp-down
ftp-down:
	@docker rm -f $(FTP_CONTAINER) >/dev/null 2>&1 || true

.PHONY: sftp-up
sftp-up:
	@docker rm -f $(SFTP_CONTAINER) >/dev/null 2>&1 || true
	docker run -d --network $(TEST_NETWORK) --network-alias sftp --name $(SFTP_CONTAINER) -p $(SFTP_PORT):22 \
		$(SFTP_IMAGE) $(SFTP_USER):$(SFTP_PASS):::upload
	$(call wait_for_port,$(SFTP_CONTAINER),$(SFTP_PORT))

.PHONY: sftp-down
sftp-down:
	@docker rm -f $(SFTP_CONTAINER) >/dev/null 2>&1 || true

.PHONY: webdav-up
webdav-up:
	@docker rm -f $(DAV_CONTAINER) >/dev/null 2>&1 || true
	docker run -d --network $(TEST_NETWORK) --network-alias webdav --name $(DAV_CONTAINER) -p $(DAV_PORT):80 \
		-e USERNAME=$(DAV_USER) -e PASSWORD=$(DAV_PASS) $(DAV_IMAGE)
	$(call wait_for_port,$(DAV_CONTAINER),$(DAV_PORT))

.PHONY: webdav-down
webdav-down:
	@docker rm -f $(DAV_CONTAINER) >/dev/null 2>&1 || true

# Every server at once, and the matching teardown. Nothing here is installed
# on the machine running the tests.
# The stand-in for Dropbox, Google Drive and OneDrive. Built from source in
# this repository rather than pulled, so it cannot drift from the drivers.
.PHONY: mockapi-up
mockapi-up:
	docker build -t $(MOCK_IMAGE) -f .github/docker/mockapi/Dockerfile .
	@docker rm -f $(MOCK_CONTAINER) >/dev/null 2>&1 || true
	docker run -d --network $(TEST_NETWORK) --network-alias mockapi \
		--name $(MOCK_CONTAINER) -p $(MOCK_PORT):8081 $(MOCK_IMAGE)
	$(call wait_for_port,$(MOCK_CONTAINER),$(MOCK_PORT))

.PHONY: mockapi-down
mockapi-down:
	@docker rm -f $(MOCK_CONTAINER) >/dev/null 2>&1 || true

.PHONY: network-up
network-up:
	@docker network inspect $(TEST_NETWORK) >/dev/null 2>&1 || \
		docker network create $(TEST_NETWORK) >/dev/null

.PHONY: servers-up
servers-up: network-up samba-up minio-up ftp-up sftp-up webdav-up mockapi-up

.PHONY: servers-down
servers-down: samba-down minio-down ftp-down sftp-down webdav-down mockapi-down
	@docker network rm $(TEST_NETWORK) >/dev/null 2>&1 || true

.PHONY: minio-down
minio-down:
	@docker rm -f $(S3_CONTAINER) >/dev/null 2>&1 || true

.PHONY: samba-up
samba-up:
	docker build -t $(SMB_IMAGE) .github/docker/samba
	@docker rm -f $(SMB_CONTAINER) >/dev/null 2>&1 || true
	docker run -d --network $(TEST_NETWORK) --network-alias samba --name $(SMB_CONTAINER) -p $(SMB_PORT):445 $(SMB_IMAGE)
	$(call wait_for_port,$(SMB_CONTAINER),$(SMB_PORT))

.PHONY: samba-down
samba-down:
	@docker rm -f $(SMB_CONTAINER) >/dev/null 2>&1 || true

# S3 driver against a throwaway MinIO.
.PHONY: test-s3
test-s3: minio-up
	@S3_ENDPOINT=$(S3_ADDR):$(S3_PORT) S3_BUCKET=$(S3_BUCKET) \
		S3_ACCESS_KEY=$(S3_KEY) S3_SECRET_KEY=$(S3_SECRET) S3_SECURE=false \
		$(GO) test -race -count=1 -tags=s3_integration -run Integration ./s3/... ; \
		status=$$? ; $(MAKE) minio-down ; exit $$status

# Every driver that needs a server, with one coverage profile over the lot.
# This is the number that matters: without the tags most of each driver is
# never executed, and the default `test` target reports it as untested.
.PHONY: test-integration
test-integration: servers-up
	@SMB_HOST=$(SMB_ADDR) SMB_PORT=$(SMB_PORT) SMB_SHARE=tmp \
		SMB_USER=smbuser SMB_PASS=Smbpasswd12345 \
		S3_ENDPOINT=$(S3_ADDR):$(S3_PORT) S3_BUCKET=$(S3_BUCKET) \
		S3_ACCESS_KEY=$(S3_KEY) S3_SECRET_KEY=$(S3_SECRET) S3_SECURE=false \
		$(GO) test -count=1 -tags=$(INTEGRATION_TAGS) \
			-coverpkg=$$($(GO) list ./... | grep -v '/test/' | paste -sd, -) \
			-covermode=atomic -coverprofile=$(COVERAGE) ./... ; \
		status=$$? ; \
		if [ $$status -ne 0 ]; then $(MAKE) servers-down ; exit $$status; fi
	@# The C harnesses need both servers still up: the SMB and S3 archives
	@# mount against them, which is the only way to reach the success paths.
	@$(MAKE) --no-print-directory cabi-cover ; \
		status=$$? ; $(MAKE) servers-down ; \
		if [ $$status -ne 0 ]; then exit $$status; fi
	@$(MAKE) --no-print-directory cover-strip-harness
	@$(GO) tool cover -func=$(COVERAGE) | tail -1

# Run the C harness against the already-running Samba and fold its profile
# into $(COVERAGE). The C ABI is the only caller of some of this code, so
# leaving it out understates what is actually tested.
.PHONY: cabi-cover
cabi-cover: cabi-drivers cabi-unified
	@$(GO) tool covdata textfmt -i=$(CABI_COVER) -o $(CABI_DIR)/unified.out
	@test/cabi/merge-coverage.sh $(CABI_DIR)/merged.out $(COVERAGE) \
		$(CABI_DIR)/unified.out $(CABI_DIR)/drivers/*.out
	@mv $(CABI_DIR)/merged.out $(COVERAGE)

# The C ABI harnesses. Each links one shipped archive and calls its exported
# functions the way a consumer does.
#
# A c-archive has no Go main, so the runtime never writes its coverage counters
# on the way out. The archives are built with the `coverage` tag, which adds an
# export the unified harness calls to flush them, and with -covermode=atomic,
# which is what WriteCountersDir requires. The per-driver archives have no such
# export, so they are built and run without coverage: what they prove is that
# the archive links and the ABI behaves, which does not depend on measuring it.
.PHONY: test-cabi
test-cabi: servers-up
	@$(MAKE) --no-print-directory cabi-drivers
	@$(MAKE) --no-print-directory cabi-unified ; \
		status=$$? ; $(MAKE) servers-down ; exit $$status

# The eight per-driver archives, unmounted. These reach openfile, writefile and
# setOutBytes, which no Go test can call.
#
# Each archive gets its own coverage directory: they are separate programs with
# separate metadata, and mixing their counters in one directory is not
# something covdata is asked to untangle.
.PHONY: cabi-drivers
cabi-drivers:
	@rm -rf $(CABI_DIR)/drivers ; mkdir -p $(CABI_DIR)/drivers
	@for d in $(ARCHIVES); do \
		mkdir -p $(CABI_DIR)/drivers/cov-$$d ; \
		$(GO) build -cover -covermode=atomic -tags coverage -buildmode=c-archive \
			-o $(CABI_DIR)/drivers/lib$$d.a ./$$d/cmd/$$d || exit 1 ; \
		$(CC) -DNETWORKFS_COVERAGE -o $(CABI_DIR)/drivers/test_$$d test/cabi/test_$$d.c \
			-I$(CABI_DIR)/drivers $(CABI_DIR)/drivers/lib$$d.a $(CABI_LDLIBS) || exit 1 ; \
		case $$d in \
			smb) cfg='{"host":"$(SMB_ADDR)","port":"$(SMB_PORT)","share":"tmp","user":"smbuser","pass":"Smbpasswd12345"}' ;; \
			s3)  cfg='{"endpoint":"$(S3_ADDR):$(S3_PORT)","bucket":"$(S3_BUCKET)","access_key_id":"$(S3_KEY)","secret_access_key":"$(S3_SECRET)","secure":"false","use_path_style":"true"}' ;; \
			ftp) cfg='{"host":"$(FTP_ADDR)","port":"$(FTP_PORT)","user":"$(FTP_USER)","pass":"$(FTP_PASS)"}' ;; \
			sftp) cfg='{"host":"$(SFTP_ADDR)","port":"$(SFTP_PORT)","user":"$(SFTP_USER)","pass":"$(SFTP_PASS)","root":"/upload"}' ;; \
			webdav) cfg='{"url":"http://$(DAV_ADDR):$(DAV_PORT)","user":"$(DAV_USER)","pass":"$(DAV_PASS)"}' ;; \
			dropbox) cfg='{"access_token":"mock","api_base_url":"http://$(MOCK_ADDR):$(MOCK_PORT)/dropbox"}' ;; \
			gdrive) cfg='{"client_id":"c","client_secret":"s","refresh_token":"r","api_base_url":"http://$(MOCK_ADDR):$(MOCK_PORT)/gdrive"}' ;; \
			onedrive) cfg='{"client_id":"c","refresh_token":"r","api_base_url":"http://$(MOCK_ADDR):$(MOCK_PORT)/onedrive"}' ;; \
			*)   cfg='' ;; \
		esac ; \
		CABI_CONFIG="$$cfg" GOCOVERDIR=$(CABI_DIR)/drivers/cov-$$d \
			$(CABI_DIR)/drivers/test_$$d || exit 1 ; \
		$(GO) tool covdata textfmt -i=$(CABI_DIR)/drivers/cov-$$d \
			-o $(CABI_DIR)/drivers/$$d.out || exit 1 ; \
	done

# The unified archive, with coverage, against a real server. Assumes Samba is
# already up.
.PHONY: cabi-unified
cabi-unified:
	@rm -rf $(CABI_DIR)/unified $(CABI_COVER)
	@mkdir -p $(CABI_DIR)/unified $(CABI_COVER)
	@$(GO) build -cover -covermode=atomic -tags coverage \
		-buildmode=c-archive -o $(CABI_DIR)/unified/libnetworkfs.a ./cmd/networkfs
	@$(CC) -DNETWORKFS_COVERAGE -o $(CABI_DIR)/unified/test_networkfs \
		test/cabi/test_networkfs.c -I$(CABI_DIR)/unified \
		$(CABI_DIR)/unified/libnetworkfs.a $(CABI_LDLIBS)
	@GOCOVERDIR=$(CABI_COVER) \
		SMB_HOST=$(SMB_ADDR) SMB_PORT=$(SMB_PORT) SMB_SHARE=tmp \
		SMB_USER=smbuser SMB_PASS=Smbpasswd12345 \
		$(CABI_DIR)/unified/test_networkfs

# Run the whole suite inside a container, so the only thing needed on the
# machine is Docker: no Go toolchain, no C compiler, no make.
#
# The servers are started on the host and joined to a shared network; the
# runner joins the same network and reaches them by name on their internal
# ports rather than through published ones. That is also what makes the
# pipeline reproducible, since CI runs this same image.
.PHONY: test-docker
test-docker: servers-up
	docker build -t $(RUNNER_IMAGE) .github/docker/testrunner
	@docker run --rm --network $(TEST_NETWORK) \
		-v "$(CURDIR)":/src -v go-networkfs-gomod:/go/pkg/mod \
		-e SMB_ADDR=samba  -e SMB_PORT=445 \
		-e S3_ADDR=minio   -e S3_PORT=9000 \
		-e FTP_ADDR=ftp    -e FTP_PORT=21 \
		-e SFTP_ADDR=sftp  -e SFTP_PORT=22 \
		-e DAV_ADDR=webdav -e DAV_PORT=80 \
		-e MOCK_ADDR=mockapi -e MOCK_PORT=8081 \
		$(RUNNER_IMAGE) make ci-tests ; \
		status=$$? ; $(MAKE) servers-down ; exit $$status

# The suite, assuming the servers are already up and reachable at the *_ADDR
# addresses. This is what runs inside the runner container; it starts nothing
# and tears nothing down.
#
# test/... is excluded from the measurement. The mock API server lives there
# and is scaffolding, not product: counting it would mean the coverage figure
# falls every time the test harness grows, which is the wrong incentive.
.PHONY: ci-tests
ci-tests:
	$(GO) test -count=1 -tags=$(INTEGRATION_TAGS) \
		-coverpkg=$$($(GO) list ./... | grep -v '/test/' | paste -sd, -) \
		-covermode=atomic -coverprofile=$(COVERAGE) ./...
	@$(MAKE) --no-print-directory cabi-cover
	@$(MAKE) --no-print-directory cover-strip-harness
	@$(GO) tool cover -func=$(COVERAGE) | tail -1

# Drop the harness's own packages from a profile.
.PHONY: cover-strip-harness
cover-strip-harness:
	@grep -v '/test/' $(COVERAGE) > $(COVERAGE).tmp && mv $(COVERAGE).tmp $(COVERAGE)

.PHONY: bench
bench:
	$(GO) test -run=^$$ -bench=. -benchmem ./ftp/...

.PHONY: coverage-html
coverage-html: $(COVERAGE)
	$(GO) tool cover -html=$(COVERAGE)

$(COVERAGE):
	$(GO) test -covermode=atomic -coverprofile=$(COVERAGE) ./...

.PHONY: archives
archives: $(addprefix $(OUT)/lib,$(addsuffix .a,$(ARCHIVES))) $(OUT)/libnetworkfs.a

$(OUT)/lib%.a:
	@mkdir -p $(OUT)
	CGO_ENABLED=1 $(GO) build -buildmode=c-archive -o $@ ./$*/cmd/$*

# Unified dispatcher archive — links every registered driver and chooses the
# backend at mount time via the driver_type argument. Source lives under
# cmd/networkfs rather than <driver>/cmd/<driver>, so it has its own rule.
$(OUT)/libnetworkfs.a:
	@mkdir -p $(OUT)
	CGO_ENABLED=1 $(GO) build -buildmode=c-archive -o $@ ./cmd/networkfs

.PHONY: tui
tui:
	@mkdir -p $(OUT)
	$(GO) build -o $(OUT)/networkfs ./cmd/tui

.PHONY: vet
vet:
	$(GO) vet ./...

.PHONY: tidy
tidy:
	$(GO) mod tidy
	$(GO) mod verify

.PHONY: clean
clean:
	rm -rf $(OUT) $(COVERAGE)
