package stacks

import _ "embed"

// ManifestSchema is the JSON Schema of a version 8 homelab-stack.json, for
// editors: field docs, completion and early marks. ParseManifest and
// Validate stay the authority; the schema leaves out the legacy fields
// (storage, secrets) and the rules that span fields.
//
//go:embed manifest.schema.json
var ManifestSchema []byte
