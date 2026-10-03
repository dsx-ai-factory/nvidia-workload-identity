package main

import (
	"errors"
	"net"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

type testNeighborDeleter struct {
	deleted *netlink.Neigh
	err     error
}

func (nl *testNeighborDeleter) NeighDel(neighbor *netlink.Neigh) error {
	nl.deleted = neighbor
	return nl.err
}

func TestClearVMNeighbor(t *testing.T) {
	link := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: vethContainerSide, Index: 7}}
	ip := net.ParseIP("10.1.0.7")
	for _, test := range []struct {
		name string
		err  error
	}{
		{"existing mapping", nil},
		{"no mapping", unix.ENOENT},
		{"permission denied", unix.EPERM},
	} {
		t.Run(test.name, func(t *testing.T) {
			nl := &testNeighborDeleter{err: test.err}
			err := clearVMNeighbor(nl, link, ip)
			if test.err == unix.EPERM {
				if !errors.Is(err, test.err) {
					t.Fatalf("error = %v, want %v", err, test.err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if nl.deleted == nil || nl.deleted.LinkIndex != 7 || !nl.deleted.IP.Equal(ip) {
				t.Fatalf("did not target the guest neighbor on %s: %+v", vethContainerSide, nl.deleted)
			}
		})
	}
}
