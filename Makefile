GO ?= go
GOOS ?= linux
GOARCH ?= amd64
CGO_ENABLED ?= 0

BIN_DIR := bin
ARTIFACT_DIR := internal/embedded/artifacts/linux_$(GOARCH)

.PHONY: all build test fmt vet clean

all: test build

build:
	mkdir -p $(BIN_DIR) $(ARTIFACT_DIR)
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=$(CGO_ENABLED) $(GO) build -trimpath -o $(ARTIFACT_DIR)/runnerd ./cmd/runnerd
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=$(CGO_ENABLED) $(GO) build -trimpath -o $(ARTIFACT_DIR)/sandbox-init ./cmd/sandbox-init
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=$(CGO_ENABLED) $(GO) build -trimpath -o $(BIN_DIR)/sandboxd ./cmd/sandboxd
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=$(CGO_ENABLED) $(GO) build -trimpath -o $(BIN_DIR)/runnerd ./cmd/runnerd
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=$(CGO_ENABLED) $(GO) build -trimpath -o $(BIN_DIR)/sandbox-init ./cmd/sandbox-init

test:
	$(GO) test ./...

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

clean:
	rm -rf $(BIN_DIR)
	rm -f internal/embedded/artifacts/linux_*/runnerd
	rm -f internal/embedded/artifacts/linux_*/sandbox-init

