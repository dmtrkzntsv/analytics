# syntax=docker/dockerfile:1
#
# Two published targets from one file:
#   --target runtime  → twillingate            (serve, migrate, version)
#   --target evidence → twillingate-evidence   (dashboards)
#
# The ingestion image is the internet-facing one and carries nothing but the
# binary. Evidence needs a Node toolchain at *runtime* — it is a static site
# generator that bakes query results in at build time, so the site is rebuilt
# whenever the data changes — which is why it is packaged separately.

# Build stage must satisfy the toolchain floor in go.mod; bumping go.mod
# without bumping this tag fails the build rather than silently degrading.
# --platform=$BUILDPLATFORM pins this stage to the native runner and lets Go
# cross-compile to TARGET*, instead of QEMU emulating the compiler itself. The
# binary is CGO_ENABLED=0, so there is no C toolchain to cross-target. Building
# arm64/arm under emulation cost ~11 min per release against ~80s natively.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
ARG TARGETVARIANT
# GOARM takes the bare number ("7"), TARGETVARIANT the tag form ("v7").
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} GOARM=${TARGETVARIANT#v} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/twillingate ./cmd/twillingate

# @evidence-dev/sqlite depends on node-gyp's sqlite3, which publishes no musl
# prebuilds and therefore compiles here. Keyed on the lockfile so editing a
# dashboard page does not reinstall 988 packages; the compilers never reach a
# published image.
# Node 22, not 20: the Evidence build imports the node:sqlite builtin, which
# does not exist before 22 and fails the build with ERR_UNKNOWN_BUILTIN_MODULE.
FROM node:22-alpine AS evidence-build
RUN apk add --no-cache build-base python3
WORKDIR /opt/evidence
COPY evidence/package.json evidence/package-lock.json ./
RUN npm ci
COPY evidence/ ./

FROM alpine:3.20 AS runtime
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H -u 10001 twillingate
COPY --from=go-build /out/twillingate /usr/local/bin/twillingate
# Docker seeds a fresh named volume from the image directory, ownership
# included. Without this the volume mounts root-owned and the non-root
# process cannot create the database file on first boot.
RUN mkdir -p /var/lib/twillingate && chown twillingate:twillingate /var/lib/twillingate
VOLUME ["/var/lib/twillingate"]
USER twillingate
# Container defaults; override per-deployment via compose env_file/environment.
# LISTEN_ADDR binds all interfaces here (unlike the bare-metal loopback
# default) because published ports reach the container's own IP, not lo.
ENV LISTEN_ADDR=0.0.0.0:8080 \
    DATABASE_URL=sqlite:///var/lib/twillingate/twillingate.db
ENTRYPOINT ["/usr/local/bin/twillingate"]
CMD ["serve"]

FROM node:22-alpine AS evidence
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H -u 10001 twillingate
COPY --from=go-build /out/twillingate /usr/local/bin/twillingate
# Evidence writes .evidence/ and build/ inside the project, and the snapshot
# lands in the work directory; both must be writable by the non-root user.
# Ownership is set by COPY: a `chown -R` afterwards would write a second,
# full-size copy of the tree into its own layer and double the image.
COPY --from=evidence-build --chown=twillingate:twillingate /opt/evidence /opt/evidence
# DuckDB-wasm caches its autoloaded extensions under $HOME. The user has no
# home directory of its own, and without a writable one the parquet extension
# fails to install — leaving `evidence sources` to exit 0 having written no
# tables at all, so the build fails later with "Table does not exist".
ENV HOME=/opt/evidence/.home
RUN install -d -o twillingate -g twillingate /var/lib/dashboards "$HOME"
USER twillingate
# Warm the extension cache against the real schema. This turns a runtime
# dependency on extensions.duckdb.org into a build-time one, and fails the
# image build — rather than a deployment — if a source query does not match
# the migrations.
RUN set -eu; \
    DATABASE_URL=sqlite:///tmp/warm.db twillingate migrate; \
    cd /opt/evidence; \
    EVIDENCE_SOURCE__twillingate__filename=../../../../tmp/warm.db npm run sources; \
    rm -f /tmp/warm.db
ENV DASHBOARDS_ADDR=0.0.0.0:3000 \
    DASHBOARDS_PROJECT_DIR=/opt/evidence \
    DASHBOARDS_WORK_DIR=/var/lib/dashboards \
    DATABASE_URL=sqlite:///var/lib/twillingate/twillingate.db
ENTRYPOINT ["/usr/local/bin/twillingate"]
CMD ["dashboards"]
