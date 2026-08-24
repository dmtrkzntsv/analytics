BIN := analytics
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test check vet build-all dist docker run smoke test-install test-compose dashboards seed-demo clean

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
		cp .env.example projects.example.json $$stage/; \
		tar -czf dist/$(BIN)-linux-$$arch.tar.gz -C $$stage .; \
		rm -rf $$stage; \
		echo "packaged dist/$(BIN)-linux-$$arch.tar.gz"; \
	done
	cd dist && sha256sum $(BIN)-linux-*.tar.gz > SHA256SUMS

docker:
	docker build --target runtime -t analytics:$(VERSION) .
	docker build --target evidence -t analytics-evidence:$(VERSION) .

# ---- local development / testing ----

define LOCAL_ENV
LISTEN_ADDR=127.0.0.1:8080
DATABASE_URL=sqlite://local/analytics.db
GEO_URL=none://
LOG_LEVEL=debug
LOG_FORMAT=text
BUFFER_FLUSH_INTERVAL=1s
PROJECTS_FILE=local/projects.json
endef
export LOCAL_ENV

# Several projects so the dashboards show a realistic project list; each alias
# has a matching traffic profile in scripts/seed-demo.py.
define LOCAL_PROJECTS
[
  {
    "alias": "dev",
    "name": "Local Dev",
    "allowed_origins": ["http://localhost:8080", "http://localhost:5173", "http://localhost:3000"]
  },
  {
    "alias": "marketing",
    "name": "Marketing Site",
    "allowed_origins": ["http://localhost:8080", "https://example.com"]
  },
  {
    "alias": "docs",
    "name": "Docs Portal",
    "allowed_origins": ["http://localhost:8080", "https://docs.example.com"]
  },
  {
    "alias": "app",
    "name": "SaaS App",
    "allowed_origins": ["http://localhost:8080", "https://app.example.com"]
  },
  {
    "alias": "legacy",
    "name": "Legacy Blog",
    "allowed_origins": ["http://localhost:8080"]
  }
]
endef
export LOCAL_PROJECTS

local/.env:
	@mkdir -p local
	@printf '%s\n' "$$LOCAL_ENV" > $@
	@echo "wrote $@"

local/projects.json:
	@mkdir -p local
	@printf '%s\n' "$$LOCAL_PROJECTS" > $@
	@echo "wrote $@"

# The binary reads plain env vars; the launcher loads the env file (same
# division of labour as systemd's EnvironmentFile= in production).
run: build local/.env local/projects.json
	set -a; . ./local/.env; set +a; ./$(BIN) serve

smoke: build
	./scripts/smoke.sh

test-install: build
	./scripts/test-install.sh

# Builds both images and runs the single-server compose stack end to end.
# Slow — a first Evidence build takes about a minute — so it is not part of
# `make check`.
test-compose:
	./scripts/test-compose.sh

# Fills local/analytics.db with 180 days of believable traffic so the dashboards
# have something to plot, one profile per project in local/projects.json. Needs
# the server to have started once so those projects are registered. Re-running
# replaces the seeded rows rather than stacking another copy on top.
seed-demo: local/.env local/projects.json
	python3 scripts/seed-demo.py local/analytics.db

dashboards:
	cd evidence && npm install \
		&& EVIDENCE_SOURCE__analytics__filename=../../../local/analytics.db npm run sources \
		&& EVIDENCE_SOURCE__analytics__filename=../../../local/analytics.db npm run dev

clean:
	rm -rf $(BIN) dist local coverage.out
