package render

import (
	"context"
	"net/http"
	"sync/atomic"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/dockette/docshot/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

// Only conversions and renders take a slot; beyond the queue the answer is 429, not a pile-up of LibreOffice processes.

type limiter struct {
	slots    chan struct{}
	waiting  atomic.Int64
	maxQueue int64
}

func newLimiter(concurrency, queue int) *limiter {
	return &limiter{slots: make(chan struct{}, max(concurrency, 1)), maxQueue: int64(queue)}
}

func (l *limiter) acquire(ctx context.Context) (func(), error) {
	select {
	case l.slots <- struct{}{}:
		return func() { <-l.slots }, nil
	default:
	}
	if l.waiting.Add(1) > l.maxQueue {
		l.waiting.Add(-1)
		metrics.Rejected.Inc()
		return nil, apierr.New(http.StatusTooManyRequests, "busy: %d renders running, queue of %d full", cap(l.slots), l.maxQueue)
	}
	defer l.waiting.Add(-1)
	select {
	case l.slots <- struct{}{}:
		return func() { <-l.slots }, nil
	case <-ctx.Done():
		return nil, apierr.New(http.StatusServiceUnavailable, "timed out waiting for a render slot")
	}
}

var (
	inflightDesc   = prometheus.NewDesc("docshot_inflight", "Renders/conversions running now.", nil, nil)
	slotsDesc      = prometheus.NewDesc("docshot_concurrency", "Renders/conversions allowed at once (DOCSHOT_CONCURRENCY).", nil, nil)
	queueDesc      = prometheus.NewDesc("docshot_queue", "Requests waiting for a slot.", nil, nil)
	queueMaxDesc   = prometheus.NewDesc("docshot_queue_limit", "Waiting requests allowed before 429 (DOCSHOT_QUEUE).", nil, nil)
	memBytesDesc   = prometheus.NewDesc("docshot_memory_cache_bytes", "Bytes held by the memory caches.", []string{"cache"}, nil)
	memEntriesDesc = prometheus.NewDesc("docshot_memory_cache_entries", "Entries in the memory caches.", []string{"cache"}, nil)
	memMaxDesc     = prometheus.NewDesc("docshot_memory_cache_max_bytes", "Size limit of each memory cache (DOCSHOT_CACHE_MB).", nil, nil)
)

// Collector reports the limiter and memory caches at scrape time.
func (r *Renderer) Collector() prometheus.Collector { return gauges{r} }

type gauges struct{ r *Renderer }

func (g gauges) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{inflightDesc, slotsDesc, queueDesc, queueMaxDesc, memBytesDesc, memEntriesDesc, memMaxDesc} {
		ch <- d
	}
}

func (g gauges) Collect(ch chan<- prometheus.Metric) {
	r := g.r
	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
	}
	gauge(inflightDesc, float64(len(r.limit.slots)))
	gauge(slotsDesc, float64(cap(r.limit.slots)))
	gauge(queueDesc, float64(r.limit.waiting.Load()))
	gauge(queueMaxDesc, float64(r.limit.maxQueue))
	gauge(memBytesDesc, float64(r.cache.images.Bytes()), "images")
	gauge(memBytesDesc, float64(r.cache.docs.Bytes()), "documents")
	gauge(memEntriesDesc, float64(r.cache.images.Len()), "images")
	gauge(memEntriesDesc, float64(r.cache.docs.Len()), "documents")
	gauge(memMaxDesc, float64(int64(r.cfg.CacheMB)<<20))
}
