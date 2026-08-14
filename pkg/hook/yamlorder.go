package hook

import (
	"bytes"
	"sort"

	yaml "gopkg.in/yaml.v3"
)

// keyOrderGroups are manifest-key orderings applied on top of yaml.v3's
// default map-key order (plain alphabetical). Each group is an ordered list
// of keys; for a given map, whichever of a group's keys are present get
// gathered into a contiguous block, in the group's order, spliced in at the
// position where the earliest one of them already sits (see
// applyOrderGroup) — every other key keeps its current relative order.
//
// Groups and their order are chosen to minimize reordering churn against
// the ~450 hand-maintained services/*/_infra/*.yaml files already in the
// api monorepo, not to reproduce "textbook" Kubernetes API declaration
// order — measured empirically (walk every hand-authored manifest, compare
// its authored key order against each candidate group applied). A group
// only earns a place here if it drives observed churn under ~15%; several
// evidence-backed candidates were measured and dropped because no ordering
// of their keys does better than ~50-100% churn — the group's keys are
// routinely interleaved with untouched sibling fields in real files, so any
// fixed order disturbs most of them regardless (Container's name/image/etc:
// two roughly-equal-sized conventions contradict each other, ~54% churn
// either way; DeploymentSpec, CronJobSpec, Probe: the group's keys are
// almost never contiguous in a real file, ~100% churn regardless of order).
//
// Groups are path-agnostic: they fire on sibling key names wherever they
// co-occur, so e.g. the PodSpec group covers a Deployment's
// spec.template.spec, a CronJob's spec.jobTemplate.spec.template.spec, a
// bare Job, DaemonSet, StatefulSet, etc. without needing to know which
// workload kind or nesting depth produced the map.
var keyOrderGroups = [][]string{
	// PodSpec: initContainers run to completion before containers start,
	// so list them first. 94% of hand-authored pod specs with both keys
	// already order them this way, and the pair sits contiguous at the
	// front of the pod spec in the overwhelming majority of files — ~17%
	// churn. (volumes is declared before both in the true k8s.io/api
	// struct order, but in practice it's almost always authored much
	// later, alongside restartPolicy/serviceAccountName/etc — adding it
	// to this group would drag it forward through everything in between
	// on nearly every file, so it's deliberately left out.)
	{"initContainers", "containers"},

	// ObjectMeta: at the top (Deployment/CronJob/etc's own metadata),
	// name/namespace are what's typically present and labels leads 88%
	// of the time; on a pod template's metadata (where Datadog
	// annotations live), annotations leads 68% of the time. Because the
	// two contexts have almost disjoint typical keysets, one ordered list
	// serves both: ~5% churn at the top level, ~32% at the pod-template
	// level (a genuine ~68/32 split in this fleet — not resolvable by any
	// single fixed order, but still better than the ~35%/~67% a
	// name-first "textbook" order would cost).
	{"annotations", "labels", "name", "namespace"},

	// VolumeMount: mountPath-then-name is what's actually authored 73%
	// of the time (plus another 26% as mountPath, name, readOnly) — ~1%
	// churn. The "textbook" name-first order costs ~100% churn here.
	{"mountPath", "name", "readOnly", "subPath"},

	// ResourceRequirements: limits-before-requests already matches both
	// the true API order and 98% of authored resource blocks — ~1% churn.
	{"limits", "requests", "claims"},

	// JobSpec (CronJob's embedded job template only — 36 instances in
	// this fleet): low blast radius either way; ~11% churn, kept because
	// the cost of being wrong is at most a handful of files.
	{"parallelism", "completions", "selector", "template"},
}

// orderedYAMLMarshal marshals v the same way yaml.v3's default
// map[string]any encoding does (2-space indent, alphabetized keys) except
// for the keys named in keyOrderGroups, which are reordered per group. Every
// key not named in any group keeps its normal alphabetical position, so this
// only changes behavior for the specific overrides above.
func orderedYAMLMarshal(v any) ([]byte, error) {
	node, err := orderedNode(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(node); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// orderedNode recursively converts a decoded `any` (as produced by
// encoding/json's Unmarshal into `any` — map[string]any, []any, or a scalar)
// into a *yaml.Node tree, applying orderedMapKeys to every map along the
// way. A yaml.Node's mapping content is emitted in exactly the order it was
// appended, unlike map[string]any which yaml.v3 always alphabetizes on
// Marshal — building this tree by hand is what lets us override specific
// key groups without losing yaml.v3's default ordering for everything else.
func orderedNode(v any) (*yaml.Node, error) {
	switch t := v.(type) {
	case map[string]any:
		node := &yaml.Node{Kind: yaml.MappingNode}
		for _, k := range orderedMapKeys(t) {
			valNode, err := orderedNode(t[k])
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k},
				valNode,
			)
		}
		return node, nil
	case []any:
		node := &yaml.Node{Kind: yaml.SequenceNode}
		for _, item := range t {
			itemNode, err := orderedNode(item)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, itemNode)
		}
		return node, nil
	default:
		// Scalars (string/number/bool/nil) and any other type yaml.v3
		// knows how to encode: round-trip through a fresh Node's own
		// Encode so its scalar tagging/quoting rules (numeric-looking
		// strings, bools, null, etc.) match the default path exactly.
		var node yaml.Node
		if err := node.Encode(t); err != nil {
			return nil, err
		}
		return &node, nil
	}
}

// orderedMapKeys returns m's keys alphabetically, then applies
// keyOrderGroups on top. This is the single point where map key order is
// decided; every other yaml.v3 default (indent, scalar quoting, sequence
// style) is left untouched.
func orderedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, group := range keyOrderGroups {
		keys = applyOrderGroup(keys, group)
	}
	return keys
}

// applyOrderGroup gathers whichever of group's keys are present in keys
// into one contiguous block, in the group's specified order, and splices
// that block in at the position where the earliest matched key already
// sits — every other key keeps its current relative position, undisturbed.
//
// A no-op whenever at most one of the group's keys is present: with only
// one match, removing it and re-inserting it at its own original spot
// reconstructs the input exactly. This matters in practice — most groups
// here name keys that usually appear alone in a given map (e.g. most pod
// specs have containers but no initContainers) — so a group only changes
// anything for the map shapes it's actually meant to touch.
func applyOrderGroup(keys []string, group []string) []string {
	inGroup := make(map[string]bool, len(group))
	for _, k := range group {
		inGroup[k] = true
	}

	matched := make([]string, 0, len(group))
	matchedSet := make(map[string]bool, len(group))
	firstIdx := -1
	for i, k := range keys {
		if !inGroup[k] {
			continue
		}
		if firstIdx == -1 {
			firstIdx = i
		}
	}
	if firstIdx == -1 {
		return keys
	}
	for _, k := range group {
		if !matchedSet[k] {
			for _, existing := range keys {
				if existing == k {
					matched = append(matched, k)
					matchedSet[k] = true
					break
				}
			}
		}
	}
	if len(matched) < 2 {
		return keys
	}

	others := make([]string, 0, len(keys)-len(matched))
	insertAt := 0
	for i, k := range keys {
		if matchedSet[k] {
			continue
		}
		if i < firstIdx {
			insertAt++
		}
		others = append(others, k)
	}

	out := make([]string, 0, len(keys))
	out = append(out, others[:insertAt]...)
	out = append(out, matched...)
	out = append(out, others[insertAt:]...)
	return out
}
