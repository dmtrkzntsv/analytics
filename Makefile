BIN := twillingate
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test check vet build-all dist docker run smoke test-install test-compose test-restore dashboards seed-demo clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/twillingate

# GOPKGS excludes node_modules: some npm packages ship Go source that
# go list ./... would otherwise treat as part of this module.
GOPKGS = $(shell go list ./... | grep -v '/node_modules/')

test:
	go test -race $(GOPKGS)

vet:
	go vet $(GOPKGS)

# test-restore is in check because, unlike the docker-backed test-install and
# test-compose, it only needs sqlite3 and runs in a couple of seconds.
check: vet
	./scripts/coverage.sh
	./scripts/test-restore.sh

build-all:
	for target in linux/amd64 linux/arm64 linux/arm; do \
		GOOS=$${target%/*} GOARCH=$${target#*/} CGO_ENABLED=0 \
		go build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BIN)-$${target%/*}-$${target#*/} ./cmd/twillingate || exit 1; \
	done

# Release tarballs: binary + deploy/ (installer, units, configs) per arch,
# with a SHA256SUMS the installer verifies. This is what release.yml publishes.
dist: build-all
	@set -e; for arch in amd64 arm64 arm; do \
		stage=dist/stage-$$arch; rm -rf $$stage; mkdir -p $$stage; \
		cp dist/$(BIN)-linux-$$arch $$stage/$(BIN); \
		cp -r deploy $$stage/deploy; \
		cp .env.example $$stage/; \
		tar -czf dist/$(BIN)-linux-$$arch.tar.gz -C $$stage .; \
		rm -rf $$stage; \
		echo "packaged dist/$(BIN)-linux-$$arch.tar.gz"; \
	done
	cd dist && sha256sum $(BIN)-linux-*.tar.gz > SHA256SUMS

docker:
	docker build --target runtime -t twillingate:$(VERSION) .
	docker build --target evidence -t twillingate-evidence:$(VERSION) .

# ---- local development / testing ----

define LOCAL_ENV
LISTEN_ADDR=127.0.0.1:8080
DATABASE_DSN=sqlite://local/twillingate.db
GEO_DSN=none://
LOG_LEVEL=debug
LOG_FORMAT=text
BUFFER_FLUSH_INTERVAL=1s
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
# division of labour as systemd's EnvironmentFile= in production). Projects
# live in the database now, not a file: make sure a `dev` project exists
# (idempotent — a second run just fails quietly on the duplicate alias, hence
# the 2>/dev/null). Issue a key for it with `./twillingate key issue -project
# dev -label local`.
run: build local/.env
	set -a; . ./local/.env; set +a; \
	./$(BIN) project create -alias dev 2>/dev/null || true; \
	./$(BIN) serve

smoke: build
	./scripts/smoke.sh

test-install: build
	./scripts/test-install.sh

# Builds both images and runs the single-server compose stack end to end.
# Slow — a first Evidence build takes about a minute — so it is not part of
# `make check`.
test-compose:
	./scripts/test-compose.sh

# Runs restore.sh against a stubbed litestream and asserts a failed cycle
# never replaces the previous replica. Also part of `make check`.
test-restore:
	./scripts/test-restore.sh

# Fills local/twillingate.db with 180 days of believable traffic so the dashboards
# have something to plot, one profile per project in local/projects.json. Needs
# the server to have started once so those projects are registered. Re-running
# replaces the seeded rows rather than stacking another copy on top.
seed-demo: local/.env local/projects.json build
	@DATABASE_DSN="sqlite://$(PWD)/local/twillingate.db" \
	 PROJECTS_FILE="$(PWD)/local/projects.json" ./$(BIN) migrate
	python3 scripts/seed-demo.py local/twillingate.db local/projects.json
	@echo
	@echo "Cohorts are computed by the daily pass; run 'make run' once to"
	@echo "trigger the boot catch-up so the retention page has data."

dashboards:
	cd evidence && npm install \
		&& EVIDENCE_SOURCE__twillingate__filename=../../../local/twillingate.db npm run sources \
		&& EVIDENCE_SOURCE__twillingate__filename=../../../local/twillingate.db npm run dev

clean:
	rm -rf $(BIN) dist local coverage.out
