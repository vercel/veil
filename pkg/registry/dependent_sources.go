package registry

import (
	"net/url"
	"strings"
)

// DependencySourceID is the stable bundle key and default destination for a
// dependent template. Keep original path spelling: cleaning would alias sources.
func DependencySourceID(kind, name, source string) string {
	escape := func(s string) string {
		if s == "." || s == ".." {
			return strings.ReplaceAll(s, ".", "%2E")
		}
		return url.PathEscape(s)
	}
	return "dependencies/" + escape(kind) + "/" + escape(name) + "/" + escape(source)
}
