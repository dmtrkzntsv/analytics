# syntax=docker/dockerfile:1
# Build stage must satisfy the toolchain floor in go.mod; bumping go.mod
# without bumping this tag fails the build rather than silently degrading.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/analytics ./cmd/analytics

FROM litestream/litestream:0.3.13 AS litestream

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H -u 10001 analytics
COPY --from=build /out/analytics /usr/local/bin/analytics
COPY --from=litestream /usr/local/bin/litestream /usr/local/bin/litestream
# Docker seeds a fresh named volume from the image directory, ownership
# included. Without this the volume mounts root-owned and the non-root
# process cannot create the database file on first boot.
RUN mkdir -p /var/lib/analytics && chown analytics:analytics /var/lib/analytics
VOLUME ["/var/lib/analytics"]
USER analytics
ENTRYPOINT ["/usr/local/bin/analytics"]
CMD ["serve", "-config", "/etc/analytics/config.json"]
