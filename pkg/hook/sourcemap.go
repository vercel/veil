package hook

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-sourcemap/sourcemap"
)

// extractInlineSourcemap pulls the base64-encoded source map appended by
// esbuild's `Sourcemap: api.SourceMapInline` mode and returns a parsed
// Consumer. Returns nil when no inline map is present (e.g. raw IIFE
// strings used in tests).
func extractInlineSourcemap(code string) *sourcemap.Consumer {
	const marker = "//# sourceMappingURL=data:application/json;base64,"
	idx := strings.LastIndex(code, marker)
	if idx < 0 {
		return nil
	}
	payload := strings.TrimRight(code[idx+len(marker):], "\r\n\t ")
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil
	}
	c, err := sourcemap.Parse("hook.js", raw)
	if err != nil {
		return nil
	}
	return c
}

// stackFrameRE matches QuickJS-NG stack frames of the shape
// `    at <name> (<file>:<line>:<col>)`. The leading whitespace is part
// of the match so we only rewrite real frames, not message text that
// happens to contain a colon-separated number pair.
var stackFrameRE = regexp.MustCompile(`(?m)^(\s+at [^(]*\()(hook\.js):(\d+):(\d+)(\))`)

// rewriteStackTrace rewrites every `hook.js:LINE:COL` reference in msg
// using the supplied sourcemap, appending the original `source:line:col`
// after the bundled position so the developer can see both. Returns the
// message unchanged if c is nil or the lookup fails.
func rewriteStackTrace(msg string, c *sourcemap.Consumer) string {
	if c == nil {
		return msg
	}
	return stackFrameRE.ReplaceAllStringFunc(msg, func(frame string) string {
		m := stackFrameRE.FindStringSubmatch(frame)
		if len(m) != 6 {
			return frame
		}
		line, _ := strconv.Atoi(m[3])
		col, _ := strconv.Atoi(m[4])
		source, _, srcLine, srcCol, ok := c.Source(line, col)
		if !ok || source == "" {
			return frame
		}
		// Strip the esbuild fs-plugin namespace prefix from the source
		// path so the user sees a plain relative path.
		source = strings.TrimPrefix(source, "fs:")
		return fmt.Sprintf("%s%s:%d:%d%s", m[1], source, srcLine, srcCol, m[5])
	})
}
