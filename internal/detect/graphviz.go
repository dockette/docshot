package detect

import (
	"strings"
)

// isGraphviz tells a Graphviz .dot from a Word 97 template .dot (OLE2).
func isGraphviz(b []byte) bool {
	t := strings.ToLower(strings.TrimSpace(string(head(b, 2048))))
	for strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "/*") {
		if i := strings.IndexAny(t, "\n"); i >= 0 {
			t = strings.TrimSpace(t[i+1:])
		} else {
			break
		}
	}
	t = strings.TrimPrefix(t, "strict ")
	return strings.HasPrefix(t, "digraph") || strings.HasPrefix(t, "graph")
}
