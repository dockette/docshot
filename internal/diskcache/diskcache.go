// Package diskcache is a byte-bounded LRU of rendered results on disk.
//
// Each entry is one file, <dir>/v1/<key[:2]>/<key>: a JSON header line, then the body.
// The index lives in memory and is rebuilt from the headers on start, ordered by
// mtime (bumped on use), so a mounted directory keeps its hits and LRU order across restarts.
package diskcache

import (
	"bufio"
	"container/list"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const (
	schema     = "v1"
	maxHeader  = 64 << 10
	touchEvery = time.Minute // mtime is the persisted LRU order; bump it at most this often
)

// ErrTooLarge: the body is over a quarter of the cache, which would flush everything else.
var ErrTooLarge = errors.New("diskcache: entry too large")

// Entry is the header stored before the body; it is everything needed to answer
// a request, conditional ones included, without reading the body.
type Entry struct {
	Key         string            `json:"key"`
	Source      string            `json:"source,omitempty"`   // purge handle, e.g. "s3:bucket/key"
	Name        string            `json:"name,omitempty"`     // document file name, for download names
	Type        string            `json:"type,omitempty"`     // input type, for metrics
	Revision    string            `json:"revision,omitempty"` // source revision it was made from (S3 ETag, size+mtime, content hash)
	ContentType string            `json:"content_type"`
	ETag        string            `json:"etag,omitempty"`
	Header      map[string]string `json:"header,omitempty"` // response headers to replay (X-Page-Count, …)
	Created     time.Time         `json:"created"`          // rendered at; sent as Last-Modified
	Validated   time.Time         `json:"validated"`        // last confirmed against the source; TTL counts from here
	Size        int64             `json:"size"`
}

type node struct {
	e       Entry
	hdr     int64 // header line length, newline included: the body starts here
	touched time.Time
}

// TypeStat is the share of the cache held by one input type.
type TypeStat struct {
	Entries int
	Bytes   int64
}

type Stats struct {
	Entries      int
	Bytes        int64
	Max          int64
	Oldest       time.Time // last use of the LRU tail
	Evictions    int64
	EvictedBytes int64
	Types        map[string]TypeStat
}

type Cache struct {
	dir string // <root>/v1
	max int64

	mu        sync.Mutex
	used      int64
	order     *list.List // front = most recent; values *node
	items     map[string]*list.Element
	sources   map[string]map[string]struct{}
	types     map[string]TypeStat
	evictions int64
	evicted   int64
}

// Open loads (or creates) the cache in root and trims it to maxBytes.
func Open(root string, maxBytes int64) (*Cache, error) {
	c := &Cache{dir: filepath.Join(root, schema), max: maxBytes, order: list.New(),
		items: map[string]*list.Element{}, sources: map[string]map[string]struct{}{}, types: map[string]TypeStat{}}
	tmp := filepath.Join(c.dir, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil, err
	}
	// Leftovers of writes cut short by a crash; also proves the directory is writable.
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return nil, err
	}
	probe, err := os.CreateTemp(tmp, "probe-*")
	if err != nil {
		return nil, err
	}
	probe.Close()
	os.Remove(probe.Name())

	t0 := time.Now()
	type found struct {
		n     *node
		mtime time.Time
	}
	var all []found
	shards, _ := os.ReadDir(c.dir)
	for _, sh := range shards {
		if !sh.IsDir() || len(sh.Name()) != 2 {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(c.dir, sh.Name()))
		for _, f := range files {
			p := filepath.Join(c.dir, sh.Name(), f.Name())
			n, mtime, err := readHeader(p)
			if err != nil || n.e.Key != f.Name() {
				slog.Warn("cache: dropping unreadable entry", "file", p, "err", err)
				os.Remove(p)
				continue
			}
			all = append(all, found{n, mtime})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mtime.Before(all[j].mtime) })
	c.mu.Lock()
	for _, f := range all {
		f.n.touched = f.mtime
		c.insert(f.n)
	}
	c.evict()
	c.mu.Unlock()
	slog.Info("cache: loaded", "dir", root, "entries", len(c.items), "bytes", c.used, "max", maxBytes, "ms", time.Since(t0).Milliseconds())
	return c, nil
}

func readHeader(p string) (*node, time.Time, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	line, err := bufio.NewReaderSize(io.LimitReader(f, maxHeader), maxHeader).ReadSlice('\n')
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("header: %w", err)
	}
	n := &node{hdr: int64(len(line))}
	if err := json.Unmarshal(line, &n.e); err != nil {
		return nil, time.Time{}, err
	}
	if st.Size() != n.hdr+n.e.Size {
		return nil, time.Time{}, fmt.Errorf("truncated: %d bytes, want %d", st.Size(), n.hdr+n.e.Size)
	}
	return n, st.ModTime(), nil
}

func (c *Cache) path(key string) string { return filepath.Join(c.dir, key[:2], key) }

