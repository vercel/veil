// Package variables resolves user-declared veil.json variables at render
// time, merging CLI flags, environment variables, and declared defaults.
package variables

import (
	"fmt"
	"strconv"
	"strings"

	veilv1 "github.com/vercel/veil/api/go/veil/v1"
	"github.com/vercel/veil/pkg/config"
)

// EnvPrefix is prepended (uppercased name appended) to look up variables from
// the process environment.
const EnvPrefix = "VEIL_VAR_"

// Resolve returns concrete typed values for every declared variable. Sources
// are consulted in precedence order:
//
//  1. cliPairs ("name=value") — highest priority
//  2. environment lookup via envGet(VEIL_VAR_<UPPER_NAME>)
//  3. the variable's declared Default
//
// A declared variable with no resolved value surfaces as an error naming it
// and the ways to provide it. CLI pairs referencing undeclared variables are
// rejected; environment variables that don't match a declaration are ignored
// (the env is often shared across projects).
func Resolve(decls map[string]*veilv1.Variable, cliPairs []string, envGet func(string) (string, bool)) (map[string]any, error) {
	cli, err := parseCLIVars(cliPairs)
	if err != nil {
		return nil, err
	}
	for name := range cli {
		if _, ok := decls[name]; !ok {
			return nil, fmt.Errorf("--var %q: variable not declared in veil.json", name)
		}
	}

	out := make(map[string]any, len(decls))
	for name, decl := range decls {
		enumVals, err := config.ParsedEnum(decl)
		if err != nil {
			return nil, fmt.Errorf("variable %q enum: %w", name, err)
		}

		if raw, ok := cli[name]; ok {
			v, err := coerce(decl.Type, raw)
			if err != nil {
				return nil, fmt.Errorf("--var %s: %w", name, err)
			}
			if err := checkEnum(name, v, enumVals, "--var "+name); err != nil {
				return nil, err
			}
			out[name] = v
			continue
		}
		if envGet != nil {
			envKey := EnvPrefix + strings.ToUpper(name)
			if raw, ok := envGet(envKey); ok {
				v, err := coerce(decl.Type, raw)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", envKey, err)
				}
				if err := checkEnum(name, v, enumVals, envKey); err != nil {
					return nil, err
				}
				out[name] = v
				continue
			}
		}
		if config.HasDefault(decl) {
			v, err := config.ParsedDefault(decl)
			if err != nil {
				return nil, fmt.Errorf("variable %q default: %w", name, err)
			}
			// Defaults are already validated against the enum at config
			// load time; no need to re-check here.
			out[name] = v
			continue
		}
		return nil, fmt.Errorf("required variable %q not provided (set --var %s=... or export %s%s=...)",
			name, name, EnvPrefix, strings.ToUpper(name))
	}
	return out, nil
}

// checkEnum rejects values that don't match one of the declared enum
// entries. source is used purely for error-message phrasing.
func checkEnum(name string, v any, enumVals []any, source string) error {
	if enumVals == nil {
		return nil
	}
	for _, allowed := range enumVals {
		if allowed == v {
			return nil
		}
	}
	return fmt.Errorf("%s: value %v not in declared enum %v", source, v, enumVals)
}

// parseCLIVars splits name=value pairs. The value may itself contain `=`.
func parseCLIVars(pairs []string) (map[string]string, error) {
	out := make(map[string]string, len(pairs))
	for _, p := range pairs {
		name, value, ok := strings.Cut(p, "=")
		if !ok {
			return nil, fmt.Errorf("invalid --var %q: expected name=value", p)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("invalid --var %q: empty name", p)
		}
		out[name] = value
	}
	return out, nil
}

// coerce converts a raw string (from CLI or env) into the declared type.
func coerce(t veilv1.VariableType_Enum, raw string) (any, error) {
	switch t {
	case veilv1.VariableType_string:
		return raw, nil
	case veilv1.VariableType_number:
		n, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("expected number, got %q", raw)
		}
		return n, nil
	case veilv1.VariableType_bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("expected bool, got %q", raw)
		}
		return b, nil
	default:
		return nil, fmt.Errorf("unknown variable type %q", t)
	}
}
