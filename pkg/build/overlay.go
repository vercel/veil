package build

import (
	"fmt"
	"sort"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
)

// ShapeOverlayIf rewrites the `if` property under `metadata.overlays.items`
// so its `properties` enumerate the project's declared variables and
// `additionalProperties` is false. Without this, the proto-generated
// metadata schema accepts any string keys under `if` — a typo in an
// overlay rule (e.g. `evn` instead of `env`) would silently never
// match. Baking in the variable list pushes that error to schema
// validation time.
//
// Mutates metadataSchema in place. A no-op when the expected nested
// path is missing (defensive — keeps the helper safe to call even if
// the embedded schema shape evolves).
func ShapeOverlayIf(metadataSchema map[string]any, variables map[string]*veilv1.Variable) {
	properties, ok := nestedMap(metadataSchema, "properties", "overlays", "items", "properties")
	if !ok {
		return
	}

	names := make([]string, 0, len(variables))
	for name := range variables {
		names = append(names, name)
	}
	sort.Strings(names)

	props := make(map[string]any, len(names))
	for _, name := range names {
		props[name] = map[string]any{
			"type":        "string",
			"description": fmt.Sprintf("Regex matched against the stringified value of variable %q. The overlay applies only when every listed regex matches.", name),
		}
	}

	properties["if"] = map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"description":          "Per-variable regex conditions. Every entry's regex must match the named variable's stringified value for the overlay to apply. An empty object means the overlay always applies.",
		"properties":           props,
	}
}

// nestedMap descends through the given keys, returning the inner
// map[string]any if every step exists and is itself a map. Returns
// (nil, false) the moment a key is missing or not a map.
func nestedMap(m map[string]any, keys ...string) (map[string]any, bool) {
	cur := m
	for _, k := range keys {
		v, ok := cur[k]
		if !ok {
			return nil, false
		}
		next, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}