// Get looks an entry up in the index (no disk I/O) and marks it used.
func (c *Cache) Get(key string) (Entry, bool) {
	c.mu.Lock()
	el, ok := c.items[key]
	if !ok {
		c.mu.Unlock()
		return Entry{}, false
	}
	c.order.MoveToFront(el)
	n := el.Value.(*node)
	now := time.Now()
	touch := now.Sub(n.touched) > touchEvery
	if touch {
		n.touched = now
	}
	e := n.e
	c.mu.Unlock()
	if touch {
		_ = os.Chtimes(c.path(key), now, now)
	}
	return e, true
}

// Open returns the entry and its body. A file gone from under the index is dropped (a miss).
func (c *Cache) Open(key string) (Entry, io.ReadCloser, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return Entry{}, nil, fs.ErrNotExist
	}
	n := el.Value.(*node)
	f, err := os.Open(c.path(key))
	if err == nil {
		_, err = f.Seek(n.hdr, io.SeekStart)
		if err != nil {
			f.Close()
		}
	}
	if err != nil {
		c.remove(el)
		return Entry{}, nil, err
	}
	return n.e, f, nil
}

// Put stores body under e.Key, replacing any older entry, then evicts down to the limit.
func (c *Cache) Put(e Entry, body []byte) error {
	e.Size = int64(len(body))
	if c.max <= 0 || e.Size > c.max/4 {
		return ErrTooLarge
	}
	hdr, err := json.Marshal(e)
	if err != nil {
		return err
	}
	hdr = append(hdr, '\n')
	f, err := os.CreateTemp(filepath.Join(c.dir, "tmp"), "*.tmp")
	if err != nil {
		return err
	}
	_, err = f.Write(hdr)
	if err == nil {
		_, err = f.Write(body)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.MkdirAll(filepath.Join(c.dir, e.Key[:2]), 0o755)
	}
	if err != nil {
		os.Remove(f.Name())
		return err
	}
	// Rename and index together, so Open never pairs a file with another write's header.
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := os.Rename(f.Name(), c.path(e.Key)); err != nil {
		os.Remove(f.Name())
		return err
	}
	if el, ok := c.items[e.Key]; ok {
		c.unindex(el)
	}
	c.insert(&node{e: e, hdr: int64(len(hdr)), touched: time.Now()})
	c.evict()
	return nil
}

// Revalidated restamps an entry as confirmed against its source (rewrites its header).
func (c *Cache) Revalidated(key string, at time.Time) error {
	e, body, err := c.Open(key)
	if err != nil {
		return err
	}
	b, err := io.ReadAll(body)
	body.Close()
	if err != nil {
		return err
	}
	e.Validated = at
	return c.Put(e, b)
}

// Delete removes one entry.
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		c.remove(el)
	}
}

// Purge removes every entry made from source ("" = everything) and returns how many.
func (c *Cache) Purge(source string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	var els []*list.Element
	if source == "" {
		for _, el := range c.items {
			els = append(els, el)
		}
	} else {
		for key := range c.sources[source] {
			els = append(els, c.items[key])
		}
	}
	for _, el := range els {
		c.remove(el)
	}
	return len(els)
}

func (c *Cache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Stats{Entries: len(c.items), Bytes: c.used, Max: c.max, Evictions: c.evictions, EvictedBytes: c.evicted,
		Types: make(map[string]TypeStat, len(c.types))}
	if back := c.order.Back(); back != nil {
		s.Oldest = back.Value.(*node).touched
	}
	for t, ts := range c.types {
		s.Types[t] = ts
	}
	return s
}

// insert, unindex, remove and evict run under c.mu.

func (c *Cache) insert(n *node) {
	c.items[n.e.Key] = c.order.PushFront(n)
	c.used += n.e.Size
	if n.e.Source != "" {
		if c.sources[n.e.Source] == nil {
			c.sources[n.e.Source] = map[string]struct{}{}
		}
		c.sources[n.e.Source][n.e.Key] = struct{}{}
	}
	ts := c.types[n.e.Type]
	ts.Entries++
	ts.Bytes += n.e.Size
	c.types[n.e.Type] = ts
}

func (c *Cache) unindex(el *list.Element) {
	n := el.Value.(*node)
	c.order.Remove(el)
	delete(c.items, n.e.Key)
	c.used -= n.e.Size
	if keys := c.sources[n.e.Source]; keys != nil {
		delete(keys, n.e.Key)
		if len(keys) == 0 {
			delete(c.sources, n.e.Source)
		}
	}
	ts := c.types[n.e.Type]
	ts.Entries--
	ts.Bytes -= n.e.Size
	if ts.Entries <= 0 {
		delete(c.types, n.e.Type)
	} else {
		c.types[n.e.Type] = ts
	}
}

func (c *Cache) remove(el *list.Element) {
	c.unindex(el)
	_ = os.Remove(c.path(el.Value.(*node).e.Key))
}

func (c *Cache) evict() {
	for c.used > c.max {
		el := c.order.Back()
		if el == nil {
			return
		}
		c.evictions++
		c.evicted += el.Value.(*node).e.Size
		c.remove(el)
	}
}
