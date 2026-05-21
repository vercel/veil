package protoencode

// ExpandHookShorthand normalizes the bare-string entries authors may
// write inside a HooksDefinition map.
//
// Both kind.yaml and resource yaml accept either form for `render` /
// `validate` / `post_render` arrays:
//
//	render:
//	  - ./hooks/foo.ts                  # bare string shorthand
//	  - path: ./hooks/bar.ts            # full proto-defined object
//	    access:
//	      env:
//	        - { name: TOKEN, description: ... }
//
// protojson rejects the bare-string form, so callers that read the
// document into a generic map first call ExpandHookShorthand on the
// `hooks` block, then marshal back to JSON before unmarshalling into
// the proto type.
//
// `hooks` is the HooksDefinition-shaped sub-map (top-level for kinds,
// metadata.hooks for resources). Mutates in place; safe to call when
// hooks is nil or the arrays are missing / empty.
func ExpandHookShorthand(hooks map[string]any) {
	if hooks == nil {
		return
	}
	for _, key := range []string{"render", "validate", "post_render"} {
		arr, ok := hooks[key].([]any)
		if !ok {
			continue
		}
		for i, entry := range arr {
			if s, ok := entry.(string); ok {
				arr[i] = map[string]any{"path": s}
			}
		}
		hooks[key] = arr
	}
}
