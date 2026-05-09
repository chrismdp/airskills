#!/usr/bin/env python3
"""Down-convert an OpenAPI 3.1 spec into 3.0-compatible form for tools
that don't yet handle 3.1 (notably oapi-codegen — see issue #373).

Two transformations:
  * `"type": ["X", "null"]` (3.1 nullable) → `"type": "X", "nullable": true`
    (3.0 nullable). Order-independent and supports the inverse pair.
  * Top-level `openapi: "3.1.x"` → `openapi: "3.0.3"`.

Reads JSON from argv[1] (or stdin), writes JSON to stdout. Recurses
through every nested object/array — nullable fields can appear anywhere
in `components.schemas`, request bodies, and response schemas.
"""

import json
import sys


def _is_nullable_marker(d):
    """An allOf entry is a 'nullable marker' if it just sets nullable on
    a base type — used by zod-to-openapi to express `Schema.nullable()`
    against a $ref. Pattern: `{type: "object", nullable: true}` plus
    optional `description`.
    """
    if not isinstance(d, dict):
        return False
    keys = set(d.keys()) - {"description"}
    return d.get("nullable") is True and keys.issubset({"type", "nullable"})


def transform(node):
    if isinstance(node, dict):
        # Step 1: 3.1 type-array → 3.0 nullable on the local node.
        t = node.get("type")
        if isinstance(t, list) and "null" in t:
            non_null = [x for x in t if x != "null"]
            if len(non_null) == 1:
                node["type"] = non_null[0]
                node["nullable"] = True
            else:
                # Multi-type union (rare). Fall back to the first concrete
                # type and mark nullable — closest 3.0 representation.
                node["type"] = non_null[0] if non_null else "string"
                node["nullable"] = True

        # Step 2: recurse into children FIRST so nested 3.1 type-arrays
        # become {nullable: true} before the allOf-collapse heuristic
        # below runs (which needs to see them in 3.0 shape).
        node = {k: transform(v) for k, v in node.items()}

        # Step 3: collapse allOf-with-nullable-marker into $ref+nullable.
        # zod-to-openapi emits `Ref.nullable()` as
        #   {allOf: [{$ref: ...}, {type: "object", nullable: true, ...}]}
        # which oapi-codegen rejects with 'merging two schemas with
        # different Nullable'. Collapsing to `{$ref, nullable: true}`
        # gives oapi-codegen the pattern it actually handles. $ref
        # siblings are off-spec in strict OpenAPI 3.0 but oapi-codegen
        # accepts this idiom (it's the canonical 3.0 nullable-ref shape
        # that other generators emit too).
        all_of = node.get("allOf")
        if isinstance(all_of, list) and len(all_of) == 2:
            ref_part = next((x for x in all_of if isinstance(x, dict) and "$ref" in x), None)
            null_part = next((x for x in all_of if _is_nullable_marker(x)), None)
            if ref_part is not None and null_part is not None and ref_part is not null_part:
                rebuilt = {k: v for k, v in node.items() if k != "allOf"}
                rebuilt["$ref"] = ref_part["$ref"]
                rebuilt["nullable"] = True
                # Description: outer node wins; otherwise the marker's
                # description (which carried the field's docstring under
                # zod-to-openapi's allOf form).
                if "description" not in rebuilt and "description" in null_part:
                    rebuilt["description"] = null_part["description"]
                return rebuilt

        return node
    if isinstance(node, list):
        return [transform(x) for x in node]
    return node


def main() -> int:
    src = open(sys.argv[1], "r") if len(sys.argv) > 1 else sys.stdin
    spec = json.load(src)
    if isinstance(spec.get("openapi"), str) and spec["openapi"].startswith("3.1"):
        spec["openapi"] = "3.0.3"
    spec = transform(spec)
    json.dump(spec, sys.stdout, indent=2)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
