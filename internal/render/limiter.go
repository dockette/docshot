package render

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/dockette/docshot/internal/apierr"
	"github.com/dockette/docshot/internal/metrics"
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
		metrics.Inc("docshot_rejected_total", "")
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

func (r *Renderer) WriteGauges(b *strings.Builder) {
	metrics.Gauge(b, "docshot_inflight", "Renders/conversions running now.", float64(len(r.limit.slots)))
	metrics.Gauge(b, "docshot_queue", "Requests waiting for a slot.", float64(r.limit.waiting.Load()))
	fmt.Fprintf(b, "# HELP docshot_cache_bytes Bytes held by the memory caches.\n# TYPE docshot_cache_bytes gauge\n")
	fmt.Fprintf(b, "docshot_cache_bytes%s %d\n", metrics.Label("cache", "images"), r.cache.images.Bytes())
	fmt.Fprintf(b, "docshot_cache_bytes%s %d\n", metrics.Label("cache", "documents"), r.cache.docs.Bytes())
}
