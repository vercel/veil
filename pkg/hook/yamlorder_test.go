package hook

import "encoding/json"

// decodeJSON round-trips a JSON literal through json.Unmarshal into `any`,
// mirroring exactly what stringifyFn receives from the JS side (a JSON
// string) before handing it to orderedYAMLMarshal.
func (s *HookSuite) decodeJSON(jsonStr string) any {
	var v any
	s.Require().NoError(json.Unmarshal([]byte(jsonStr), &v))
	return v
}

func (s *HookSuite) TestOrderedYAMLMarshalOrdersInitContainersBeforeContainers() {
	v := s.decodeJSON(`{
		"affinity": {},
		"containers": [{"name": "app"}],
		"dnsPolicy": "ClusterFirst",
		"imagePullSecrets": [{"name": "reg"}],
		"initContainers": [{"name": "migrate"}],
		"nodeSelector": {}
	}`)
	out, err := orderedYAMLMarshal(v)
	s.Require().NoError(err)
	// initContainers/containers are spliced in at the position of the
	// earliest matched key (here, containers — the first of the two to
	// appear alphabetically) — every other key keeps its position.
	s.Equal(`affinity: {}
initContainers:
  - name: migrate
containers:
  - name: app
dnsPolicy: ClusterFirst
imagePullSecrets:
  - name: reg
nodeSelector: {}
`, string(out))
}

func (s *HookSuite) TestOrderedYAMLMarshalIsPathAgnostic() {
	// A CronJob nests its pod spec under
	// spec.jobTemplate.spec.template.spec — the group must fire there
	// too, with no path-specific knowledge.
	v := s.decodeJSON(`{
		"spec": {
			"jobTemplate": {
				"spec": {
					"template": {
						"spec": {
							"containers": [{"name": "job"}],
							"initContainers": [{"name": "setup"}]
						}
					}
				}
			}
		}
	}`)
	out, err := orderedYAMLMarshal(v)
	s.Require().NoError(err)
	s.Equal(`spec:
  jobTemplate:
    spec:
      template:
        spec:
          initContainers:
            - name: setup
          containers:
            - name: job
`, string(out))
}

func (s *HookSuite) TestOrderedYAMLMarshalLeavesLonePodSpecKeyUntouched() {
	// Only containers is present (no initContainers) — the overwhelming
	// majority case. A lone matched key is a true no-op: it's left
	// exactly where it already was, not relocated.
	v := s.decodeJSON(`{"affinity": {}, "containers": [{"name": "app"}], "dnsPolicy": "ClusterFirst"}`)
	out, err := orderedYAMLMarshal(v)
	s.Require().NoError(err)
	s.Equal(`affinity: {}
containers:
  - name: app
dnsPolicy: ClusterFirst
`, string(out))
}

func (s *HookSuite) TestOrderedYAMLMarshalOrdersObjectMetaFields() {
	v := s.decodeJSON(`{"annotations": {"a": "b"}, "labels": {"c": "d"}, "name": "svc", "namespace": "default"}`)
	out, err := orderedYAMLMarshal(v)
	s.Require().NoError(err)
	s.Equal(`annotations:
  a: b
labels:
  c: d
name: svc
namespace: default
`, string(out))
}

func (s *HookSuite) TestOrderedYAMLMarshalOrdersVolumeMountFields() {
	v := s.decodeJSON(`{"mountPath": "/data", "name": "data", "readOnly": true}`)
	out, err := orderedYAMLMarshal(v)
	s.Require().NoError(err)
	s.Equal("mountPath: /data\nname: data\nreadOnly: true\n", string(out))
}

func (s *HookSuite) TestOrderedYAMLMarshalLeavesUnrelatedKeysAlphabetical() {
	v := s.decodeJSON(`{"a": 1, "b": ["xx", "yy"], "z": true}`)
	out, err := orderedYAMLMarshal(v)
	s.Require().NoError(err)
	s.Equal("a: 1\nb:\n  - xx\n  - yy\nz: true\n", string(out))
}

func (s *HookSuite) TestApplyOrderGroupNoOpWhenNoKeysPresent() {
	s.Equal([]string{"a", "b"}, applyOrderGroup([]string{"a", "b"}, []string{"x", "y", "z"}))
}

func (s *HookSuite) TestApplyOrderGroupNoOpWhenOnlyOneKeyPresent() {
	// A lone match reconstructs the input exactly — removing it and
	// re-inserting it at its own position is a true no-op, regardless of
	// where in keys it sits.
	s.Equal([]string{"a", "b", "c"}, applyOrderGroup([]string{"a", "b", "c"}, []string{"c"}))
	s.Equal([]string{"a", "b", "c"}, applyOrderGroup([]string{"a", "b", "c"}, []string{"a"}))
}

func (s *HookSuite) TestApplyOrderGroupSplicesContiguousMatchesInPlace() {
	// containers/initContainers are already adjacent (positions 1,3) —
	// splicing them at the earliest match's position (1) leaves "x" and
	// "y" exactly where they were.
	got := applyOrderGroup(
		[]string{"x", "containers", "y", "initContainers", "z"},
		[]string{"initContainers", "containers"},
	)
	s.Equal([]string{"x", "initContainers", "containers", "y", "z"}, got)
}

func (s *HookSuite) TestApplyOrderGroupDoesNotDragADistantMatchForward() {
	// "far" is present but nowhere near the other two matches — splicing
	// still only disturbs the keys between the earliest match and where
	// "far" used to be; keys before the earliest match are untouched.
	got := applyOrderGroup(
		[]string{"before", "b", "middle1", "middle2", "a", "far"},
		[]string{"a", "b", "far"},
	)
	s.Equal([]string{"before", "a", "b", "far", "middle1", "middle2"}, got)
}

func (s *HookSuite) TestApplyOrderGroupIgnoresGroupKeysAbsentFromKeys() {
	got := applyOrderGroup(
		[]string{"replicas", "selector", "template"},
		[]string{"replicas", "selector", "template", "strategy"},
	)
	s.Equal([]string{"replicas", "selector", "template"}, got)
}
