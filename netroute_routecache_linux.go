//go:build linux

package netroute

import (
	"net"
	"sync"
	"time"
)

// routeCacheTTL is how long a resolved route is reused. Each lookup otherwise
// opens a netlink socket and sends an RTM_GETROUTE, a few hundred
// microseconds on a busy host, and callers such as go-libp2p's reuseport
// transport do that on every outbound connection. They dial the same peers
// over and over, so reusing the answer briefly removes nearly all of that
// cost while still following a route change within seconds.
const routeCacheTTL = 10 * time.Second

// routeCacheMax bounds the cache. Reaching it means lookups are spread over
// more destinations than a short-lived cache helps with, so it is cleared
// rather than grown.
const routeCacheMax = 4096

type routeKey struct {
	input, src, dst string
}

type cachedRouteEntry struct {
	iface        net.Interface
	gateway      net.IP
	preferredSrc net.IP
	expires      time.Time
}

var routeCache = struct {
	sync.Mutex
	routes map[routeKey]cachedRouteEntry
}{routes: make(map[routeKey]cachedRouteEntry)}

// cachedRoute returns the route for the given lookup, calling lookup when no
// fresh answer is cached. Errors are not cached.
func cachedRoute(input net.HardwareAddr, src, dst net.IP, lookup func() (*net.Interface, net.IP, net.IP, error)) (*net.Interface, net.IP, net.IP, error) {
	key := routeKey{input: string(input), src: string(normalizeIP(src)), dst: string(normalizeIP(dst))}
	t := now()
	routeCache.Lock()
	c, ok := routeCache.routes[key]
	routeCache.Unlock()
	if ok && t.Before(c.expires) {
		return copyLink(&c.iface), copyIP(c.gateway), copyIP(c.preferredSrc), nil
	}

	iface, gateway, preferredSrc, err := lookup()
	if err != nil {
		return nil, nil, nil, err
	}

	routeCache.Lock()
	if len(routeCache.routes) >= routeCacheMax {
		clear(routeCache.routes)
	}
	routeCache.routes[key] = cachedRouteEntry{
		iface:        *copyLink(iface),
		gateway:      copyIP(gateway),
		preferredSrc: copyIP(preferredSrc),
		expires:      t.Add(routeCacheTTL),
	}
	routeCache.Unlock()
	return iface, gateway, preferredSrc, nil
}

// normalizeIP gives an IPv4 address its 4-byte form, so the 4- and 16-byte
// spellings of the same address share a cache entry.
func normalizeIP(ip net.IP) net.IP {
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return ip
}
