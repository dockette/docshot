// Package metrics holds docshot's Prometheus collectors; Register adds them to a registry.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Label values are bounded: "type" is a detected extension (unrecognised files count as "other"),
// "source" is path|s3|url|upload, "op" is page|grid|pages.
var (
	Requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "docshot_requests_total",
		Help: "Render requests by operation, source and the layer that answered (disk, store, memory, document, shared, miss, not_modified, stale, revalidated).",
	}, []string{"op", "source", "cache"})
	Errors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "docshot_errors_total",
		Help: "Failed render requests by HTTP status and error code.",
	}, []string{"op", "source", "status", "code"})
	Documents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "docshot_documents_total",
		Help: "Successful renders by detected input type and route (cache hits included).",
	}, []string{"type", "route", "op"})
	InputBytes = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "docshot_input_size_bytes",
		Help:    "Size of loaded source documents.",
		Buckets: prometheus.ExponentialBuckets(1<<10, 4, 11), // 1 KiB … 1 GiB
	}, []string{"source", "type"})
	OutputBytes = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "docshot_output_size_bytes",
		Help:    "Size of returned images and ZIPs.",
		Buckets: prometheus.ExponentialBuckets(1<<10, 4, 10), // 1 KiB … 256 MiB
	}, []string{"op", "format"})
	Pages = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "docshot_document_pages",
		Help:    "Pages (frames, members) of rendered documents.",
		Buckets: []float64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000},
	}, []string{"route"})

	Conversions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "docshot_conversions_total",
		Help: "Documents laid out to PDF, by route and input type.",
	}, []string{"route", "type"})
	ConversionErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "docshot_conversion_errors_total",
		Help: "Failed conversions to PDF, by route and input type.",
	}, []string{"route", "type"})
	ConversionSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "docshot_conversion_duration_seconds",
		Help:    "Time spent converting documents to PDF.",
		Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 180},
	}, []string{"route"})
	Renders = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "docshot_renders_total",
		Help: "Rasterisations (cache misses only), by operation and input type.",
	}, []string{"op", "type"})
	RenderSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "docshot_render_duration_seconds",
		Help:    "Time spent rasterising and encoding.",
		Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60},
	}, []string{"op"})
	Rejected = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "docshot_rejected_total",
		Help: "Requests refused with 429 because the queue was full.",
	})
	GotenbergRestarts = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "docshot_gotenberg_restarts_total",
		Help: "Times the supervised Gotenberg crashed and was restarted.",
	})

	CacheRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "docshot_cache_requests_total",
		Help: "Disk cache lookups by result (hit, miss, stale, revalidated, refreshed, revalidate_error, not_modified).",
	}, []string{"result"})
	CacheWrites = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "docshot_cache_writes_total",
		Help: "Disk cache writes by result (ok, error, skipped).",
	}, []string{"result"})
	CachePurged = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "docshot_cache_purged_entries_total",
		Help: "Disk cache entries removed through DELETE /cache.",
	})

	S3Requests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "docshot_s3_requests_total",
		Help: "S3 calls by operation (stat, get, put; source or stored result) and result.",
	}, []string{"op", "result"})
	S3Seconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "docshot_s3_request_duration_seconds",
		Help:    "S3 call latency, body transfer included.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	}, []string{"op"})
	S3Bytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "docshot_s3_bytes_total",
		Help: "Bytes transferred to and from S3, by direction (received, sent).",
	}, []string{"direction"})
	URLFetches = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "docshot_url_fetches_total",
		Help: "?url= downloads by result (ok, denied, error).",
	}, []string{"result"})
)

var all = []prometheus.Collector{
	Requests, Errors, Documents, InputBytes, OutputBytes, Pages,
	Conversions, ConversionErrors, ConversionSeconds, Renders, RenderSeconds, Rejected, GotenbergRestarts,
	CacheRequests, CacheWrites, CachePurged, S3Requests, S3Seconds, S3Bytes, URLFetches,
}

// NewRegistry returns a registry with docshot's collectors, Go runtime and process metrics.
func NewRegistry(extra ...prometheus.Collector) *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	reg.MustRegister(all...)
	reg.MustRegister(extra...)
	return reg
}

// Result maps an error to a bounded label value.
func Result(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}
