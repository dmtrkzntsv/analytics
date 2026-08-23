BIN := analytics
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test check vet build-all docker

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/analytics

test:
	go test -race ./...

vet:
	go vet ./...

check: vet
	./scripts/coverage.sh

build-all:
	for target in linux/amd64 linux/arm64 linux/arm; do \
		GOOS=$${target%/*} GOARCH=$${target#*/} CGO_ENABLED=0 \
		go build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BIN)-$${target%/*}-$${target#*/} ./cmd/analytics || exit 1; \
	done

docker:
	docker build -t analytics:$(VERSION) .
