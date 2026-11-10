//go:build linux

package netroute

import (
	"net"
	"reflect"
	"testing"
	"time"
)

func resetLinkCache(t *testing.T) {
	t.Helper()
	linkCache.Lock()
	clear(linkCache.links)
	linkCache.Unlock()
	t.Cleanup(func() {
		now = time.Now
		linkCache.Lock()
		clear(linkCache.links)
		linkCache.Unlock()
	})
}

func TestCachedLinkByIndexReusesUntilExpiry(t *testing.T) {
	resetLinkCache(t)
	fd := openNetlink(t)
	base := time.Now()
	now = func() time.Time { return base }

	want, err := net.InterfaceByIndex(1)
	if err != nil {
		t.Skip("no interface with index 1")
	}
	got, err := cachedLinkByIndex(fd, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cachedLinkByIndex(1) = %+v, want %+v", got, want)
	}

	// Plant a different name to tell a cached answer from a fresh one.
	linkCache.Lock()
	c := linkCache.links[1]
	c.iface.Name = "cached"
	linkCache.links[1] = c
	linkCache.Unlock()

	now = func() time.Time { return base.Add(linkCacheTTL - time.Millisecond) }
	if got, _ := cachedLinkByIndex(fd, 1); got.Name != "cached" {
		t.Fatalf("within the TTL got %q, want the cached entry", got.Name)
	}

	now = func() time.Time { return base.Add(linkCacheTTL) }
	if got, _ := cachedLinkByIndex(fd, 1); got.Name != want.Name {
		t.Fatalf("after the TTL got %q, want a fresh %q", got.Name, want.Name)
	}
}

func TestCachedLinkByIndexReturnsCopies(t *testing.T) {
	resetLinkCache(t)
	fd := openNetlink(t)
	a, err := cachedLinkByIndex(fd, 1)
	if err != nil {
		t.Skip("no interface with index 1")
	}
	a.Name = "changed"
	if a.HardwareAddr != nil {
		a.HardwareAddr[0] ^= 0xff
	}
	b, err := cachedLinkByIndex(fd, 1)
	if err != nil {
		t.Fatal(err)
	}
	if b.Name == "changed" {
		t.Fatal("a caller's change to a returned interface leaked into the cache")
	}
}

func TestCachedLinkByIndexDoesNotCacheErrors(t *testing.T) {
	resetLinkCache(t)
	fd := openNetlink(t)
	if _, err := cachedLinkByIndex(fd, 1<<30); err == nil {
		t.Fatal("expected an error for a nonexistent interface index")
	}
	linkCache.Lock()
	n := len(linkCache.links)
	linkCache.Unlock()
	if n != 0 {
		t.Fatalf("cache holds %d entries after an error, want 0", n)
	}
}

func TestCachedLinkByIndexBounded(t *testing.T) {
	resetLinkCache(t)
	fd := openNetlink(t)
	linkCache.Lock()
	for i := 0; i < linkCacheMax; i++ {
		linkCache.links[-1-i] = cachedLink{expires: time.Now().Add(time.Hour)}
	}
	linkCache.Unlock()
	if _, err := cachedLinkByIndex(fd, 1); err != nil {
		t.Skip("no interface with index 1")
	}
	linkCache.Lock()
	n := len(linkCache.links)
	linkCache.Unlock()
	if n != 1 {
		t.Fatalf("cache holds %d entries after overflowing, want 1", n)
	}
}
