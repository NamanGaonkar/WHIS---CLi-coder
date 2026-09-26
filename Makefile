# WHIS — hyper-lean build & release
BINARY   := whis
VERSION  ?= $(shell git describe --tags --always 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)

PLATFORMS := \
	windows/amd64 windows/arm64 \
	linux/amd64 linux/arm64 \
	darwin/amd64 darwin/arm64

.PHONY: all build install test vet fmt clean release

all: vet test build

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/whis

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/whis

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

clean:
	rm -f $(BINARY) whis.exe
	rm -rf dist

release: clean
	@mkdir -p dist
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		ext=""; if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		echo "→ $${os}/$${arch}"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" \
			-o "dist/whis-$${os}-$${arch}$${ext}" ./cmd/whis || exit 1; \
	done
	@cd dist && for f in whis-*; do sha256sum $$f > $$f.sha256; done
	@echo "artifacts in dist/"

# whis.1 compressed man page generation is intentionally not bundled;
# `whis /help` and README are the canonical docs.
