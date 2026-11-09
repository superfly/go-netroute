//go:build linux

package netroute

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// ARP hardware types of IP tunnels, from linux/if_arp.h. The kernel reports a
// tunnel's local IP address as its IFLA_ADDRESS, which is not a hardware
// address, so linkByIndex drops it the same way the standard library does.
const (
	arpHardwareIPv4IPv4 = 768 // IPv4 over IPv4 tunneling
	arpHardwareIPv6IPv6 = 769 // IPv6 over IPv6 tunneling
	arpHardwareIPv6IPv4 = 776 // IPv6 over IPv4 tunneling
	arpHardwareGREIPv4  = 778 // any over GRE over IPv4 tunneling
	arpHardwareGREIPv6  = 823 // any over GRE over IPv6 tunneling
)

// linkByIndex fetches a single interface with one RTM_GETLINK request on the
// netlink socket fd.
//
// net.InterfaceByIndex dumps every link on the host and filters the result,
// so its cost grows with the number of interfaces: tens of milliseconds on a
// host with thousands of them. Asking the kernel for the one index we need
// costs the same however many interfaces exist. The result matches what
// net.InterfaceByIndex returns for the same index.
func linkByIndex(fd int, index int) (*net.Interface, error) {
	if index <= 0 {
		return nil, fmt.Errorf("invalid interface index %d", index)
	}

	seq := atomic.AddUint32(&nlSequence, 1)
	request, err := buildLinkRequest(index, seq, uint32(os.Getpid()))
	if err != nil {
		return nil, err
	}
	if err := syscall.Sendto(fd, request, 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return nil, fmt.Errorf("send netlink link request: %w", err)
	}
	return readLinkResponse(fd, seq, index)
}

func buildLinkRequest(index int, seq, pid uint32) ([]byte, error) {
	ifim := syscall.IfInfomsg{
		Family: syscall.AF_UNSPEC,
		Index:  int32(index),
	}
	header := syscall.NlMsghdr{
		Len:   uint32(syscall.NLMSG_HDRLEN + syscall.SizeofIfInfomsg),
		Type:  uint16(syscall.RTM_GETLINK),
		Flags: uint16(syscall.NLM_F_REQUEST),
		Seq:   seq,
		Pid:   pid,
	}

	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.NativeEndian, header); err != nil {
		return nil, fmt.Errorf("marshal nlmsghdr: %w", err)
	}
	if err := binary.Write(buf, binary.NativeEndian, ifim); err != nil {
		return nil, fmt.Errorf("marshal ifinfomsg: %w", err)
	}
	return buf.Bytes(), nil
}

func readLinkResponse(fd int, seq uint32, index int) (*net.Interface, error) {
	buf := make([]byte, 1<<16)
	for {
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			return nil, fmt.Errorf("receive netlink link response: %w", err)
		}

		msgs, err := syscall.ParseNetlinkMessage(buf[:n])
		if err != nil {
			return nil, fmt.Errorf("parse netlink link response: %w", err)
		}

		for _, m := range msgs {
			if m.Header.Seq != seq {
				continue
			}
			switch m.Header.Type {
			case syscall.NLMSG_ERROR:
				if err := parseNetlinkError(m.Data); err != nil {
					return nil, fmt.Errorf("netlink link error: %w", err)
				}
			case syscall.NLMSG_DONE:
				return nil, errors.New("link lookup returned no result")
			case syscall.RTM_NEWLINK:
				if len(m.Data) < syscall.SizeofIfInfomsg {
					return nil, fmt.Errorf("short ifinfomsg: %d bytes", len(m.Data))
				}
				ifim := (*syscall.IfInfomsg)(unsafe.Pointer(&m.Data[0]))
				if int(ifim.Index) != index {
					continue
				}
				attrs, err := syscall.ParseNetlinkRouteAttr(&m)
				if err != nil {
					return nil, fmt.Errorf("parse link attributes: %w", err)
				}
				return newLink(ifim, attrs), nil
			}
		}
	}
}

// newLink builds a net.Interface from an RTM_NEWLINK message, the same way
// the standard library's net package does on Linux.
func newLink(ifim *syscall.IfInfomsg, attrs []syscall.NetlinkRouteAttr) *net.Interface {
	ifi := &net.Interface{Index: int(ifim.Index), Flags: linkFlags(ifim.Flags)}
	for _, a := range attrs {
		switch a.Attr.Type {
		case syscall.IFLA_ADDRESS:
			switch len(a.Value) {
			case net.IPv4len:
				switch ifim.Type {
				case arpHardwareIPv4IPv4, arpHardwareGREIPv4, arpHardwareIPv6IPv4:
					continue
				}
			case net.IPv6len:
				switch ifim.Type {
				case arpHardwareIPv6IPv6, arpHardwareGREIPv6:
					continue
				}
			}
			if !bytes.Equal(a.Value, make([]byte, len(a.Value))) {
				ifi.HardwareAddr = net.HardwareAddr(bytes.Clone(a.Value))
			}
		case syscall.IFLA_IFNAME:
			if len(a.Value) > 0 {
				ifi.Name = string(bytes.TrimRight(a.Value, "\x00"))
			}
		case syscall.IFLA_MTU:
			if len(a.Value) >= 4 {
				ifi.MTU = int(binary.NativeEndian.Uint32(a.Value[:4]))
			}
		}
	}
	return ifi
}

func linkFlags(rawFlags uint32) net.Flags {
	var f net.Flags
	if rawFlags&syscall.IFF_UP != 0 {
		f |= net.FlagUp
	}
	if rawFlags&syscall.IFF_RUNNING != 0 {
		f |= net.FlagRunning
	}
	if rawFlags&syscall.IFF_BROADCAST != 0 {
		f |= net.FlagBroadcast
	}
	if rawFlags&syscall.IFF_LOOPBACK != 0 {
		f |= net.FlagLoopback
	}
	if rawFlags&syscall.IFF_POINTOPOINT != 0 {
		f |= net.FlagPointToPoint
	}
	if rawFlags&syscall.IFF_MULTICAST != 0 {
		f |= net.FlagMulticast
	}
	return f
}
