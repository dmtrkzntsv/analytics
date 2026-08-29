// Package docs embeds the normative documentation that the MCP endpoint
// serves as resources. Embedding the file itself (rather than a copy)
// means the resource cannot drift from the doc: they are the same bytes.
package docs

import _ "embed"

//go:embed ingest-api.md
var IngestAPI string
