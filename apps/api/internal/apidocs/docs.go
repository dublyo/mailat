package apidocs

import _ "embed"

// Spec is compiled into the binary so containers cannot omit documentation.
//
//go:embed openapi.json
var Spec []byte
