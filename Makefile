# Makefile — common invocations for go-networkfs.
#
# Targets:
#   test              go test -race with coverage
#   test-short        skip integration tests that start embedded servers
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

.PHONY: all
all: test archives tui

.PHONY: test
test:
	$(GO) test -race -covermode=atomic -coverprofile=$(COVERAGE) ./...
	$(GO) tool cover -func=$(COVERAGE) | tail -1

.PHONY: test-short
test-short:
	$(GO) test -race -short ./...

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
