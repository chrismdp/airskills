// Package apitypes holds Go types generated from the platform's
// OpenAPI spec. Single source of truth: the Zod schemas in
// platform/lib/openapi-zod.ts. Regenerate after a spec change with:
//
//	bash scripts/codegen.sh                       # CI path: uses fixtures/openapi.json
//	PLATFORM_REPO=../platform bash scripts/codegen.sh   # dev path: dumps live spec
//
// The CI regen-drift gate (in .github/workflows/ci.yml) re-runs codegen
// against the committed fixture and fails if internal/apitypes/ is dirty,
// so any spec change forces a fixture refresh.
//
//go:generate bash ../../scripts/codegen.sh
package apitypes
