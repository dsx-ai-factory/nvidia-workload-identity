// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"net"
	"reflect"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

type testNetworkWriter struct {
	family            int
	routes            []netlink.Route
	deleted, replaced []*netlink.Route
	addr              *netlink.Addr
	neighbor          *netlink.Neigh
	err               error
}

func (w *testNetworkWriter) RouteListFiltered(family int, _ *netlink.Route, _ uint64) ([]netlink.Route, error) {
	w.family = family
	return w.routes, w.err
}
func (w *testNetworkWriter) RouteDel(route *netlink.Route) error {
	w.deleted = append(w.deleted, route)
	return w.err
}
func (w *testNetworkWriter) RouteReplace(route *netlink.Route) error {
	w.replaced = append(w.replaced, route)
	return w.err
}
func (w *testNetworkWriter) AddrReplace(_ netlink.Link, addr *netlink.Addr) error {
	w.addr = addr
	return w.err
}
func (w *testNetworkWriter) NeighSet(neighbor *netlink.Neigh) error {
	w.neighbor = neighbor
	return w.err
}

func TestGuestReplyPath(t *testing.T) {
	for _, test := range []struct {
		guest, metadata, prefix string
		family, flags           int
	}{
		{"10.1.0.7", imdsIP, "10.1.0.7/32", netlink.FAMILY_V4, 0},
		{"fd00::7", imdsIPv6, "fd00::7/128", netlink.FAMILY_V6, unix.IFA_F_NODAD},
	} {
		t.Run(test.guest, func(t *testing.T) {
			ip := net.ParseIP(test.guest)
			link := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Index: 9, Name: vethContainerSide}}
			mac := net.HardwareAddr{2, 0, 0, 0, 0, 7}
			local := netlink.Route{Dst: hostPrefix(ip), Type: unix.RTN_LOCAL, Table: unix.RT_TABLE_LOCAL}
			other := netlink.Route{Dst: hostPrefix(net.ParseIP("fd00::8")), Type: unix.RTN_LOCAL}
			writer := &testNetworkWriter{routes: []netlink.Route{local, other, {Dst: hostPrefix(ip), Type: unix.RTN_UNICAST}, {}}}
			if err := deleteLocalRouteForIP(writer, ip); err != nil {
				t.Fatal(err)
			}
			if writer.family != test.family || len(writer.deleted) != 1 || !reflect.DeepEqual(*writer.deleted[0], local) {
				t.Fatalf("local route deletion: family=%d routes=%v", writer.family, writer.deleted)
			}
			if err := addVMRoute(writer, link, ip); err != nil {
				t.Fatal(err)
			}
			if len(writer.replaced) != 1 || writer.replaced[0].Dst.String() != test.prefix || writer.replaced[0].LinkIndex != 9 {
				t.Fatalf("reply route: %v", writer.replaced)
			}
			if err := addVMNeighbor(writer, link, ip, mac); err != nil {
				t.Fatal(err)
			}
			if writer.neighbor.LinkIndex != 9 || !writer.neighbor.IP.Equal(ip) || !reflect.DeepEqual(writer.neighbor.HardwareAddr, mac) || writer.neighbor.State != netlink.NUD_PERMANENT {
				t.Fatalf("reply neighbor: %v", writer.neighbor)
			}
			if err := assignIMDSIP(writer, link, net.ParseIP(test.metadata)); err != nil {
				t.Fatal(err)
			}
			if !writer.addr.IP.Equal(net.ParseIP(test.metadata)) || writer.addr.Flags != test.flags {
				t.Fatalf("metadata address: %v", writer.addr)
			}
			writer.err = errors.New("netlink failure")
			for _, err := range []error{
				assignIMDSIP(writer, link, net.ParseIP(test.metadata)),
				addVMRoute(writer, link, ip),
				addVMNeighbor(writer, link, ip, mac),
				deleteLocalRouteForIP(writer, ip),
			} {
				if !errors.Is(err, writer.err) {
					t.Fatalf("lost netlink error: %v", err)
				}
			}
		})
	}
}
