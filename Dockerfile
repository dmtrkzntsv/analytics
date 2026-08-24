# syntax=docker/dockerfile:1
#
# Two published targets from one file:
#   --target runtime  → analytics            (serve, migrate, version)
#   --target evidence → analytics-evidence   (dashboards)
#
# The ingestion image is the internet-facing one and carries nothing but the
# binary. Evidence needs a Node toolchain at *runtime* — it is a static site
# generator that bakes query results in at build time, so the site is rebuilt
# whenever the data changes — which is why it is packaged separately.

# Build stage must satisfy the toolchain floor in go.mod; bumping go.mod
# without bumping this tag fails the build rather than silently degrading.
FROM golang:1.25-alpine AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/analytics ./cmd/analytics

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
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H -u 10001 analytics
COPY --from=go-build /out/analytics /usr/local/bin/analytics
# Docker seeds a fresh named volume from the image directory, ownership
# included. Without this the volume mounts root-owned and the non-root
# process cannot create the database file on first boot.
RUN mkdir -p /var/lib/analytics && chown analytics:analytics /var/lib/analytics
VOLUME ["/var/lib/analytics"]
USER analytics
# Container defaults; override per-deployment via compose env_file/environment.
# LISTEN_ADDR binds all interfaces here (unlike the bare-metal loopback
# default) because published ports reach the container's own IP, not lo.
ENV LISTEN_ADDR=0.0.0.0:8080 \
    DATABASE_URL=sqlite:///var/lib/analytics/analytics.db \
    PROJECTS_FILE=/etc/analytics/projects.json
ENTRYPOINT ["/usr/local/bin/analytics"]
CMD ["serve"]

FROM node:22-alpine AS evidence
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H -u 10001 analytics
COPY --from=go-build /out/analytics /usr/local/bin/analytics
# Evidence writes .evidence/ and build/ inside the project, and the snapshot
# lands in the work directory; both must be writable by the non-root user.
# Ownership is set by COPY: a `chown -R` afterwards would write a second,
# full-size copy of the tree into its own layer and double the image.
COPY --from=evidence-build --chown=analytics:analytics /opt/evidence /opt/evidence
RUN install -d -o analytics -g analytics /var/lib/dashboards
USER analytics
ENV DASHBOARDS_ADDR=0.0.0.0:3000 \
    DASHBOARDS_PROJECT_DIR=/opt/evidence \
    DASHBOARDS_WORK_DIR=/var/lib/dashboards \
    DATABASE_URL=sqlite:///var/lib/analytics/analytics.db
ENTRYPOINT ["/usr/local/bin/analytics"]
CMD ["dashboards"]
