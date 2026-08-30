// Package docs embeds the normative documentation that the MCP endpoint
// serves as resources, and the helper scripts the collector serves next to
// its SDK. Embedding the file itself (rather than a copy) means the
// resource cannot drift from the doc: they are the same bytes.
package docs

import _ "embed"

//go:embed ingest-api.md
var IngestAPI string

// PlausibleShim keeps Plausible's class-based event tagging working after
// the tracker swap. internal/server hosts it at /js/plausible-shim.js so a
// migrating site loads it rather than copying it; plausible/README.md
// documents the same bytes.
//
//go:embed plausible/plausible-shim.js
var PlausibleShim []byte
