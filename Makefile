BIN := analytics
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test check vet build-all dist docker run smoke test-install dashboards clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/analytics

# GOPKGS excludes node_modules: some npm packages ship Go source that
# go list ./... would otherwise treat as part of this module.
GOPKGS = $(shell go list ./... | grep -v '/node_modules/')

test:
	go test -race $(GOPKGS)

vet:
	go vet $(GOPKGS)

check: vet
	./scripts/coverage.sh

build-all:
	for target in linux/amd64 linux/arm64 linux/arm; do \
		GOOS=$${target%/*} GOARCH=$${target#*/} CGO_ENABLED=0 \
		go build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BIN)-$${target%/*}-$${target#*/} ./cmd/analytics || exit 1; \
	done

# Release tarballs: binary + deploy/ (installer, units, configs) per arch,
# with a SHA256SUMS the installer verifies. This is what release.yml publishes.
dist: build-all
	@set -e; for arch in amd64 arm64 arm; do \
		stage=dist/stage-$$arch; rm -rf $$stage; mkdir -p $$stage; \
		cp dist/$(BIN)-linux-$$arch $$stage/$(BIN); \
		cp -r deploy $$stage/deploy; \
		tar -czf dist/$(BIN)-linux-$$arch.tar.gz -C $$stage .; \
		rm -rf $$stage; \
		echo "packaged dist/$(BIN)-linux-$$arch.tar.gz"; \
	done
	cd dist && sha256sum $(BIN)-linux-*.tar.gz > SHA256SUMS

docker:
	docker build -t analytics:$(VERSION) .

# ---- local development / testing ----

define LOCAL_CONFIG
{
  "listen": "127.0.0.1:8080",
  "database": "sqlite://local/analytics.db",
  "geo": "none://",
  "log": { "level": "debug", "format": "text" },
  "buffer": { "flush_max_events": 100, "flush_interval": "1s", "capacity": 10000 },
  "projects": [
    {
      "alias": "dev",
      "name": "Local Dev",
      "allowed_origins": ["http://localhost:8080", "http://localhost:5173", "http://localhost:3000"]
    }
  ]
}
endef
export LOCAL_CONFIG

local/config.json:
	@mkdir -p local
	@printf '%s\n' "$$LOCAL_CONFIG" > $@
	@echo "wrote $@"

run: build local/config.json
	./$(BIN) serve -config local/config.json

smoke: build
	./scripts/smoke.sh

test-install: build
	./scripts/test-install.sh

dashboards: local/config.json
	cd backoffice/evidence && npm install \
		&& EVIDENCE_SOURCE__analytics__filename=../../../../local/analytics.db npm run sources \
		&& EVIDENCE_SOURCE__analytics__filename=../../../../local/analytics.db npm run dev

clean:
	rm -rf $(BIN) dist local coverage.out
