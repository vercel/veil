module github.com/vercel/veil

go 1.26.1

require (
	github.com/charmbracelet/lipgloss v1.1.0
	github.com/evanw/esbuild v0.28.0
	github.com/fastschema/qjs v0.0.6
	github.com/google/cel-go v0.28.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2
	github.com/stretchr/testify v1.11.1
	github.com/urfave/cli/v3 v3.6.2
	github.com/vercel/veil/api/go v0.0.0-00010101000000-000000000000
	golang.org/x/term v0.40.0
	gopkg.in/natefinch/lumberjack.v2 v2.2.1
)

require github.com/go-sourcemap/sourcemap v2.1.4+incompatible // indirect

require (
	buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go v1.36.11-20260209202127-80ab13bee0bf.1 // indirect
	google.golang.org/protobuf v1.36.11
)

replace github.com/vercel/veil/api/go => ./api/go

require (
	cel.dev/expr v0.25.1 // indirect
	github.com/antlr4-go/antlr/v4 v4.13.1 // indirect
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/charmbracelet/colorprofile v0.2.3-0.20250311203215-f60798e515dc // indirect
	github.com/charmbracelet/x/ansi v0.8.0 // indirect
	github.com/charmbracelet/x/cellbuf v0.0.13-0.20250311204145-2c3ea96c31dd // indirect
	github.com/charmbracelet/x/term v0.2.1 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/goccy/go-json v0.10.6
	github.com/lucasb-eyer/go-colorful v1.2.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.16 // indirect
	github.com/muesli/termenv v0.16.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/tetratelabs/wazero v1.9.0 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	golang.org/x/exp v0.0.0-20240823005443-9b4947da3948 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/text v0.22.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20240826202546-f6391c0de4c7 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240826202546-f6391c0de4c7 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
