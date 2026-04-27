.PHONY: all build proto clean

all: clean proto build

build:
	go build -o ./veil cmd/veil/main.go

# Generate all protobuf outputs (Go + JSON Schema)
proto:
	buf generate
	go run ./scripts/deref-jsonschema/ api/jsonschema
	rm -rf pkg/embeds/jsonschema && cp -r api/jsonschema pkg/embeds/jsonschema
	cd api/go && go mod tidy

# Clean generated files
clean:
	rm -rf api/go/veil
	rm -rf api/jsonschema
	rm -rf pkg/embeds/jsonschema
