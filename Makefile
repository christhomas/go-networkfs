# Makefile — common invocations for go-networkfs.
#
# Targets:
#   test              go test -race with coverage
#   test-short        skip integration tests that start embedded servers
#   test-smb          SMB driver integration tests against a Samba container
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
test-smb:
	docker build -t $(SMB_IMAGE) .github/docker/samba
	@docker rm -f $(SMB_CONTAINER) >/dev/null 2>&1 || true
	docker run -d --name $(SMB_CONTAINER) -p $(SMB_PORT):445 $(SMB_IMAGE)
	@printf 'waiting for samba on port $(SMB_PORT)'
	@for i in $$(seq 1 30); do \
		if nc -z 127.0.0.1 $(SMB_PORT) 2>/dev/null; then echo " ready"; break; fi; \
		printf '.'; sleep 1; \
	done
	@SMB_HOST=127.0.0.1 SMB_PORT=$(SMB_PORT) SMB_SHARE=tmp \
		SMB_USER=smbuser SMB_PASS=Smbpasswd12345 \
		$(GO) test -race -count=1 -tags=smb_integration -run Integration ./smb/... ; \
		status=$$? ; \
		docker rm -f $(SMB_CONTAINER) >/dev/null 2>&1 || true ; \
		exit $$status

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
