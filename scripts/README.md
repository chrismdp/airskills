# scripts/

Build-time tooling. Nothing here ships in the user-facing binary.

## codegen.sh — regenerate `internal/apitypes/`

Generates Go response/request types from the platform's OpenAPI spec.
The CI gate (`.github/workflows/ci.yml`) re-runs this against the
committed fixture and fails if `internal/apitypes/` is dirty — so any
spec change must come with a fresh fixture and a fresh `types.gen.go`.

### Refresh procedure (after a platform spec change)

From the CLI repo root, with the platform repo cloned alongside:

```sh
PLATFORM_REPO=../platform bash scripts/codegen.sh
cp <generated-snapshot> scripts/fixtures/openapi.json   # done by codegen.sh into a temp file
```

Easier: have the script also refresh the fixture when `$PLATFORM_REPO`
is set. (Today it doesn't — refresh is manual to keep the snapshot
diff readable in PRs. Worth automating if drift becomes routine.)

Then commit `scripts/fixtures/openapi.json` and
`internal/apitypes/types.gen.go` together.

### Why two source modes

- **`$PLATFORM_REPO` set** — uses `npx tsx` against the platform's
  `lib/openapi-zod.ts` so contributors with the platform repo can see
  the live shape immediately. Errors out if the path is wrong (no
  silent fallback).
- **`$PLATFORM_REPO` unset** — uses the committed fixture under
  `scripts/fixtures/openapi.json`. This is the CI path, and the path
  every external contributor takes — they can regenerate without
  needing platform repo access.

### oapi-codegen pinning

Version is pinned via the blank import in `tools/tools.go`, captured
in `go.sum`. `go run github.com/...@vX.Y.Z` resolves through the
module cache so output is reproducible. Bump deliberately, never
auto-bump.

### `openapi-3.1-to-3.0.py`

oapi-codegen doesn't yet support OpenAPI 3.1 nullable form
(`type: [x, "null"]`). The platform spec is 3.1; this script
down-converts it to 3.0-equivalent shapes immediately before codegen
runs. Two transformations:

- `type: ["X", "null"]` → `type: "X"` + `nullable: true`
- `allOf: [{$ref}, {nullable marker}]` → `{$ref, nullable: true}`
  (the canonical 3.0 nullable-ref idiom — off-spec strictly, but
  what oapi-codegen and other 3.0 generators expect)

Drop this script when oapi-codegen ships 3.1 support
(https://github.com/oapi-codegen/oapi-codegen/issues/373).
