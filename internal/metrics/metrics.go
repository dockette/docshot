// Package metrics is a hand-rolled Prometheus text-format counter registry.
package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

type registry struct {
	mu   sync.Mutex
	vals map[string]float64
	help map[string]string
}

var reg = &registry{vals: map[string]float64{}, help: map[string]string{
	"docshot_requests_total":           "Render requests by cache layer that answered (store, memory, document, shared, miss, not_modified).",
	"docshot_errors_total":             "Failed requests by HTTP status.",
	"docshot_conversions_total":        "Documents laid out to PDF, by route.",
	"docshot_conversion_seconds_total": "Time spent converting documents to PDF.",
	"docshot_rejected_total":           "Requests refused with 429 because the queue was full.",
	"docshot_renders_total":            "Pages rasterised (cache misses only).",
	"docshot_render_seconds_total":     "Time spent rasterising and encoding pages.",
	"docshot_gotenberg_restarts_total": "Times the supervised Gotenberg crashed and was restarted.",
}}

func (m *registry) add(name, labels string, v float64) {
	m.mu.Lock()
	m.vals[name+labels] += v
	m.mu.Unlock()
}

func Add(name, labels string, v float64) { reg.add(name, labels, v) }

func Inc(name, labels string) { reg.add(name, labels, 1) }

func Label(k, v string) string { return fmt.Sprintf(`{%s=%q}`, k, v) }

// Write writes every counter in Prometheus text format.
func Write(b *strings.Builder) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	keys := make([]string, 0, len(reg.vals))
	for k := range reg.vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	seen := map[string]bool{}
	for _, k := range keys {
		name := k
		if i := strings.IndexByte(k, '{'); i >= 0 {
			name = k[:i]
		}
		if !seen[name] {
			seen[name] = true
			fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s counter\n", name, reg.help[name], name)
		}
		fmt.Fprintf(b, "%s %g\n", k, reg.vals[k])
	}
}

func Gauge(b *strings.Builder, name, help string, v float64) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", name, help, name, name, v)
}
