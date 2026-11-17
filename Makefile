# Omarchy Backup & Restore
GO      ?= $(shell mise where go@1.27.4 2>/dev/null)/bin/go
ifeq ($(wildcard $(GO)),)
GO      := go
endif
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BINDIR  ?= $(HOME)/.local/share/obr/bin
RESTIC_VERSION ?= v0.19.1
RCLONE_VERSION ?= v1.76.0
LDFLAGS := -s -w -X github.com/gaborkis11/omarchy-backup-restore/internal/version.Version=$(VERSION)

.PHONY: build install tools test vet clean

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/obr ./cmd/obr

# restic és rclone forrásból, statikusan (a sum.golang.org ellenőrzi a modulokat)
tools:
	mkdir -p $(BINDIR)
	GOBIN=$(BINDIR) CGO_ENABLED=0 $(GO) install -trimpath -ldflags "-s -w" github.com/restic/restic/cmd/restic@$(RESTIC_VERSION)
	GOBIN=$(BINDIR) CGO_ENABLED=0 $(GO) install -trimpath -ldflags "-s -w" github.com/rclone/rclone@$(RCLONE_VERSION)

install: build
	./bin/obr install

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

clean:
	rm -rf bin
