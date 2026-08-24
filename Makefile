BIN := analytics
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test check vet build-all dist docker run smoke test-install dashboards seed-demo clean

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
	docker build -t analytics:$(VERSION) .

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
# has a matching traffic profile in scripts/seed-demo.py. Every project needs
# an ingest key or the service refuses to start; `dev` and `app` run in
# identified mode so the users, groups and retention pages have data.
define LOCAL_PROJECTS
[
  {
    "alias": "dev",
    "name": "Local Dev",
    "identity": "identified",
    "ingest_keys": [{ "key": "ak_dev00000000000000000000000000", "label": "local" }],
    "allowed_origins": ["http://localhost:8080", "http://localhost:5173", "http://localhost:3000"]
  },
  {
    "alias": "marketing",
    "name": "Marketing Site",
    "ingest_keys": [{ "key": "ak_marketing000000000000000000", "label": "web" }],
    "allowed_origins": ["http://localhost:8080", "https://example.com"]
  },
  {
    "alias": "docs",
    "name": "Docs Portal",
    "ingest_keys": [{ "key": "ak_docs0000000000000000000000", "label": "web" }],
    "allowed_origins": ["http://localhost:8080", "https://docs.example.com"]
  },
  {
    "alias": "app",
    "name": "SaaS App",
    "identity": "identified",
    "ingest_keys": [
      { "key": "ak_app00000000000000000000000", "label": "web" },
      { "key": "ak_appios00000000000000000000", "label": "ios" }
    ],
    "allowed_origins": ["http://localhost:8080", "https://app.example.com"]
  },
  {
    "alias": "legacy",
    "name": "Legacy Blog",
    "ingest_keys": [{ "key": "ak_legacy00000000000000000000", "label": "web" }],
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

# Fills local/analytics.db with 180 days of believable traffic so the dashboards
# have something to plot, one profile per project in local/projects.json. Needs
# the server to have started once so those projects are registered. Re-running
# replaces the seeded rows rather than stacking another copy on top.
seed-demo: local/.env local/projects.json build
	@DATABASE_URL="sqlite://$(PWD)/local/analytics.db" \
	 PROJECTS_FILE="$(PWD)/local/projects.json" ./$(BIN) migrate
	python3 scripts/seed-demo.py local/analytics.db local/projects.json
	@echo
	@echo "Cohorts are computed by the daily pass; run 'make run' once to"
	@echo "trigger the boot catch-up so the retention page has data."

dashboards:
	cd backoffice/evidence && npm install \
		&& EVIDENCE_SOURCE__analytics__filename=../../../../local/analytics.db npm run sources \
		&& EVIDENCE_SOURCE__analytics__filename=../../../../local/analytics.db npm run dev

clean:
	rm -rf $(BIN) dist local coverage.out
