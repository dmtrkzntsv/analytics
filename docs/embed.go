// Package docs embeds the normative documentation that the MCP endpoint
// serves as resources, and the helper scripts the collector serves next to
// its SDK. Embedding the file itself (rather than a copy) means the
// resource cannot drift from the doc: they are the same bytes.
package docs

import _ "embed"

// Twillingate is the single normative document: install, configure,
// instrument, connect, query and operate. The MCP endpoint serves it as
// docs://twillingate, so the bytes an agent reads are the bytes a person
// reads. docs_sync_test.go binds its reserved-key list to the reservedKeys
// map in internal/server.
//
//go:embed twillingate.md
var Twillingate string

// PlausibleShim keeps Plausible's class-based event tagging working after
// the tracker swap. internal/server hosts it at /js/plausible-shim.js so a
// migrating site loads it rather than copying it; plausible/README.md
// documents the same bytes.
//
//go:embed plausible/plausible-shim.js
var PlausibleShim []byte
