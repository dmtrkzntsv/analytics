// Package docs embeds the normative documentation that the MCP endpoint
// serves as resources, and the helper scripts the collector serves next to
// its SDK. Embedding the file itself (rather than a copy) means the
// resource cannot drift from the doc: they are the same bytes.
package docs

import _ "embed"

// The two normative documents, split on audience: Twillingate is what an
// agent needs to set up a project, get it tracking and answer questions
// from the data; Deployment is what an operator needs to run the collector
// on their own server. Both are served over MCP, so the bytes an agent
// reads are the bytes a person reads. docs_sync_test.go binds
// Twillingate's reserved-key and tool lists to the source they describe.
//
//go:embed twillingate.md
var Twillingate string

//go:embed deployment.md
var Deployment string

// PlausibleShim keeps Plausible's class-based event tagging working after
// the tracker swap. internal/server hosts it at /js/plausible-shim.js so a
// migrating site loads it rather than copying it; plausible/README.md
// documents the same bytes.
//
//go:embed plausible/plausible-shim.js
var PlausibleShim []byte
