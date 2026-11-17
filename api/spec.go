// Package api holds the client API's contract, api/openapi.yaml, for the server
// that checks every request against it (internal/controlplane/clientapi).
//
// The file is embedded as it is written instead of oapi-codegen's
// embedded-spec, a gzipped copy in the generated code that every change to
// the contract rewrote whole, so that any two changes to it conflicted
// (issue #754).
package api

import _ "embed"

// Spec is api/openapi.yaml.
//
//go:embed openapi.yaml
var Spec []byte
