// Package version carries the build version, injected at link time:
//
//	-ldflags "-X github.com/dmtrkzntsv/twillingate/internal/version.Version=v26.830.14"
//
// It lives outside main so served assets (the twillingate.js banner) can
// carry the released version too, not only the `version` subcommand.
package version

var Version = "dev"
