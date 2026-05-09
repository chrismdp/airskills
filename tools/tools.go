//go:build tools

// Package tools tracks build-time tool dependencies in go.mod so the
// pinned version is captured in go.sum. Never imported by runtime code.
//
// The blank import below pins oapi-codegen — invoked by scripts/codegen.sh
// to regenerate internal/apitypes/types.gen.go from the OpenAPI spec.
// Bump deliberately; do not auto-bump.
package tools

import (
	_ "github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen"
)
