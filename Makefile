VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
LDFLAGS := -s -w -X github.com/emdzej/kcctl/internal/app.Version=$(VERSION)
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: build install test lint dist snapshot clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/kcctl ./cmd/kcctl

# Copies the host build into $GOPATH/bin (usually ~/go/bin).
install: build
	install -m 0755 bin/kcctl "$$(go env GOPATH)/bin/kcctl"

test:
	go vet ./...
	go test ./...
	@test -z "$$(gofmt -l .)" || (echo "gofmt needed:"; gofmt -l .; exit 1)

lint:
	golangci-lint run

# Cross-compile without goreleaser; release builds use goreleaser.
dist:
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=$$( [ $$os = windows ] && echo .exe ); \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' \
			-o dist/kcctl_$${os}_$${arch}$$ext ./cmd/kcctl || exit 1; \
	done

snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -rf bin dist
