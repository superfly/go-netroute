//go:build linux

package netroute

import (
	"errors"
	"net"
	"reflect"
	"testing"
	"time"
)

func resetRouteCache(t *testing.T) {
	t.Helper()
	routeCache.Lock()
	clear(routeCache.routes)
	routeCache.Unlock()
	t.Cleanup(func() {
		now = time.Now
		routeCache.Lock()
		clear(routeCache.routes)
		routeCache.Unlock()
	})
}

// fakeLookup counts calls and answers with a fixed route.
type fakeLookup struct {
	calls int
	err   error
}

func (f *fakeLookup) lookup() (*net.Interface, net.IP, net.IP, error) {
	f.calls++
	if f.err != nil {
		return nil, nil, nil, f.err
	}
	return &net.Interface{Index: 7, Name: "eth0", HardwareAddr: net.HardwareAddr{1, 2, 3, 4, 5, 6}},
		net.IPv4(10, 0, 0, 1).To4(), net.IPv4(10, 0, 0, 2).To4(), nil
}

func TestCachedRouteReusesUntilExpiry(t *testing.T) {
	resetRouteCache(t)
	base := time.Now()
	now = func() time.Time { return base }
	dst := net.IPv4(192, 0, 2, 1)
	f := &fakeLookup{}

	first, gw, src, err := cachedRoute(nil, nil, dst, f.lookup)
	if err != nil {
		t.Fatal(err)
	}

	now = func() time.Time { return base.Add(routeCacheTTL - time.Millisecond) }
	iface, gw2, src2, err := cachedRoute(nil, nil, dst, f.lookup)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls != 1 {
		t.Fatalf("within the TTL lookup ran %d times, want 1", f.calls)
	}
	if !reflect.DeepEqual(iface, first) || !gw2.Equal(gw) || !src2.Equal(src) {
		t.Fatalf("cached answer %v %v %v differs from %v %v %v", iface, gw2, src2, first, gw, src)
	}

	now = func() time.Time { return base.Add(routeCacheTTL) }
	if _, _, _, err := cachedRoute(nil, nil, dst, f.lookup); err != nil {
		t.Fatal(err)
	}
	if f.calls != 2 {
		t.Fatalf("after the TTL lookup ran %d times, want 2", f.calls)
	}
}

func TestCachedRouteKey(t *testing.T) {
	resetRouteCache(t)
	f := &fakeLookup{}
	dst := net.IPv4(192, 0, 2, 1)

	cachedRoute(nil, nil, dst, f.lookup)
	cachedRoute(nil, nil, dst.To4(), f.lookup)
	if f.calls != 1 {
		t.Fatalf("4- and 16-byte forms of one address ran lookup %d times, want 1", f.calls)
	}
	cachedRoute(nil, nil, net.IPv4(192, 0, 2, 2), f.lookup)
	cachedRoute(nil, net.IPv4(10, 0, 0, 3), dst, f.lookup)
	cachedRoute(net.HardwareAddr{1, 2, 3, 4, 5, 6}, nil, dst, f.lookup)
	if f.calls != 4 {
		t.Fatalf("distinct destination, source and input ran lookup %d times, want 4", f.calls)
	}
}

func TestCachedRouteReturnsCopies(t *testing.T) {
	resetRouteCache(t)
	f := &fakeLookup{}
	dst := net.IPv4(192, 0, 2, 1)
	cachedRoute(nil, nil, dst, f.lookup)

	iface, gw, src, _ := cachedRoute(nil, nil, dst, f.lookup)
	iface.Name = "changed"
	iface.HardwareAddr[0] ^= 0xff
	gw[0] ^= 0xff
	src[0] ^= 0xff

	iface, gw, src, _ = cachedRoute(nil, nil, dst, f.lookup)
	if iface.Name != "eth0" || iface.HardwareAddr[0] != 1 || !gw.Equal(net.IPv4(10, 0, 0, 1)) || !src.Equal(net.IPv4(10, 0, 0, 2)) {
		t.Fatalf("a caller's change leaked into the cache: %v %v %v", iface, gw, src)
	}
}

func TestCachedRouteDoesNotCacheErrors(t *testing.T) {
	resetRouteCache(t)
	f := &fakeLookup{err: errors.New("no route")}
	dst := net.IPv4(192, 0, 2, 1)
	for i := 0; i < 2; i++ {
		if _, _, _, err := cachedRoute(nil, nil, dst, f.lookup); err == nil {
			t.Fatal("expected the lookup's error")
		}
	}
	if f.calls != 2 {
		t.Fatalf("lookup ran %d times, want 2", f.calls)
	}
}

func TestCachedRouteBounded(t *testing.T) {
	resetRouteCache(t)
	routeCache.Lock()
	for i := 0; i < routeCacheMax; i++ {
		routeCache.routes[routeKey{dst: string(rune(i))}] = cachedRouteEntry{expires: time.Now().Add(time.Hour)}
	}
	routeCache.Unlock()
	f := &fakeLookup{}
	cachedRoute(nil, nil, net.IPv4(192, 0, 2, 1), f.lookup)
	routeCache.Lock()
	n := len(routeCache.routes)
	routeCache.Unlock()
	if n != 1 {
		t.Fatalf("cache holds %d entries after overflowing, want 1", n)
	}
}

func TestRouteCachedMatchesUncached(t *testing.T) {
	resetRouteCache(t)
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	dst := net.IPv4(127, 0, 0, 1)
	wantIface, wantGw, wantSrc, err := lookupRoute(nil, nil, dst)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		iface, gw, src, err := r.Route(dst)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(iface, wantIface) || !gw.Equal(wantGw) || !src.Equal(wantSrc) {
			t.Fatalf("Route = %v %v %v, want %v %v %v", iface, gw, src, wantIface, wantGw, wantSrc)
		}
	}
}

// BenchmarkRouteUncached measures one kernel round trip, what Route costs
// when the answer is not cached.
func BenchmarkRouteUncached(b *testing.B) {
	dst := net.IPv4(127, 0, 0, 1)
	for b.Loop() {
		if _, _, _, err := lookupRoute(nil, nil, dst); err != nil {
			b.Fatal(err)
		}
	}
}
