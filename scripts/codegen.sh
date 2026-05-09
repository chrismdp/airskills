#!/usr/bin/env bash
# Regenerate internal/apitypes/types.gen.go from the platform's OpenAPI spec.
#
# Source resolution:
#   * $PLATFORM_REPO set: read $PLATFORM_REPO/lib/openapi-zod.ts via npx tsx,
#     dump the spec, feed it to oapi-codegen. Errors out if the path is wrong
#     so a typo doesn't silently fall back to the stale fixture.
#   * $PLATFORM_REPO unset: use scripts/fixtures/openapi.json — this is the
#     CI path, and the regen-drift gate compares the generated output
#     against whatever the fixture currently says.
#
# Refresh procedure (run from the CLI repo root):
#   cp $PLATFORM_REPO/<dump-output> scripts/fixtures/openapi.json && \
#     bash scripts/codegen.sh
# Commit the fixture + types.gen.go together.

set -euo pipefail

cd "$(dirname "$0")/.."

SPEC_FILE=""
CLEANUP=""
trap '[[ -n "$CLEANUP" ]] && rm -f "$CLEANUP"' EXIT

if [[ -n "${PLATFORM_REPO:-}" ]]; then
  if [[ ! -f "$PLATFORM_REPO/lib/openapi-zod.ts" ]]; then
    echo "error: PLATFORM_REPO=$PLATFORM_REPO does not contain lib/openapi-zod.ts" >&2
    echo "       set PLATFORM_REPO to the platform repo root (where lib/openapi-zod.ts lives)" >&2
    exit 1
  fi
  SPEC_FILE="$(mktemp /tmp/airskills-openapi-XXXXXX.json)"
  CLEANUP="$SPEC_FILE"
  ( cd "$PLATFORM_REPO" && npx tsx -e "
    import { buildOpenApiSpecFromZod } from './lib/openapi-zod';
    const spec = buildOpenApiSpecFromZod({ baseUrl: 'https://airskills.ai', commit: 'fixture' });
    console.log(JSON.stringify(spec, null, 2));
  " ) > "$SPEC_FILE"
  echo "codegen: spec dumped from \$PLATFORM_REPO ($PLATFORM_REPO)" >&2
else
  SPEC_FILE="scripts/fixtures/openapi.json"
  if [[ ! -f "$SPEC_FILE" ]]; then
    echo "error: $SPEC_FILE missing — run with PLATFORM_REPO=... or commit a fixture" >&2
    exit 1
  fi
  echo "codegen: using fixture $SPEC_FILE" >&2
fi

# oapi-codegen doesn't yet support OpenAPI 3.1 nullable form
# (`type: [x, "null"]`). Pre-process the spec to the 3.0 equivalent
# (`type: x, "nullable": true`) so codegen sees a shape it understands.
# See https://github.com/oapi-codegen/oapi-codegen/issues/373.
SPEC_FILE_30="$(mktemp /tmp/airskills-openapi-30-XXXXXX.json)"
trap 'rm -f "$SPEC_FILE_30" "${CLEANUP:-}"' EXIT
python3 scripts/openapi-3.1-to-3.0.py "$SPEC_FILE" > "$SPEC_FILE_30"

# Pinned oapi-codegen version is captured in go.mod via tools/tools.go.
# `go run` resolves the version from go.sum so this is reproducible.
go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen \
  -config scripts/oapi-codegen.yaml \
  "$SPEC_FILE_30"

echo "codegen: wrote internal/apitypes/types.gen.go" >&2
