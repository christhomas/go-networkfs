# Makefile — common invocations for go-networkfs.
#
# Targets:
#   test              go test -race with coverage
#   test-short        skip integration tests that start embedded servers
#   test-smb          SMB driver integration tests against a Samba container
#   test-s3           S3 driver integration tests against a MinIO container
#   test-integration  every driver's integration tests, with coverage
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

# Tags for every driver whose integration tests need a server.
INTEGRATION_TAGS ?= smb_integration,s3_integration

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
	@SMB_HOST=127.0.0.1 SMB_PORT=$(SMB_PORT) SMB_SHARE=tmp \
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
	docker run -d --name $(S3_CONTAINER) -p $(S3_PORT):9000 \
		-e MINIO_ROOT_USER=$(S3_KEY) -e MINIO_ROOT_PASSWORD=$(S3_SECRET) \
		$(S3_IMAGE) server /data
	$(call wait_for_port,$(S3_CONTAINER),$(S3_PORT))

.PHONY: minio-down
minio-down:
	@docker rm -f $(S3_CONTAINER) >/dev/null 2>&1 || true

.PHONY: samba-up
samba-up:
	docker build -t $(SMB_IMAGE) .github/docker/samba
	@docker rm -f $(SMB_CONTAINER) >/dev/null 2>&1 || true
	docker run -d --name $(SMB_CONTAINER) -p $(SMB_PORT):445 $(SMB_IMAGE)
	$(call wait_for_port,$(SMB_CONTAINER),$(SMB_PORT))

.PHONY: samba-down
samba-down:
	@docker rm -f $(SMB_CONTAINER) >/dev/null 2>&1 || true

# S3 driver against a throwaway MinIO.
.PHONY: test-s3
test-s3: minio-up
	@S3_ENDPOINT=127.0.0.1:$(S3_PORT) S3_BUCKET=$(S3_BUCKET) \
		S3_ACCESS_KEY=$(S3_KEY) S3_SECRET_KEY=$(S3_SECRET) S3_SECURE=false \
		$(GO) test -race -count=1 -tags=s3_integration -run Integration ./s3/... ; \
		status=$$? ; $(MAKE) minio-down ; exit $$status

# Every driver that needs a server, with one coverage profile over the lot.
# This is the number that matters: without the tags most of each driver is
# never executed, and the default `test` target reports it as untested.
.PHONY: test-integration
test-integration: samba-up minio-up
	@SMB_HOST=127.0.0.1 SMB_PORT=$(SMB_PORT) SMB_SHARE=tmp \
		SMB_USER=smbuser SMB_PASS=Smbpasswd12345 \
		S3_ENDPOINT=127.0.0.1:$(S3_PORT) S3_BUCKET=$(S3_BUCKET) \
		S3_ACCESS_KEY=$(S3_KEY) S3_SECRET_KEY=$(S3_SECRET) S3_SECURE=false \
		$(GO) test -count=1 -tags=$(INTEGRATION_TAGS) \
			-covermode=atomic -coverprofile=$(COVERAGE) ./... ; \
		status=$$? ; $(MAKE) samba-down minio-down ; \
		if [ $$status -ne 0 ]; then exit $$status; fi
	@$(GO) tool cover -func=$(COVERAGE) | tail -1

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
