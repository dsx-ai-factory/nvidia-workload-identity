// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"testing"

	"github.com/vishvananda/netlink"
)

type testLinks []netlink.Link

func (links testLinks) LinkByName(name string) (netlink.Link, error) {
	for _, link := range links {
		if link.Attrs().Name == name {
			return link, nil
		}
	}
	return nil, errors.New("link not found")
}

func (links testLinks) LinkByIndex(index int) (netlink.Link, error) {
	for _, link := range links {
		if link.Attrs().Index == index {
			return link, nil
		}
	}
	return nil, errors.New("link not found")
}

func TestFindPrimaryBridge(t *testing.T) {
	secondary := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "k6t-secondary", Index: 1}}
	primary := &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "k6t-ovn-udn1", Index: 2}}
	tap := &netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "tap0", Index: 3, MasterIndex: 2}}
	for _, test := range []struct {
		name  string
		links testLinks
	}{
		{"secondary first", testLinks{secondary, primary, tap}},
		{"primary first", testLinks{primary, tap, secondary}},
	} {
		t.Run(test.name, func(t *testing.T) {
			bridge, err := findBridge(test.links)
			if err != nil {
				t.Fatal(err)
			}
			if bridge != primary {
				t.Fatalf("selected %s, want %s", bridge.Attrs().Name, primary.Attrs().Name)
			}
		})
	}
}

func TestFindBridgeRejectsInvalidAttachment(t *testing.T) {
	for _, test := range []struct {
		name  string
		links testLinks
	}{
		{"missing tap", testLinks{&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "k6t-eth0", Index: 1}}}},
		{"unattached tap", testLinks{&netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "tap0", Index: 1}}}},
		{"missing master", testLinks{&netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "tap0", Index: 1, MasterIndex: 2}}}},
		{"other bridge", testLinks{
			&netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "tap0", Index: 1, MasterIndex: 2}},
			&netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: "other", Index: 2}},
		}},
		{"non-bridge master", testLinks{
			&netlink.Tuntap{LinkAttrs: netlink.LinkAttrs{Name: "tap0", Index: 1, MasterIndex: 2}},
			&netlink.Bond{LinkAttrs: netlink.LinkAttrs{Name: "k6t-bond", Index: 2}},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if bridge, err := findBridge(test.links); err == nil {
				t.Fatalf("accepted invalid primary attachment: %s", bridge.Attrs().Name)
			}
		})
	}
}
