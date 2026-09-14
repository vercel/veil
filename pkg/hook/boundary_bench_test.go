package hook

import (
	"fmt"
	"strings"
	"testing"

	"github.com/fastschema/qjs"
	"github.com/goccy/go-json"
)

// How should a JS-resident object reach Go for schema validation? Three
// ways, all measured the way the runtime actually works — the loop runs
// inside QuickJS, because a hook calls validateSchema from JS, not the
// other way round.
//
//	js-stringify    what veil does today: JS calls JSON.stringify and
//	                hands Go the string, Go unmarshals it.
//	host-stringify  JS hands Go the object, Go calls Value.JSONStringify
//	                (still QuickJS's serializer, driven from Go) and
//	                unmarshals.
//	host-walk       JS hands Go the object, Go walks it into a
//	                map[string]any with no JSON in the middle.
//
// Go's own json.Marshal is not a fourth option: it cannot see a value
// living in the QuickJS heap, so something has to cross the Wasm
// boundary first. That crossing is what these measure.
//
// Each host function takes its argument through Context().Function
// rather than FuncToJS, since FuncToJS converts every argument to a Go
// value before the body runs — which is the very cost host-walk is
// meant to isolate.

func benchObject(fields, depth int) string {
	var b strings.Builder
	b.WriteString("{")
	for i := range fields {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"field%d":"value-%d"`, i, i)
	}
	if depth > 0 {
		fmt.Fprintf(&b, `,"nested":%s`, benchObject(fields, depth-1))
	}
	b.WriteString("}")
	return b.String()
}

var boundaryCases = []struct {
	name   string
	fields int
	depth  int
}{
	{"small", 5, 0},   // a typical source doc
	{"medium", 20, 2}, // a chunky config
	{"large", 60, 4},  // a deployment manifest
}

// runBoundary installs sink as __sink, then runs call in a JS loop of
// b.N iterations — one Eval for the whole benchmark, so script
// compilation is not charged per operation.
func runBoundary(b *testing.B, src, call string, sink qjs.Function) {
	b.Helper()
	rt, err := qjs.New()
	if err != nil {
		b.Fatalf("runtime: %v", err)
	}
	defer rt.Close()

	setup, err := rt.Eval("bench.js", qjs.Code("globalThis.__obj = "+src+";"))
	if err != nil {
		b.Fatalf("eval: %v", err)
	}
	setup.Free()

	global := rt.Context().Global()
	global.SetPropertyStr("__sink", rt.Context().Function(sink))
	global.Free()

	b.ReportAllocs()
	b.ResetTimer()
	out, err := rt.Eval("call.js", qjs.Code(
		fmt.Sprintf("for (let i = 0; i < %d; i++) { %s }", b.N, call)))
	if err != nil {
		b.Fatalf("call: %v", err)
	}
	b.StopTimer()
	out.Free()
}

func BenchmarkBoundaryJSStringify(b *testing.B) {
	for _, tc := range boundaryCases {
		b.Run(tc.name, func(b *testing.B) {
			var sink any
			runBoundary(b, benchObject(tc.fields, tc.depth), "__sink(JSON.stringify(globalThis.__obj));",
				func(this *qjs.This) (*qjs.Value, error) {
					if err := json.Unmarshal([]byte(this.Args()[0].String()), &sink); err != nil {
						return nil, err
					}
					return this.Context().NewNull(), nil
				})
		})
	}
}

func BenchmarkBoundaryHostStringify(b *testing.B) {
	for _, tc := range boundaryCases {
		b.Run(tc.name, func(b *testing.B) {
			var sink any
			runBoundary(b, benchObject(tc.fields, tc.depth), "__sink(globalThis.__obj);",
				func(this *qjs.This) (*qjs.Value, error) {
					text, err := this.Args()[0].JSONStringify()
					if err != nil {
						return nil, err
					}
					if err := json.Unmarshal([]byte(text), &sink); err != nil {
						return nil, err
					}
					return this.Context().NewNull(), nil
				})
		})
	}
}

func BenchmarkBoundaryHostWalk(b *testing.B) {
	for _, tc := range boundaryCases {
		b.Run(tc.name, func(b *testing.B) {
			runBoundary(b, benchObject(tc.fields, tc.depth), "__sink(globalThis.__obj);",
				func(this *qjs.This) (*qjs.Value, error) {
					if _, err := qjs.ToGoValue[map[string]any](this.Args()[0]); err != nil {
						return nil, err
					}
					return this.Context().NewNull(), nil
				})
		})
	}
}
