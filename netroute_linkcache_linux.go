//go:build linux

package netroute

import (
	"bytes"
	"net"
	"sync"
	"time"
)

// linkCacheTTL is how long a fetched interface is reused. An RTM_GETLINK
// request waits on the kernel's RTNL lock, which hosts that create and
// destroy interfaces at a high rate hold for tens of milliseconds at a time,
// and every caller blocked on it stalls together. Routes on such hosts
// resolve to the same few output interfaces, so reusing the answer briefly
// keeps nearly every lookup off that lock while still noticing a renamed or
// reconfigured interface within seconds.
const linkCacheTTL = 10 * time.Second

// linkCacheMax bounds the cache. Lookups resolve to output interfaces, of
// which a host has few that carry routes, so reaching it means the cache is
// not helping and is cleared rather than grown.
const linkCacheMax = 256

type cachedLink struct {
	iface   net.Interface
	expires time.Time
}

var linkCache = struct {
	sync.Mutex
	links map[int]cachedLink
}{links: make(map[int]cachedLink)}

// now is replaced in tests.
var now = time.Now

// cachedLinkByIndex returns the interface with the given index, fetching it
// with linkByIndex on the netlink socket fd when no fresh copy is cached.
// Errors are not cached.
func cachedLinkByIndex(fd int, index int) (*net.Interface, error) {
	t := now()
	linkCache.Lock()
	c, ok := linkCache.links[index]
	linkCache.Unlock()
	if ok && t.Before(c.expires) {
		return copyLink(&c.iface), nil
	}

	iface, err := linkByIndex(fd, index)
	if err != nil {
		return nil, err
	}

	linkCache.Lock()
	if len(linkCache.links) >= linkCacheMax {
		clear(linkCache.links)
	}
	linkCache.links[index] = cachedLink{iface: *copyLink(iface), expires: t.Add(linkCacheTTL)}
	linkCache.Unlock()
	return iface, nil
}

// copyLink returns a copy that shares no memory with the cache, so a caller
// modifying the result cannot change what later callers see.
func copyLink(iface *net.Interface) *net.Interface {
	out := *iface
	out.HardwareAddr = net.HardwareAddr(bytes.Clone(iface.HardwareAddr))
	return &out
}
