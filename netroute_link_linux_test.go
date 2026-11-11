//go:build linux

package netroute

import (
	"net"
	"reflect"
	"syscall"
	"testing"
)

func openNetlink(tb testing.TB) int {
	tb.Helper()
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_DGRAM, syscall.NETLINK_ROUTE)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { syscall.Close(fd) })
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		tb.Fatal(err)
	}
	return fd
}

func TestLinkByIndexMatchesStdlib(t *testing.T) {
	ifs, err := net.Interfaces()
	if err != nil || len(ifs) == 0 {
		t.Skip("Can't test links without access to system interfaces")
	}
	fd := openNetlink(t)
	for _, want := range ifs {
		got, err := linkByIndex(fd, want.Index)
		if err != nil {
			t.Fatalf("linkByIndex(%d): %v", want.Index, err)
		}
		if !reflect.DeepEqual(*got, want) {
			t.Errorf("linkByIndex(%d) = %+v, want %+v", want.Index, *got, want)
		}
	}
}

func TestLinkByIndexUnknown(t *testing.T) {
	fd := openNetlink(t)
	if _, err := linkByIndex(fd, 1<<30); err == nil {
		t.Fatal("expected an error for a nonexistent interface index")
	}
	// The socket still serves requests after an error reply.
	if _, err := linkByIndex(fd, 1); err != nil {
		t.Fatalf("linkByIndex(1) after error: %v", err)
	}
}

func TestRouteInterfaceMatchesStdlib(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	iface, _, _, err := r.Route(net.IPv4(127, 0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	want, err := net.InterfaceByIndex(iface.Index)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(iface, want) {
		t.Fatalf("Route interface = %+v, want %+v", iface, want)
	}
}

// BenchmarkRoute measures what a caller pays per repeated lookup, including
// New(), which the route cache answers. BenchmarkRouteUncached measures the
// kernel round trip behind it.
func BenchmarkRoute(b *testing.B) {
	dst := net.IPv4(127, 0, 0, 1)
	for b.Loop() {
		r, err := New()
		if err != nil {
			b.Fatal(err)
		}
		if _, _, _, err := r.Route(dst); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLinkByIndex(b *testing.B) {
	fd := openNetlink(b)
	for b.Loop() {
		if _, err := linkByIndex(fd, 1); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInterfaceByIndex(b *testing.B) {
	for b.Loop() {
		if _, err := net.InterfaceByIndex(1); err != nil {
			b.Fatal(err)
		}
	}
}
