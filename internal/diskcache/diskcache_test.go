package diskcache

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func key(c byte) string { return strings.Repeat(string(c), 32) }

func put(t *testing.T, c *Cache, k, source, body string) {
	t.Helper()
	if err := c.Put(Entry{Key: k, Source: source, Type: "pdf", ContentType: "image/png", ETag: `"` + k + `"`}, []byte(body)); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, c *Cache, k string) string {
	t.Helper()
	_, r, err := c.Open(k)
	if err != nil {
		t.Fatalf("open %s: %v", k[:4], err)
	}
	defer r.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}

func TestPutGetReopen(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	put(t, c, key('a'), "s3:b/x.docx", "hello")
	put(t, c, key('a'), "s3:b/x.docx", "hello again") // replace
	if e, ok := c.Get(key('a')); !ok || e.Size != 11 || e.ETag != `"`+key('a')+`"` {
		t.Fatalf("get: %+v %v", e, ok)
	}
	if got := read(t, c, key('a')); got != "hello again" {
		t.Fatalf("body %q", got)
	}

	c2, err := Open(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if got := read(t, c2, key('a')); got != "hello again" {
		t.Fatalf("after reopen: %q", got)
	}
	if s := c2.Stats(); s.Entries != 1 || s.Bytes != 11 || s.Types["pdf"].Entries != 1 {
		t.Fatalf("stats %+v", s)
	}
}

func TestEvictsLeastRecentlyUsed(t *testing.T) {
	c, err := Open(t.TempDir(), 40)
	if err != nil {
		t.Fatal(err)
	}
	put(t, c, key('a'), "", "0123456789")
	put(t, c, key('b'), "", "0123456789")
	put(t, c, key('c'), "", "0123456789")
	c.Get(key('a')) // a is now newer than b
	put(t, c, key('d'), "", "0123456789")
	put(t, c, key('e'), "", "0123456789")
	if _, ok := c.Get(key('b')); ok {
		t.Fatal("b should have been evicted")
	}
	if _, ok := c.Get(key('a')); !ok {
		t.Fatal("a was used recently and should stay")
	}
	if s := c.Stats(); s.Bytes > 40 || s.Evictions != 1 {
		t.Fatalf("stats %+v", s)
	}
	if err := c.Put(Entry{Key: key('f')}, make([]byte, 11)); err != ErrTooLarge {
		t.Fatalf("want ErrTooLarge, got %v", err)
	}
}

func TestPurgeBySource(t *testing.T) {
	c, err := Open(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	put(t, c, key('a'), "s3:b/x", "1")
	put(t, c, key('b'), "s3:b/x", "2")
	put(t, c, key('c'), "s3:b/y", "3")
	if n := c.Purge("s3:b/x"); n != 2 {
		t.Fatalf("purged %d", n)
	}
	if _, ok := c.Get(key('c')); !ok {
		t.Fatal("other source purged")
	}
	if n := c.Purge(""); n != 1 || c.Stats().Entries != 0 {
		t.Fatalf("purge all: %d", n)
	}
}

func TestRevalidatedAndCorruptFiles(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	put(t, c, key('a'), "", "body")
	put(t, c, key('b'), "", "body")
	at := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := c.Revalidated(key('a'), at); err != nil {
		t.Fatal(err)
	}
	if e, _ := c.Get(key('a')); !e.Validated.Equal(at) || read(t, c, key('a')) != "body" {
		t.Fatalf("revalidated: %+v", e)
	}
	// truncate b, leave junk in tmp: both are cleaned up on open
	p := filepath.Join(dir, schema, key('b')[:2], key('b'))
	if err := os.Truncate(p, 10); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, schema, "tmp", "x.tmp"), []byte("junk"), 0o644)
	c2, err := Open(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c2.Get(key('b')); ok {
		t.Fatal("truncated entry loaded")
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("truncated file kept")
	}
	if e, ok := c2.Get(key('a')); !ok || !e.Validated.Equal(at) {
		t.Fatal("good entry lost")
	}
}
